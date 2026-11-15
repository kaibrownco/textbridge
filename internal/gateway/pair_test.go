package gateway

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodCookie = "SID=a; HSID=b; SSID=c; APISID=d; SAPISID=e; __Secure-1PSIDTS=f; OSID=g"

func pairFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.srv.cfg.StateDir = t.TempDir()
	return f
}

func (f *fixture) pairPost(cookie, pw string) *http.Response {
	resp, err := f.client.PostForm(f.gw.URL+"/pair", url.Values{"cookie": {cookie}, "passphrase": {pw}})
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

func body(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

func TestPairPageFlow(t *testing.T) {
	f := pairFixture(t)
	reqFile := filepath.Join(f.srv.pairDir(), "request.cookie")

	r, _ := f.client.Get(f.gw.URL + "/pair")
	if b := body(r); r.StatusCode != 200 || !strings.Contains(b, "Safari or Firefox") {
		t.Fatalf("form: %d", r.StatusCode)
	}

	// Wrong passphrase: nothing queued.
	r = f.pairPost(goodCookie, "nope")
	body(r)
	if r.StatusCode != 401 {
		t.Fatalf("wrong passphrase: %d", r.StatusCode)
	}
	if _, err := os.Stat(reqFile); err == nil {
		t.Fatal("request queued with wrong passphrase")
	}

	// Storage-table style paste is rejected with guidance.
	r = f.pairPost("SIDabc\nHSIDdef\nOSIDghi", f.pw)
	if b := body(r); r.StatusCode != 400 || !strings.Contains(b, "Header String") {
		t.Fatalf("bad cookie: %d", r.StatusCode)
	}

	// Good request is queued, with "Cookie:" prefix stripped.
	r = f.pairPost("Cookie: "+goodCookie, f.pw)
	body(r)
	if r.StatusCode != 303 || !strings.HasPrefix(r.Header.Get("Location"), "/pair/wait?t=") {
		t.Fatalf("submit: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	got, err := os.ReadFile(reqFile)
	if err != nil || strings.TrimSpace(string(got)) != goodCookie {
		t.Fatalf("queued cookie wrong: %q %v", got, err)
	}
	if fi, _ := os.Stat(reqFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("request file mode %v", fi.Mode().Perm())
	}
	wait := f.gw.URL + r.Header.Get("Location")

	status := filepath.Join(f.srv.pairDir(), "status")
	check := func(content, want string) {
		t.Helper()
		if content != "" {
			os.WriteFile(status, []byte(content), 0o600)
		}
		r, _ := f.client.Get(wait)
		if b := body(r); !strings.Contains(b, want) {
			t.Fatalf("status %q: page missing %q", content, want)
		}
	}
	check("", "Connecting")
	check("state=emoji\nemoji=🧶\n", "🧶")
	check("state=error\nmessage=client init timed out\n", "client init timed out")
	check("state=success\n", "Phone linked")

	// Bad or missing token goes back to the form.
	r, _ = f.client.Get(f.gw.URL + "/pair/wait?t=forged")
	body(r)
	if r.StatusCode != 303 {
		t.Fatalf("forged token: %d", r.StatusCode)
	}
}

func TestPairPageEscapesStatus(t *testing.T) {
	f := pairFixture(t)
	r := f.pairPost(goodCookie, f.pw)
	body(r)
	os.WriteFile(filepath.Join(f.srv.pairDir(), "status"), []byte("state=error\nmessage=<script>x</script>\n"), 0o600)
	r, _ = f.client.Get(f.gw.URL + r.Header.Get("Location"))
	if b := body(r); strings.Contains(b, "<script>x") {
		t.Fatal("status message not escaped")
	}
}

func TestNormalizeCookieFormats(t *testing.T) {
	want := "SID=a; HSID=b; SSID=c; APISID=d; SAPISID=e; OSID=g"
	cases := map[string]string{
		"header":  "Cookie: SID=a; HSID=b; SSID=c; APISID=d; SAPISID=e; OSID=g",
		"wrapped": "SID=a; HSID=b;\n SSID=c; APISID=d; SAPISID=e; OSID=g",
		"json": `[{"name":"SID","value":"a","domain":".google.com"},{"name":"HSID","value":"b","domain":".google.com"},
			{"name":"SSID","value":"c","domain":".google.com"},{"name":"APISID","value":"d","domain":".google.com"},
			{"name":"SAPISID","value":"e","domain":".google.com"},{"name":"OSID","value":"g","domain":"messages.google.com"},
			{"name":"X","value":"z","domain":".evil.com"}]`,
		"netscape": ".google.com\tTRUE\t/\tTRUE\t0\tSID\ta\n#HttpOnly_.google.com\tTRUE\t/\tTRUE\t0\tHSID\tb\n" +
			".google.com\tTRUE\t/\tTRUE\t0\tSSID\tc\n.google.com\tTRUE\t/\tTRUE\t0\tAPISID\td\n" +
			".google.com\tTRUE\t/\tTRUE\t0\tSAPISID\te\nmessages.google.com\tFALSE\t/\tTRUE\t0\tOSID\tg\n",
	}
	for name, in := range cases {
		if got := normalizeCookie(in); got != want {
			t.Errorf("%s: got %q", name, got)
		}
		if !cookieLooksRight(normalizeCookie(in)) {
			t.Errorf("%s: rejected", name)
		}
	}
}
