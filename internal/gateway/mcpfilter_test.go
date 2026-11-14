package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstructionsAndToolFiltering(t *testing.T) {
	var calls []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m rpcMsg
		_ = json.Unmarshal(b, &m)
		calls = append(calls, m.Method)
		switch m.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"openmessage","version":"x"},"capabilities":{}}}`)
		case "tools/list":
			// Answer as SSE to cover that path.
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: "+`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"send_message","description":"Send a text."},{"name":"import_messages","description":"x"},{"name":"get_conversation","description":"Get."}]}}`+"\n\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":3,"result":{"content":[]}}`)
		}
	}))
	defer up.Close()

	f := newFixture(t)
	srv, err := New(Config{PublicURL: f.gw.URL, Upstream: up.URL, RedirectHosts: []string{"claude.ai"}}, f.srv.store, f.srv.pwHash)
	if err != nil {
		t.Fatal(err)
	}
	f.gw.Config.Handler = srv
	f.srv = srv
	access, _, _ := f.login(f.pw)

	resp := f.mcp("POST", access, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	m := decode(t, resp)
	res := m["result"].(map[string]any)
	if !strings.Contains(res["instructions"].(string), "ASK BEFORE SENDING") {
		t.Fatalf("instructions missing: %v", res)
	}
	if res["serverInfo"].(map[string]any)["name"] != "textbridge" {
		t.Fatal("server name not rewritten")
	}

	resp = f.mcp("POST", access, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s := string(body)
	if strings.Contains(s, "import_messages") || !strings.Contains(s, "get_conversation") {
		t.Fatalf("tool filter wrong: %s", s)
	}
	if !strings.Contains(s, "ONLY call this after Kai has approved") || !strings.HasPrefix(s, "event: message\ndata: ") {
		t.Fatalf("send note or SSE framing missing: %s", s)
	}

	n := len(calls)
	resp = f.mcp("POST", access, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"import_messages","arguments":{}}}`)
	m = decode(t, resp)
	if m["error"] == nil || len(calls) != n {
		t.Fatalf("hidden tool not blocked: %v (upstream calls %d→%d)", m, n, len(calls))
	}
	resp = f.mcp("POST", access, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_conversation","arguments":{}}}`)
	m = decode(t, resp)
	if m["result"] == nil {
		t.Fatalf("allowed tool failed: %v", m)
	}
}
