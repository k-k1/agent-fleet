// Command control-plane is the Phase 1 MVP Control Plane: it serves the static
// Console, drives the per-user Workspace via the local Docker Runtime adapter,
// and proxies REST + the terminal WebSocket through to the Workspace Agent.
// dev 形態（認証バイパス・単一ユーザー）。docs/11-phase1-plan.md 参照。
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	addr       string
	consoleDir string
	mgr        *manager
	// Bitbucket OAuth (Authorization Code Grant) — CP owns the public callback.
	bbKey         string
	bbSecret      string
	publicBaseURL string // external base, e.g. https://host/agent-fleet (for redirect_uri)
}

func main() {
	portBase, _ := strconv.Atoi(envOr("WS_AGENT_PORT", "7700"))
	mgr := &manager{
		rts:         map[string]*dockerRuntime{},
		image:       envOr("WS_IMAGE", "agent-fleet/workspace:m3"),
		dataRoot:    envOr("WS_DATA", "/tmp/af-data"),
		agentHost:   envOr("WS_AGENT_HOST", "127.0.0.1"),
		memory:      envOr("WS_MEMORY", "1g"),
		sessionCmd:  os.Getenv("WS_SESSION_CMD"), // empty => claude
		extraEnv:    splitCSV(os.Getenv("WS_ENV")), // KEY=VAL,KEY=VAL -> container -e
		portBase:    portBase,
		nextPort:    portBase,
		authMode:    envOr("AUTH", "dev"), // dev (fixed id) | proxy (gateway header)
		devUser:     envOr("DEV_USER", "dev"),
		emailHeader: envOr("AUTH_EMAIL_HEADER", "X-Forwarded-Email"),
	}
	// OAuth App client_id (non-secret) for the GitHub device flow — injected into
	// the Workspace so the Agent can run the flow. Reuses the extraEnv -> -e path.
	if cid := os.Getenv("GITHUB_OAUTH_CLIENT_ID"); cid != "" {
		mgr.extraEnv = append(mgr.extraEnv, "GITHUB_OAUTH_CLIENT_ID="+cid)
	}
	cfg := config{
		addr:          envOr("CP_ADDR", ":8080"),
		consoleDir:    envOr("CONSOLE_DIR", "./console"),
		bbKey:         os.Getenv("BITBUCKET_OAUTH_KEY"),
		bbSecret:      os.Getenv("BITBUCKET_OAUTH_SECRET"),
		publicBaseURL: os.Getenv("PUBLIC_BASE_URL"),
		mgr:           mgr,
	}

	mux := http.NewServeMux()

	// Workspace lifecycle (local Docker Runtime adapter).
	mux.HandleFunc("GET /api/workspace", cfg.handleWorkspaceGet)
	mux.HandleFunc("POST /api/workspace/start", cfg.handleWorkspaceStart)
	mux.HandleFunc("POST /api/workspace/stop", cfg.handleWorkspaceStop)

	// Session ops — proxied to the Workspace Agent.
	mux.HandleFunc("GET /api/sessions", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/stop", cfg.proxyAgentREST)

	// Repository ops — proxied to the Workspace Agent (/api stripped -> /repos*).
	mux.HandleFunc("GET /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/repos/{name}", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/status", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/branches", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/checkout", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/fetch", cfg.proxyAgentREST)

	// Connections ops — proxied to the Workspace Agent (/api stripped).
	mux.HandleFunc("GET /api/connections", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/connections/git/{host}", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/git/{host}", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/git/github/oauth/start", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/git/github/oauth/poll", cfg.proxyAgentREST)
	// Bitbucket OAuth — CP-native (owns the public callback), not proxied.
	mux.HandleFunc("GET /api/connections/git/bitbucket/oauth/start", cfg.handleBitbucketOAuthStart)
	mux.HandleFunc("GET /api/oauth/bitbucket/callback", cfg.handleBitbucketOAuthCallback)
	mux.HandleFunc("POST /api/connections/claude/start", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/claude/complete", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/claude", cfg.proxyAgentREST)

	// Terminal PTY — proxied WebSocket.
	mux.HandleFunc("GET /ws/terminal", cfg.proxyTerminal)

	// Static Console (catch-all). no-store so reloads always get fresh assets
	// during active development.
	mux.Handle("/", noStore(http.FileServer(http.Dir(cfg.consoleDir))))

	log.Printf("control-plane on %s (console=%s, ws image=%s, auth=%s)", cfg.addr, cfg.consoleDir, cfg.mgr.image, cfg.mgr.authMode)
	srv := &http.Server{Addr: cfg.addr, Handler: logRequests(mux), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// splitCSV parses "A=1,B=2" into ["A=1","B=2"], dropping blanks.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
}
