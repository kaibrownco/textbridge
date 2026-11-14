package gateway

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const redirect = "https://claude.ai/api/mcp/auth_callback"

type fixture struct {
	t        *testing.T
	gw       *httptest.Server
	upstream *httptest.Server
	srv      *Server
	pw       string
	client   *http.Client
	lastAuth string // Authorization header seen upstream
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, pw: GeneratePassphrase()}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization") + "|" + r.Header.Get("Cookie")
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet {
			// Simulate an SSE stream.
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			fmt.Fprint(w, "data: first\n\n")
			fl.Flush()
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, "data: second\n\n")
			fl.Flush()
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"echo":%q}}`, string(body))
	}))
	t.Cleanup(f.upstream.Close)

	hash, err := HashPassword(f.pw)
	if err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.gw = httptest.NewUnstartedServer(nil)
	f.gw.Start()
	t.Cleanup(f.gw.Close)
	f.srv, err = New(Config{
		PublicURL:     f.gw.URL,
		Upstream:      f.upstream.URL,
		RedirectHosts: []string{"claude.ai", "claude.com"},
	}, st, hash)
	if err != nil {
		t.Fatal(err)
	}
	f.gw.Config.Handler = f.srv
	f.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return f
}

func (f *fixture) postJSON(path string, v any) *http.Response {
	b, _ := json.Marshal(v)
	resp, err := f.client.Post(f.gw.URL+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

func decode(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	defer r.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func pkcePair() (verifier, challenge string) {
	verifier = randomToken(48)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

var reqRe = regexp.MustCompile(`name="req" value="([^"]+)"`)

// login runs register → authorize → passphrase → token, returning tokens.
func (f *fixture) login(pw string) (access, refresh string, clientID string) {
	t := f.t
	reg := f.postJSON("/register", map[string]any{"client_name": "Claude", "redirect_uris": []string{redirect}})
	if reg.StatusCode != 201 {
		t.Fatalf("register: %d", reg.StatusCode)
	}
	clientID = decode(t, reg)["client_id"].(string)
	verifier, challenge := pkcePair()
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"xyz"},
		"resource": {f.gw.URL + "/mcp"},
	}
	resp, err := f.client.Get(f.gw.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := reqRe.FindSubmatch(page)
	if resp.StatusCode != 200 || m == nil {
		t.Fatalf("authorize page: %d %s", resp.StatusCode, page)
	}
	resp, err = f.client.PostForm(f.gw.URL+"/authorize", url.Values{"req": {string(m[1])}, "passphrase": {pw}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		return "", "", clientID
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Host != "claude.ai" || loc.Query().Get("state") != "xyz" {
		t.Fatalf("bad redirect %s", loc)
	}
	tok, err := f.client.PostForm(f.gw.URL+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.StatusCode != 200 {
		b, _ := io.ReadAll(tok.Body)
		t.Fatalf("token: %d %s", tok.StatusCode, b)
	}
	tm := decode(t, tok)
	return tm["access_token"].(string), tm["refresh_token"].(string), clientID
}

func (f *fixture) mcp(method, token, body string) *http.Response {
	req, _ := http.NewRequest(method, f.gw.URL+"/mcp", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Cookie", "secret=1")
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

func TestUnauthenticatedMCPGetsDiscoveryChallenge(t *testing.T) {
	f := newFixture(t)
	resp := f.mcp("POST", "", `{}`)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), f.gw.URL+"/.well-known/oauth-protected-resource") {
		t.Fatalf("missing resource_metadata: %q", resp.Header.Get("WWW-Authenticate"))
	}
	if f.lastAuth != "" {
		t.Fatal("request reached upstream without a token")
	}
	resp = f.mcp("POST", "made-up-token", `{}`)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("bogus token: want 401, got %d", resp.StatusCode)
	}
}

func TestMetadata(t *testing.T) {
	f := newFixture(t)
	r, _ := f.client.Get(f.gw.URL + "/.well-known/oauth-protected-resource")
	m := decode(t, r)
	if m["resource"] != f.gw.URL+"/mcp" {
		t.Fatalf("resource %v", m["resource"])
	}
	r, _ = f.client.Get(f.gw.URL + "/.well-known/oauth-authorization-server")
	m = decode(t, r)
	if m["registration_endpoint"] != f.gw.URL+"/register" || m["issuer"] != f.gw.URL {
		t.Fatalf("as metadata %v", m)
	}
}

func TestFullFlowProxyAndRefresh(t *testing.T) {
	f := newFixture(t)
	access, refresh, clientID := f.login(f.pw)
	if access == "" {
		t.Fatal("login failed with right passphrase")
	}

	resp := f.mcp("POST", access, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "tools/list") {
		t.Fatalf("proxy: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Mcp-Session-Id") != "sess-1" {
		t.Fatal("session header not passed through")
	}
	if f.lastAuth != "|" {
		t.Fatalf("Authorization/Cookie leaked upstream: %q", f.lastAuth)
	}

	// Streaming: first event must arrive before the stream ends.
	resp = f.mcp("GET", access, "")
	rd := bufio.NewReader(resp.Body)
	line, _ := rd.ReadString('\n')
	if line != "data: first\n" {
		t.Fatalf("stream: %q", line)
	}
	resp.Body.Close()

	// Refresh rotates: new tokens work, old refresh token is dead.
	r, _ := f.client.PostForm(f.gw.URL+"/token", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID},
	})
	tm := decode(t, r)
	if tm["access_token"] == nil {
		t.Fatalf("refresh failed: %v", tm)
	}
	r, _ = f.client.PostForm(f.gw.URL+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("old refresh token reused")
	}
	resp = f.mcp("POST", tm["access_token"].(string), `{}`)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("refreshed access token rejected")
	}
}

func TestWrongPassphraseAndLockout(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < maxFailures; i++ {
		if a, _, _ := f.login("wrong"); a != "" {
			t.Fatal("wrong passphrase accepted")
		}
	}
	// Now even the right passphrase is refused until the lock expires.
	if a, _, _ := f.login(f.pw); a != "" {
		t.Fatal("login allowed during lockout")
	}
	f.srv.mu.Lock()
	f.srv.lockedTo = time.Time{}
	f.srv.mu.Unlock()
	if a, _, _ := f.login(f.pw); a == "" {
		t.Fatal("login refused after lock cleared")
	}
}

func TestRegisterRejectsForeignRedirects(t *testing.T) {
	f := newFixture(t)
	for _, u := range []string{"https://evil.example/cb", "http://claude.ai/cb", "https://claude.ai.evil.com/cb"} {
		r := f.postJSON("/register", map[string]any{"redirect_uris": []string{u}})
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Fatalf("%s accepted", u)
		}
	}
}

func TestCodeIsSingleUseAndPKCEEnforced(t *testing.T) {
	f := newFixture(t)
	reg := f.postJSON("/register", map[string]any{"redirect_uris": []string{redirect}})
	cid := decode(t, reg)["client_id"].(string)
	verifier, challenge := pkcePair()
	q := url.Values{"response_type": {"code"}, "client_id": {cid}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	resp, _ := f.client.Get(f.gw.URL + "/authorize?" + q.Encode())
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := reqRe.FindSubmatch(page)
	resp, _ = f.client.PostForm(f.gw.URL+"/authorize", url.Values{"req": {string(m[1])}, "passphrase": {f.pw}})
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")

	bad := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {cid},
		"redirect_uri": {redirect}, "code_verifier": {randomToken(48)}}
	r, _ := f.client.PostForm(f.gw.URL+"/token", bad)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("wrong PKCE verifier accepted")
	}
	// The failed attempt burned the code, so the right verifier fails too.
	bad.Set("code_verifier", verifier)
	r, _ = f.client.PostForm(f.gw.URL+"/token", bad)
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("code reused")
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	h, _ := HashPassword("swan-ridge-41")
	if !VerifyPassword(h, "swan-ridge-41") || VerifyPassword(h, "swan-ridge-42") || VerifyPassword("junk", "x") {
		t.Fatal("password verification wrong")
	}
	seen := map[string]bool{}
	for _, w := range passWords {
		if seen[w] {
			t.Fatalf("duplicate word %q", w)
		}
		seen[w] = true
	}
	if len(passWords) < 200 {
		t.Fatalf("word list has %d words, want at least 200", len(passWords))
	}
}

func TestStatePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenStore(dir)
	tok, _ := st.IssueToken("c1", "access", time.Hour)
	st2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st2.CheckToken(tok, "access"); !ok {
		t.Fatal("token lost across restart")
	}
	if _, ok := st2.CheckToken(tok, "refresh"); ok {
		t.Fatal("access token accepted as refresh token")
	}
}
