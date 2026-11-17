package gateway

import (
	"bufio"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Web pairing: the user pastes a Google cookie header on /pair (behind the
// passphrase). The gateway drops it in pair/request.cookie; a root systemd
// path unit (textbridge-pair.path) runs `textbridge pair-from-file`, which
// stops OpenMessage, pairs, and reports progress in pair/status as
// key=value lines (state, emoji, message). /pair/wait shows that status.

const pairTokenTTL = 10 * time.Minute

func (s *Server) pairDir() string { return filepath.Join(s.cfg.StateDir, "pair") }

var pairTmpl = template.Must(template.New("pair").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
{{if .Refresh}}<meta http-equiv="refresh" content="2">{{end}}
<title>Link your phone · textbridge</title>
<style>
body{font-family:-apple-system,system-ui,sans-serif;max-width:560px;margin:6vh auto;padding:0 20px;color:#222;line-height:1.45}
h1{font-size:1.35rem}input,textarea{width:100%;box-sizing:border-box;font-size:1rem;padding:10px;margin:6px 0 14px;border:1px solid #bbb;border-radius:8px}
textarea{height:140px;font-family:ui-monospace,monospace;font-size:.8rem}
button{width:100%;font-size:1.05rem;padding:12px;border:0;border-radius:8px;background:#1a73e8;color:#fff}
ol{padding-left:1.2em}li{margin:.35em 0}.err{color:#b00020}.ok{color:#137333}
.emoji{font-size:5rem;text-align:center;margin:.2em 0}.muted{color:#666;font-size:.9rem}
</style></head><body>
{{if eq .View "form"}}
<h1>Link your phone</h1>
<ol>
<li>Use <b>Safari or Firefox</b>, not Chrome (Chrome ties the sign-in to itself and the link dies within hours).</li>
<li>One time: install the free <b>Cookie-Editor</b> extension (cookie-editor.com, for Safari or Firefox) and allow it in private windows.</li>
<li>Open a <b>private window</b>, go to <b>messages.google.com/web</b> and sign in with the Google account your phone uses.</li>
<li>Click the Cookie-Editor icon › <b>Export</b> › <b>Header String</b>. That copies everything needed.</li>
<li>Paste it below. Keep Google Messages open on your phone so you can tap the emoji.</li>
</ol>
<p class="muted">No extension? Developer tools work too: Network tab, reload, click the first messages.google.com request, copy the whole <b>Cookie</b> request header.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/pair">
<textarea name="cookie" required placeholder="SID=…; HSID=…; SSID=…; … OSID=…"></textarea>
<input type="password" name="passphrase" autocomplete="current-password" required placeholder="textbridge passphrase">
<button type="submit">Link phone</button>
</form>
<p class="muted">Afterwards, close that private window but don't sign out of it.</p>
{{else if eq .View "waiting"}}
<h1>Connecting…</h1><p>Getting a code from Google. Keep Google Messages open on your phone.</p>
{{else if eq .View "emoji"}}
<h1>Tap this on your phone</h1><div class="emoji">{{.Emoji}}</div>
<p>Google Messages on your phone is showing a few emoji. Tap this one.</p>
{{else if eq .View "success"}}
<h1 class="ok">Phone linked ✓</h1><p>Your texts are syncing in the background. You can close this page, and the private window too (don't sign out of it).</p>
{{else}}
<h1 class="err">That didn't work</h1><p>{{.Error}}</p><p><a href="/pair">Try again</a> with a fresh cookie.</p>
{{end}}
</body></html>`))

func (s *Server) renderPair(w http.ResponseWriter, status int, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = pairTmpl.Execute(w, data)
}

func (s *Server) handlePairGet(w http.ResponseWriter, _ *http.Request) {
	s.renderPair(w, 200, map[string]any{"View": "form"})
}

// normalizeCookie accepts the formats people actually end up with and turns
// them into a Cookie header string: the header itself (with or without a
// "Cookie:" prefix), a Cookie-Editor / devtools JSON export (array of
// {name, value, domain}), or a Netscape cookies.txt export.
func normalizeCookie(in string) string {
	in = strings.TrimSpace(in)
	for _, p := range []string{"Cookie:", "cookie:"} {
		in = strings.TrimSpace(strings.TrimPrefix(in, p))
	}
	googleDomain := func(d string) bool { return d == "" || strings.HasSuffix(strings.TrimPrefix(d, "."), "google.com") }
	join := func(pairs [][2]string) string {
		seen := map[string]bool{}
		var parts []string
		for _, p := range pairs {
			if p[0] == "" || seen[p[0]] {
				continue
			}
			seen[p[0]] = true
			parts = append(parts, p[0]+"="+p[1])
		}
		return strings.Join(parts, "; ")
	}
	if strings.HasPrefix(in, "[") {
		var arr []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		}
		if json.Unmarshal([]byte(in), &arr) == nil {
			var pairs [][2]string
			for _, c := range arr {
				if googleDomain(c.Domain) {
					pairs = append(pairs, [2]string{c.Name, c.Value})
				}
			}
			return join(pairs)
		}
	}
	if strings.Contains(in, "\t") && !strings.Contains(in, "; ") {
		var pairs [][2]string
		for _, line := range strings.Split(in, "\n") {
			f := strings.Split(strings.TrimRight(line, "\r"), "\t")
			if len(f) == 7 && !strings.HasPrefix(f[0], "# ") && googleDomain(strings.TrimPrefix(f[0], "#HttpOnly_")) {
				pairs = append(pairs, [2]string{f[5], f[6]})
			}
		}
		if len(pairs) > 0 {
			return join(pairs)
		}
	}
	return strings.Join(strings.Fields(in), " ")
}

// cookieLooksRight checks for the names pairing needs, so a wrong paste
// (the Storage table, a different site's request) is caught here.
func cookieLooksRight(c string) bool {
	if !strings.Contains(c, "=") || !strings.Contains(c, ";") {
		return false
	}
	for _, name := range []string{"SID=", "HSID=", "SSID=", "APISID=", "SAPISID=", "OSID="} {
		if !strings.Contains(c, name) {
			return false
		}
	}
	return true
}

func (s *Server) handlePairPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		s.renderPair(w, 400, map[string]any{"View": "form", "Error": "Couldn't read the form."})
		return
	}
	s.mu.Lock()
	locked := s.now().Before(s.lockedTo)
	s.mu.Unlock()
	if locked {
		s.renderPair(w, 429, map[string]any{"View": "form", "Error": "Too many wrong tries. Wait 15 minutes and try again."})
		return
	}
	if !VerifyPassword(s.pwHash, r.PostForm.Get("passphrase")) {
		s.recordFailure()
		log.Printf("failed pairing-page sign-in")
		s.renderPair(w, 401, map[string]any{"View": "form", "Error": "That passphrase isn't right."})
		return
	}
	cookie := normalizeCookie(r.PostForm.Get("cookie"))
	if !cookieLooksRight(cookie) {
		s.renderPair(w, 400, map[string]any{"View": "form", "Error": "That doesn't look like a complete Google Messages sign-in. It needs SID, HSID, SSID, APISID, SAPISID and OSID. Make sure you're signed in on messages.google.com/web (not another Google page), then export from Cookie-Editor as Header String, or copy the Cookie request header from the Network tab."})
		return
	}
	dir := s.pairDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.renderPair(w, 500, map[string]any{"View": "error", "Error": "Server couldn't save the request."})
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "status"), []byte("state=queued\n"), 0o600)
	tmp := filepath.Join(dir, ".request.tmp")
	if err := os.WriteFile(tmp, []byte(cookie+"\n"), 0o600); err != nil {
		s.renderPair(w, 500, map[string]any{"View": "error", "Error": "Server couldn't save the request."})
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, "request.cookie")); err != nil {
		s.renderPair(w, 500, map[string]any{"View": "error", "Error": "Server couldn't save the request."})
		return
	}
	tok := randomToken(24)
	s.mu.Lock()
	s.failures = nil
	s.pairTokens[tok] = s.now().Add(pairTokenTTL)
	s.mu.Unlock()
	log.Printf("pairing requested from the web page")
	http.Redirect(w, r, "/pair/wait?t="+tok, http.StatusSeeOther)
}

func readPairStatus(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			out[k] = v
		}
	}
	return out
}

func (s *Server) handlePairWait(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("t")
	s.mu.Lock()
	exp, ok := s.pairTokens[tok]
	s.mu.Unlock()
	if !ok || s.now().After(exp) {
		http.Redirect(w, r, "/pair", http.StatusSeeOther)
		return
	}
	st := readPairStatus(filepath.Join(s.pairDir(), "status"))
	switch st["state"] {
	case "emoji":
		s.renderPair(w, 200, map[string]any{"View": "emoji", "Emoji": st["emoji"], "Refresh": true})
	case "success":
		s.renderPair(w, 200, map[string]any{"View": "success"})
	case "error":
		msg := friendlyPairError(st["message"])
		s.renderPair(w, 200, map[string]any{"View": "error", "Error": msg})
	default: // queued, running
		s.renderPair(w, 200, map[string]any{"View": "waiting", "Refresh": true})
	}
}

// gaiaErrorNames covers Google's GaiaPairingErrorCode values that the pinned
// libgm proto predates, so its errors carry only the number ("3/32").
var gaiaErrorNames = map[string]string{
	"32": "CLIENT_ATTESTATION_MISSING",
	"33": "CLIENT_ATTESTATION_MISMATCH",
	"34": "SI_RESPONSE_FROM_UNEXPECTED_PHONE_REGISTRATION_ID",
	"35": "CLIENT_ATTESTATION_REVISION_MISMATCH",
}

// friendlyPairError turns OpenMessage's raw pairing error into something a
// person can act on, keeping the raw text so it can still be reported.
func friendlyPairError(raw string) string {
	if raw == "" {
		return "Pairing failed."
	}
	const marker = "unknown error pairing: "
	i := strings.Index(raw, marker)
	if i < 0 {
		return raw
	}
	code := raw[i+len(marker):]
	if j := strings.LastIndex(code, "/"); j >= 0 {
		code = code[j+1:]
	}
	code = strings.TrimSpace(code)
	name, ok := gaiaErrorNames[code]
	if !ok {
		return raw
	}
	switch name {
	case "CLIENT_ATTESTATION_MISSING", "CLIENT_ATTESTATION_MISMATCH", "CLIENT_ATTESTATION_REVISION_MISMATCH":
		return "Google refused the link (" + name + "). Newer versions of the Messages app ask linking devices to prove they're an official Google client, which textbridge can't do, so retrying won't help. Your cookie is fine. Workaround: on your phone open Settings > Apps > Messages > ⋮ > Uninstall updates, turn off auto-update for Messages in the Play Store, then pair again. (Details: " + raw + ")"
	default:
		return "Google refused the link (" + name + "). Try again with a fresh cookie; if it keeps happening, report this message. (Details: " + raw + ")"
	}
}
