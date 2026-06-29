// Command control-plane is the Phase 1 MVP Control Plane: it serves the static
// Console, drives the per-user Workspace via the local Docker Runtime adapter,
// and proxies REST + the terminal WebSocket through to the Workspace Agent.
// dev 形態（認証バイパス・単一ユーザー）。docs/11-phase1-plan.md 参照。
package main

import (
	"context"
	"crypto/sha256"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
		rts:         map[string]cachedRT{},
		image:       envOr("WS_IMAGE", "agent-fleet/workspace:m3"),
		dataRoot:    envOr("WS_DATA", "/tmp/af-data"),
		agentHost:   envOr("WS_AGENT_HOST", "127.0.0.1"),
		memory:      envOr("WS_MEMORY", "1g"),
		sessionCmd:  os.Getenv("WS_SESSION_CMD"),   // empty => claude
		extraEnv:    splitCSV(os.Getenv("WS_ENV")), // KEY=VAL,KEY=VAL -> container -e
		portBase:    portBase,
		authMode:    envOr("AUTH", "dev"), // dev (fixed id) | proxy (gateway header)
		devUser:     envOr("DEV_USER", "dev"),
		emailHeader: envOr("AUTH_EMAIL_HEADER", "X-Forwarded-Email"),
		// P3-2: provisioning policy + deployment super-admins.
		provisionMode: envOr("AF_PROVISION", "auto"), // auto | invite
		superAdmins:   emailSet(os.Getenv("SUPER_ADMIN_EMAILS")),
	}
	// OAuth App client_id (non-secret) for the GitHub device flow — injected into
	// the Workspace so the Agent can run the flow. Reuses the extraEnv -> -e path.
	if cid := os.Getenv("GITHUB_OAUTH_CLIENT_ID"); cid != "" {
		mgr.extraEnv = append(mgr.extraEnv, "GITHUB_OAUTH_CLIENT_ID="+cid)
	}
	// Deployment master key for at-rest credential encryption (A3). Per-user
	// subkeys are derived from its SHA-256 and injected as AF_SECRET_KEY. Unset
	// => no encryption (dev: Agent stores secrets as plaintext JSON).
	if mk := os.Getenv("AF_MASTER_KEY"); mk != "" {
		sum := sha256.Sum256([]byte(mk))
		mgr.master32 = sum[:]
		// P3-3: envelope key custodian (on-prem default). Vault/KMS adapters
		// implement the same interface for true per-tenant crypto-shred.
		mgr.custodian = newLocalCustodian(mgr.master32)
	}

	// MetadataStore (P3-1, docs/13): SQLite is the source of truth for the
	// tenant/user/workspace records. Migrate, ensure the default tenant, then
	// backfill existing on-disk users so the live deployment is wrapped as the
	// default tenant without recreating containers.
	dbPath := envOr("AF_DB", filepath.Join(mgr.dataRoot, "control-plane.db"))
	store, err := openSQLite(dbPath)
	if err != nil {
		log.Fatalf("open db %s: %v", dbPath, err)
	}
	ctx := context.Background()
	if err := store.migrate(ctx); err != nil {
		log.Fatalf("db migrate: %v", err)
	}
	mgr.store = store
	if _, err := store.EnsureDefaultTenant(ctx); err != nil {
		log.Fatalf("ensure default tenant: %v", err)
	}
	if err := mgr.backfill(ctx); err != nil {
		log.Printf("backfill warning: %v", err)
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

	// Identity — who the AuthGateway resolved this request to (and the raw
	// gateway headers, for verifying the oauth2-proxy -> Caddy -> CP chain).
	mux.HandleFunc("GET /api/whoami", cfg.handleWhoami)

	// Tenants — the caller's memberships (Console picker) + minimal admin API
	// (super_admin only; full UI is P3-5). docs/14 P3-2.
	mux.HandleFunc("GET /api/tenants", cfg.handleTenants)
	mux.HandleFunc("GET /api/admin/tenants", cfg.handleAdminListTenants)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members", cfg.handleAdminListMembers)
	mux.HandleFunc("POST /api/admin/tenants", cfg.handleAdminCreateTenant)
	mux.HandleFunc("POST /api/admin/memberships", cfg.handleAdminAddMembership)
	mux.HandleFunc("POST /api/admin/stop-workspace", cfg.handleAdminStopWorkspace)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/limits", cfg.handleAdminSetTenantLimits)
	mux.HandleFunc("PUT /api/admin/user-limits", cfg.handleAdminSetUserLimit)

	// Personal Access Tokens (Console-issued) for the MCP endpoint (docs/0006).
	mux.HandleFunc("GET /api/pat", cfg.handlePATList)
	mux.HandleFunc("POST /api/pat", cfg.handlePATCreate)
	mux.HandleFunc("DELETE /api/pat/{id}", cfg.handlePATRevoke)

	// MCP endpoint (P3-6) — opt-in. Bearer PAT auth (not the gateway header), so
	// the ingress must pass /mcp through without oauth2-proxy.
	if envOr("AF_MCP_ENABLED", "") == "true" {
		mux.HandleFunc("/mcp", cfg.handleMCP)
		log.Printf("MCP endpoint enabled at /mcp")
	}

	// Workspace lifecycle (local Docker Runtime adapter).
	mux.HandleFunc("GET /api/workspace", cfg.handleWorkspaceGet)
	mux.HandleFunc("POST /api/workspace/start", cfg.handleWorkspaceStart)
	mux.HandleFunc("POST /api/workspace/stop", cfg.handleWorkspaceStop)
	mux.HandleFunc("POST /api/workspace/recreate", cfg.handleWorkspaceRecreate)

	// Session ops — proxied to the Workspace Agent.
	mux.HandleFunc("GET /api/sessions", cfg.handleSessionsList)
	mux.HandleFunc("POST /api/sessions", cfg.handleSessionCreate)
	mux.HandleFunc("POST /api/sessions/{name}/stop", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/recreate", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/archived", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/archive", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/restore", cfg.proxyAgentREST)
	// Programmatic drive I/O (docs/0006 P3-6 E) — proxied to the Agent. Also used
	// by the MCP tools, which call the Agent directly via the resolved runtime.
	mux.HandleFunc("POST /api/sessions/{name}/input", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/status", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/output", cfg.proxyAgentREST)

	// Repository ops — proxied to the Workspace Agent (/api stripped -> /repos*).
	mux.HandleFunc("GET /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/repos/{name}", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/status", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/branches", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/checkout", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/fetch", cfg.proxyAgentREST)
	// Source-control view + light edits (docs/17 P3-5) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/changes", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/diff", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/log", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/show", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/stage", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/unstage", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/discard", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/commit", cfg.proxyAgentREST)
	// File browser (docs/17 P3-5 段2) — proxied to the Agent.
	mux.HandleFunc("GET /api/fs/tree", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/file", cfg.proxyAgentREST)

	// Claude settings (Remote Control / notifications / RTK) — proxied to the Agent.
	mux.HandleFunc("GET /api/claude/settings", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/claude/settings", cfg.proxyAgentREST)

	// Toolchain selection (node / java) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/toolchains", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/env/toolchains", cfg.proxyAgentREST)

	// Per-user UI preferences (Console display settings) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/ui-prefs", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/env/ui-prefs", cfg.proxyAgentREST)

	// Connections ops — proxied to the Workspace Agent (/api stripped).
	mux.HandleFunc("GET /api/connections", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/connections/git/{host}/repos", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/connections/git/{host}/branches", cfg.proxyAgentREST)
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
	mux.HandleFunc("PUT /api/connections/opencode", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/opencode/{env}", cfg.proxyAgentREST)
	// Codex auth — proxied to the Agent (codex owns auth.json; no public callback,
	// device-auth polls OpenAI from inside the container).
	mux.HandleFunc("POST /api/connections/codex/api-key", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/codex/device/start", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/connections/codex/device/poll", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/connections/codex", cfg.proxyAgentREST)

	// Terminal PTY — proxied WebSocket.
	mux.HandleFunc("GET /ws/terminal", cfg.proxyTerminal)

	// Preview — proxy to a service the user started inside their container
	// (Spring Boot, dev server, ...) via the Agent's /proxy/{port}. The redirect
	// adds the trailing slash so the app resolves relative assets under the path.
	mux.HandleFunc("/preview/{port}", cfg.handlePreviewRedirect)
	mux.HandleFunc("/preview/{port}/{rest...}", cfg.handlePreview)

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

// emailSet parses a CSV of emails into a lowercased lookup set (SUPER_ADMIN_EMAILS).
func emailSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			m[p] = true
		}
	}
	return m
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
