package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Server upkeep tools, added to the MCP tool list and answered by the
// gateway itself (never proxied). The gateway is unprivileged, so it drops a
// request in admin/request; a root systemd path unit (textbridge-admin.path)
// runs `textbridge admin-from-file`, which does one action from a fixed list
// and writes admin/result. There is deliberately no free-form command.

type adminTool struct {
	Name, Description string
	Schema            map[string]any
	Wait              time.Duration // how long to wait for the result
}

var adminTools = []adminTool{
	{
		Name:        "server_status",
		Description: "textbridge server health: whether each service is running, whether the phone link to Google Messages is up, recent link events, and the last update/re-sync. Use this first when texts seem to have stopped.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Wait:        25 * time.Second,
	},
	{
		Name:        "server_logs",
		Description: "Recent textbridge server log lines (noise filtered out), to diagnose problems.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"lines": map[string]any{"type": "number", "description": "How many lines (default 80, max 300)"},
		}},
		Wait: 25 * time.Second,
	},
	{
		Name:        "server_update",
		Description: "Install the latest textbridge version on the server (keeps the phone link, passphrase and Claude sign-ins). Takes 1-3 minutes and briefly disconnects this connector; check server_status afterwards. Tell the user before running it.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Wait:        10 * time.Second,
	},
	{
		Name:        "server_restart",
		Description: "Restart the textbridge services (Google Messages link and this connector). Use when the link is stuck. Briefly disconnects this connector. Tell the user before running it.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Wait:        10 * time.Second,
	},
	{
		Name:        "server_resync",
		Description: "Re-download the user's full text history from the phone (20-60 minutes; the phone should stay unlocked and on Wi-Fi). Use when old messages are missing. Tell the user before running it.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		Wait:        10 * time.Second,
	},
}

func findAdminTool(name string) (adminTool, bool) {
	for _, t := range adminTools {
		if t.Name == name {
			return t, true
		}
	}
	return adminTool{}, false
}

func adminToolDefs() []any {
	out := make([]any, 0, len(adminTools))
	for _, t := range adminTools {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
	}
	return out
}

func (s *Server) adminDir() string { return filepath.Join(s.cfg.StateDir, "admin") }

// runAdmin queues one action and waits for its result.
func (s *Server) runAdmin(t adminTool, args map[string]any) (string, error) {
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	dir := s.adminDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	id := randomToken(9)
	action := strings.TrimPrefix(t.Name, "server_")
	req := fmt.Sprintf("id=%s\naction=%s\n", id, action)
	if action == "logs" {
		n := 80
		if v, ok := args["lines"].(float64); ok && v > 0 {
			n = int(v)
		}
		if n > 300 {
			n = 300
		}
		req += fmt.Sprintf("lines=%d\n", n)
	}
	tmp := filepath.Join(dir, ".request.tmp")
	if err := os.WriteFile(tmp, []byte(req), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "request")); err != nil {
		return "", err
	}
	deadline := time.Now().Add(t.Wait)
	result := filepath.Join(dir, "result")
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(result); err == nil {
			text := string(b)
			if first, rest, ok := strings.Cut(text, "\n"); ok && first == "id="+id {
				if len(rest) > 24000 {
					rest = rest[len(rest)-24000:]
				}
				return rest, nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if action == "update" || action == "restart" {
		return "Started. This connector may drop for a minute; run server_status in about 2 minutes to confirm.", nil
	}
	return "", fmt.Errorf("the server didn't answer within %s (its admin helper may not be installed yet: run `sudo textbridge update` once on the server)", t.Wait)
}

// handleAdminCall answers a tools/call for an admin tool.
func (s *Server) handleAdminCall(w http.ResponseWriter, id json.RawMessage, t adminTool, args map[string]any) {
	out, err := s.runAdmin(t, args)
	isErr := err != nil
	if isErr {
		out = err.Error()
	}
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"result": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": out}},
			"isError": isErr,
		},
	})
}
