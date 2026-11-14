// textbridge-gateway puts a passphrase-protected OAuth login in front of
// OpenMessage's MCP endpoint.
//
//	textbridge-gateway serve          run the gateway
//	textbridge-gateway new-password   make a new passphrase (prints it once)
//	textbridge-gateway sign-out-all   revoke every Claude session
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaibrownco/textbridge/internal/gateway"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	stateDir := env("TB_STATE_DIR", "/var/lib/textbridge/gateway")
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "new-password":
		pw := gateway.GeneratePassphrase()
		hash, err := gateway.HashPassword(pw)
		if err != nil {
			log.Fatal(err)
		}
		if err := gateway.SavePasswordHash(stateDir, hash); err != nil {
			log.Fatal(err)
		}
		fmt.Println(pw)
	case "sign-out-all":
		st, err := gateway.OpenStore(stateDir)
		if err != nil {
			log.Fatal(err)
		}
		if err := st.RevokeAll(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("All Claude sessions signed out. Restart the gateway: sudo systemctl restart textbridge-gateway")
	case "serve":
		serve(stateDir)
	default:
		fmt.Fprintf(os.Stderr, "usage: %s [serve|new-password|sign-out-all]\n", os.Args[0])
		os.Exit(2)
	}
}

func serve(stateDir string) {
	public := env("TB_PUBLIC_URL", "")
	if !strings.HasPrefix(public, "https://") && !strings.HasPrefix(public, "http://127.0.0.1") && !strings.HasPrefix(public, "http://localhost") {
		log.Fatal("TB_PUBLIC_URL must be the https:// address of this server")
	}
	hash, err := gateway.LoadPasswordHash(stateDir)
	if err != nil || hash == "" {
		log.Fatal("no passphrase set; run: textbridge-gateway new-password")
	}
	st, err := gateway.OpenStore(stateDir)
	if err != nil {
		log.Fatal(err)
	}
	hosts := strings.Split(env("TB_REDIRECT_HOSTS", "claude.ai,claude.com"), ",")
	for i := range hosts {
		hosts[i] = strings.TrimSpace(hosts[i])
	}
	instructions := ""
	if f := env("TB_INSTRUCTIONS_FILE", ""); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("read TB_INSTRUCTIONS_FILE: %v", err)
		}
		instructions = string(b)
	}
	var tools []string
	if t := env("TB_TOOLS", ""); t != "" {
		tools = strings.Split(t, ",")
	}
	srv, err := gateway.New(gateway.Config{
		PublicURL:     public,
		Upstream:      env("TB_UPSTREAM", "http://127.0.0.1:7007"),
		StateDir:      stateDir,
		RedirectHosts: hosts,
		Instructions:  instructions,
		Tools:         tools,
	}, st, hash)
	if err != nil {
		log.Fatal(err)
	}
	listen := env("TB_LISTEN", "127.0.0.1:8080")
	log.Printf("textbridge gateway on %s for %s", listen, public)
	hs := &http.Server{
		Addr:              listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(hs.ListenAndServe())
}
