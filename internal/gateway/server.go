// Package gateway puts an OAuth 2.1 login in front of OpenMessage's MCP
// endpoint so it can be added to Claude as a remote custom connector.
//
// It implements just enough of the MCP authorization spec for one user:
// protected-resource metadata (RFC 9728), authorization-server metadata
// (RFC 8414), dynamic client registration (RFC 7591), and the authorization
// code flow with PKCE S256. The "user" signs in with a single passphrase.
package gateway

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	accessTTL    = time.Hour
	refreshTTL   = 60 * 24 * time.Hour
	codeTTL      = 5 * time.Minute
	pendingTTL   = 10 * time.Minute
	maxFailures  = 5
	lockDuration = 15 * time.Minute
)

type Config struct {
	PublicURL string // e.g. https://203-0-113-7.sslip.io (no trailing slash)
	Upstream  string // e.g. http://127.0.0.1:7007
	StateDir  string // where state.json and password live
	// RedirectHosts are the hosts allowed in registered redirect URIs.
	RedirectHosts []string
	// Instructions sent to Claude on connect (default: DefaultInstructions).
	Instructions string
	// Tools Claude may see and call (default: DefaultTools).
	Tools []string
}

type authCode struct {
	clientID    string
	redirectURI string
	challenge   string
	expires     time.Time
}

type pendingAuth struct {
	clientID    string
	redirectURI string
	challenge   string
	state       string
	expires     time.Time
}

type Server struct {
	cfg          Config
	store        *Store
	pwHash       string
	proxy        *httputil.ReverseProxy
	rewrite      *httputil.ReverseProxy
	instructions string
	tools        map[string]bool
	mux          *http.ServeMux
	now          func() time.Time

	mu       sync.Mutex
	codes    map[string]*authCode
	pending  map[string]*pendingAuth
	failures []time.Time
	// web pairing page tokens (token -> expiry)
	pairTokens map[string]time.Time
	lockedTo   time.Time
}

func New(cfg Config, store *Store, pwHash string) (*Server, error) {
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	up, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:        cfg,
		store:      store,
		pwHash:     pwHash,
		now:        time.Now,
		codes:      map[string]*authCode{},
		pending:    map[string]*pendingAuth{},
		pairTokens: map[string]time.Time{},
		mux:        http.NewServeMux(),
	}
	s.instructions = cfg.Instructions
	if strings.TrimSpace(s.instructions) == "" {
		s.instructions = DefaultInstructions
	}
	tools := cfg.Tools
	if len(tools) == 0 {
		tools = DefaultTools
	}
	s.tools = map[string]bool{}
	for _, t := range tools {
		if t = strings.TrimSpace(t); t != "" {
			s.tools[t] = true
		}
	}
	rewrite := func(r *httputil.ProxyRequest) {
		r.SetURL(up)
		r.Out.Header.Del("Authorization")
		r.Out.Header.Del("Cookie")
		r.Out.Header.Del("X-Forwarded-For")
		r.Out.Host = up.Host
	}
	errHandler := func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("upstream error: %v", err)
		http.Error(w, "messages backend unavailable", http.StatusBadGateway)
	}
	// Stream responses (SSE) immediately.
	s.proxy = &httputil.ReverseProxy{Rewrite: rewrite, FlushInterval: -1, ErrorHandler: errHandler}
	// For initialize / tools/list: buffer and edit the response.
	s.rewrite = &httputil.ReverseProxy{Rewrite: rewrite, ModifyResponse: s.rewriteResponse, ErrorHandler: errHandler}

	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	s.mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleResourceMetadata)
	s.mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handleResourceMetadata)
	s.mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleASMetadata)
	s.mux.HandleFunc("GET /.well-known/oauth-authorization-server/mcp", s.handleASMetadata)
	s.mux.HandleFunc("POST /register", s.handleRegister)
	s.mux.HandleFunc("GET /authorize", s.handleAuthorizeGet)
	s.mux.HandleFunc("POST /authorize", s.handleAuthorizePost)
	s.mux.HandleFunc("POST /token", s.handleToken)
	s.mux.HandleFunc("/mcp", s.handleMCP)
	s.mux.HandleFunc("GET /pair", s.handlePairGet)
	s.mux.HandleFunc("POST /pair", s.handlePairPost)
	s.mux.HandleFunc("GET /pair/wait", s.handlePairWait)
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("textbridge is running. Add " + s.cfg.PublicURL + "/mcp to Claude as a custom connector. Link or re-link your phone at " + s.cfg.PublicURL + "/pair\n"))
			return
		}
		http.NotFound(w, r)
	})
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) resourceURL() string { return s.cfg.PublicURL + "/mcp" }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"resource":                 s.resourceURL(),
		"authorization_servers":    []string{s.cfg.PublicURL},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "textbridge (Google Messages)",
	})
}

func (s *Server) handleASMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"issuer":                                s.cfg.PublicURL,
		"authorization_endpoint":                s.cfg.PublicURL + "/authorize",
		"token_endpoint":                        s.cfg.PublicURL + "/token",
		"registration_endpoint":                 s.cfg.PublicURL + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"messages"},
	})
}

// --- Dynamic client registration ---

func (s *Server) redirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.Host == "" {
		return false
	}
	host := u.Hostname()
	for _, h := range s.cfg.RedirectHosts {
		if host == h {
			// Plain http only for loopback (local testing).
			if u.Scheme == "https" || (u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1")) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, 400, "invalid_client_metadata", "bad JSON")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, 400, "invalid_redirect_uri", "redirect_uris required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !s.redirectAllowed(u) {
			oauthError(w, 400, "invalid_redirect_uri", "redirect URI not allowed: "+u)
			return
		}
	}
	name := req.ClientName
	if len(name) > 100 {
		name = name[:100]
	}
	c, err := s.store.AddClient(name, req.RedirectURIs)
	if err != nil {
		oauthError(w, 500, "server_error", "could not save client")
		return
	}
	writeJSON(w, 201, map[string]any{
		"client_id":                  c.ID,
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"client_id_issued_at":        c.CreatedAt.Unix(),
	})
}

// --- Authorization ---

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in to textbridge</title>
<style>
body{font-family:-apple-system,system-ui,sans-serif;max-width:380px;margin:12vh auto;padding:0 20px;color:#222}
h1{font-size:1.3rem}p{color:#555;line-height:1.4}input{width:100%;box-sizing:border-box;font-size:1.05rem;padding:12px;margin:8px 0 14px;border:1px solid #bbb;border-radius:8px}
button{width:100%;font-size:1.05rem;padding:12px;border:0;border-radius:8px;background:#1a73e8;color:#fff}
.err{color:#b00020}
</style></head><body>
<h1>Allow {{.Client}} to read and send your texts?</h1>
<p>Enter your textbridge passphrase to connect.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/authorize">
<input type="hidden" name="req" value="{{.Req}}">
<input type="password" name="passphrase" autocomplete="current-password" autofocus required placeholder="passphrase">
<button type="submit">Connect</button>
</form></body></html>`))

func (s *Server) renderLogin(w http.ResponseWriter, status int, reqID, client, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	// form-action also governs where the post-submit redirect may go, so the
	// allowed redirect hosts (claude.ai, claude.com) must be listed here or
	// the browser silently blocks the hand-off back to Claude.
	formAction := "'self'"
	for _, h := range s.cfg.RedirectHosts {
		if h == "localhost" || h == "127.0.0.1" {
			formAction += " http://" + h + ":*"
		} else {
			formAction += " https://" + h
		}
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+formAction+"; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = loginTmpl.Execute(w, map[string]string{"Req": reqID, "Client": client, "Error": errMsg})
}

func (s *Server) handleAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, ok := s.store.Client(q.Get("client_id"))
	if !ok {
		http.Error(w, "unknown client", 400)
		return
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" && len(c.RedirectURIs) == 1 {
		redirect = c.RedirectURIs[0]
	}
	registered := false
	for _, u := range c.RedirectURIs {
		if u == redirect {
			registered = true
		}
	}
	if !registered {
		http.Error(w, "redirect_uri not registered", 400)
		return
	}
	// From here on, errors go back to the client via redirect.
	if q.Get("response_type") != "code" {
		redirectErr(w, r, redirect, q.Get("state"), "unsupported_response_type")
		return
	}
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) < 43 {
		redirectErr(w, r, redirect, q.Get("state"), "invalid_request")
		return
	}
	if res := q.Get("resource"); res != "" && strings.TrimRight(res, "/") != s.resourceURL() && strings.TrimRight(res, "/") != s.cfg.PublicURL {
		redirectErr(w, r, redirect, q.Get("state"), "invalid_target")
		return
	}
	id := randomToken(24)
	s.mu.Lock()
	s.gcLocked()
	s.pending[id] = &pendingAuth{
		clientID: c.ID, redirectURI: redirect, challenge: q.Get("code_challenge"),
		state: q.Get("state"), expires: s.now().Add(pendingTTL),
	}
	s.mu.Unlock()
	name := c.Name
	if name == "" {
		name = "this app"
	}
	s.renderLogin(w, 200, id, name, "")
}

func redirectErr(w http.ResponseWriter, r *http.Request, redirect, state, code string) {
	u, _ := url.Parse(redirect)
	v := u.Query()
	v.Set("error", code)
	if state != "" {
		v.Set("state", state)
	}
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) gcLocked() {
	now := s.now()
	for k, p := range s.pending {
		if now.After(p.expires) {
			delete(s.pending, k)
		}
	}
	for k, c := range s.codes {
		if now.After(c.expires) {
			delete(s.codes, k)
		}
	}
}

func (s *Server) handleAuthorizePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	id := r.PostForm.Get("req")
	s.mu.Lock()
	s.gcLocked()
	p, ok := s.pending[id]
	locked := s.now().Before(s.lockedTo)
	s.mu.Unlock()
	if !ok {
		http.Error(w, "This sign-in link was already used or has expired. Go back to Claude and click Connect again.", 400)
		return
	}
	c, _ := s.store.Client(p.clientID)
	name := "this app"
	if c != nil && c.Name != "" {
		name = c.Name
	}
	if locked {
		s.renderLogin(w, 429, id, name, "Too many wrong tries. Wait 15 minutes and try again.")
		return
	}
	if !VerifyPassword(s.pwHash, r.PostForm.Get("passphrase")) {
		s.recordFailure()
		log.Printf("failed sign-in attempt")
		s.renderLogin(w, 401, id, name, "That passphrase isn't right.")
		return
	}
	code := randomToken(32)
	s.mu.Lock()
	delete(s.pending, id)
	s.failures = nil
	s.codes[code] = &authCode{clientID: p.clientID, redirectURI: p.redirectURI, challenge: p.challenge, expires: s.now().Add(codeTTL)}
	s.mu.Unlock()
	log.Printf("sign-in approved for client %q", name)

	u, _ := url.Parse(p.redirectURI)
	v := u.Query()
	v.Set("code", code)
	if p.state != "" {
		v.Set("state", p.state)
	}
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) recordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	cutoff := now.Add(-lockDuration)
	kept := s.failures[:0]
	for _, t := range s.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.failures = append(kept, now)
	if len(s.failures) >= maxFailures {
		s.lockedTo = now.Add(lockDuration)
		s.failures = nil
		log.Printf("sign-in locked for %s after %d failures", lockDuration, maxFailures)
	}
}

// --- Token endpoint ---

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "bad form")
		return
	}
	f := r.PostForm
	switch f.Get("grant_type") {
	case "authorization_code":
		s.mu.Lock()
		c, ok := s.codes[f.Get("code")]
		if ok {
			delete(s.codes, f.Get("code")) // single use
		}
		s.mu.Unlock()
		if !ok || s.now().After(c.expires) {
			oauthError(w, 400, "invalid_grant", "code invalid or expired")
			return
		}
		if f.Get("client_id") != c.clientID || f.Get("redirect_uri") != c.redirectURI {
			oauthError(w, 400, "invalid_grant", "client or redirect mismatch")
			return
		}
		if !pkceOK(f.Get("code_verifier"), c.challenge) {
			oauthError(w, 400, "invalid_grant", "PKCE check failed")
			return
		}
		s.issue(w, c.clientID)
	case "refresh_token":
		t, ok := s.store.ConsumeToken(f.Get("refresh_token"), "refresh")
		if !ok {
			oauthError(w, 400, "invalid_grant", "refresh token invalid or expired")
			return
		}
		if cid := f.Get("client_id"); cid != "" && cid != t.ClientID {
			oauthError(w, 400, "invalid_grant", "client mismatch")
			return
		}
		s.issue(w, t.ClientID)
	default:
		oauthError(w, 400, "unsupported_grant_type", "")
	}
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func (s *Server) issue(w http.ResponseWriter, clientID string) {
	at, err1 := s.store.IssueToken(clientID, "access", accessTTL)
	rt, err2 := s.store.IssueToken(clientID, "refresh", refreshTTL)
	if err1 != nil || err2 != nil {
		oauthError(w, 500, "server_error", "could not issue token")
		return
	}
	writeJSON(w, 200, map[string]any{
		"access_token":  at,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": rt,
		"scope":         "messages",
	})
}

// --- MCP proxy ---

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	tok := ""
	if strings.HasPrefix(auth, "Bearer ") {
		tok = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if _, ok := s.store.CheckToken(tok, "access"); !ok {
		w.Header().Set("WWW-Authenticate",
			`Bearer resource_metadata="`+s.cfg.PublicURL+`/.well-known/oauth-protected-resource"`)
		oauthError(w, 401, "invalid_token", "sign in required")
		return
	}
	if r.Method != http.MethodPost {
		s.proxy.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	msgs, ok := parseRPC(body)
	if ok {
		if id, name, blocked := s.blockedCall(msgs); blocked {
			log.Printf("blocked hidden tool %q", name)
			rpcError(w, id, "tool not available: "+name)
			return
		}
		if needsRewrite(msgs) {
			s.rewrite.ServeHTTP(w, r)
			return
		}
	}
	s.proxy.ServeHTTP(w, r)
}
