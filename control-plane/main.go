// Command control-plane is the Phase 1 MVP Control Plane: it serves the static
// Console, drives the per-user Workspace via the local Docker Runtime adapter,
// and proxies REST + the terminal WebSocket through to the Workspace Agent.
// dev 形態（認証バイパス・単一ユーザー）。docs/11-phase1-plan.md 参照。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"log"
	"net/http"
	"net/url"
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
	publicBaseURL string // external base, e.g. https://host (for redirect_uri)
	// CP-native Google OAuth (AUTH=oauth) — replaces oauth2-proxy. See oauth_google.go.
	googleClientID     string
	googleClientSecret string
	cookieSecret       []byte        // HMAC key for the signed session cookie
	cookieSecure       bool          // Secure flag (true behind https Funnel)
	sessionTTL         time.Duration // session cookie lifetime
	allowEmails        map[string]bool
	allowDomains       map[string]bool // allowed email domains (lowercased, no leading @)
	allowEmailsFile    string          // emails.txt-style allowlist, read live per callback
	autostart          bool            // P3-9: on-demand start of a stopped workspace on intentful access
	// Egress observation (docs/20 M2, log-only). egressToken authenticates the
	// forward proxy's POST /internal/egress; egressDedup collapses would-block audit
	// rows to one per (day, host). Both empty/nil unless egress is configured.
	egressToken string
	egressDedup *egressAuditDedup
}

func main() {
	// Subcommand: `control-plane egress-proxy` runs the log-only forward proxy
	// (docs/20 M2) instead of the CP server, reusing this same image/binary.
	if len(os.Args) > 1 && os.Args[1] == "egress-proxy" {
		runEgressProxy()
		return
	}

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
		authMode:    envOr("AUTH", "dev"), // dev (fixed id) | proxy (oauth2-proxy header) | oauth (CP-native Google)
		devUser:     envOr("DEV_USER", "dev"),
		emailHeader: envOr("AUTH_EMAIL_HEADER", "X-Forwarded-Email"),
		// P3-2: provisioning policy + deployment super-admins.
		provisionMode: envOr("AF_PROVISION", "auto"), // auto | invite
		superAdmins:   emailSet(os.Getenv("SUPER_ADMIN_EMAILS")),
		// P3-9: live activity tracking for idle-stop.
		conns: newConnRegistry(),
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
	// Postgres (AF_DATABASE_URL) is the RDS backend for a redeployable ECS CP whose
	// state must outlive task replacement (P3-7 段3a); SQLite is the on-prem default.
	dbPath := envOr("AF_DB", filepath.Join(mgr.dataRoot, "control-plane.db"))
	var store *sqlStore
	var err error
	if dburl := pgURLFromEnv(); dburl != "" {
		if store, err = openPostgres(dburl); err != nil {
			log.Fatalf("open postgres: %v", err)
		}
		log.Printf("metadata store: postgres")
	} else if store, err = openSQLite(dbPath); err != nil {
		log.Fatalf("open db %s: %v", dbPath, err)
	}
	ctx := context.Background()
	if err := store.migrate(ctx); err != nil {
		log.Fatalf("db migrate: %v", err)
	}
	mgr.store = store
	if dt, err := store.EnsureDefaultTenant(ctx); err != nil {
		log.Fatalf("ensure default tenant: %v", err)
	} else {
		mgr.defaultTenantID = dt.ID // used by rootedDataDir to detect flat paths
	}
	if err := mgr.backfill(ctx); err != nil {
		log.Printf("backfill warning: %v", err)
	}

	// docs/20 M2 (egress, log-only): when a forward-proxy address is configured, route
	// every workspace container's HTTP(S) egress through it by injecting proxy env.
	// DEFAULT OFF — nothing changes unless AF_EGRESS_PROXY_ADDR is set. Must run BEFORE
	// the runtime factory below, which snapshots mgr.extraEnv by value. Attribution is
	// coarse in this first cut (shared env, no per-workspace identity; docs/20 §D.3).
	if addr := os.Getenv("AF_EGRESS_PROXY_ADDR"); addr != "" {
		pu := "http://" + addr
		mgr.extraEnv = append(mgr.extraEnv,
			"http_proxy="+pu, "https_proxy="+pu, "HTTP_PROXY="+pu, "HTTPS_PROXY="+pu,
			"no_proxy=localhost,127.0.0.1,::1", "NO_PROXY=localhost,127.0.0.1,::1")
		log.Printf("egress: routing workspace egress through proxy %s (log-only)", addr)
	}

	// Runtime port adapter (docs/09, P3-7): pick the deployment profile. Default
	// "local" (Docker Engine / compose, the on-prem target); "ecs" selects the AWS
	// adapter. Built here — after extraEnv/store/defaultTenantID are finalized —
	// because the docker factory captures those template fields by value.
	rtProfile := envOr("AF_RUNTIME", "local")
	rtFactory, err := newRuntimeFactory(rtProfile, mgr)
	if err != nil {
		log.Fatalf("runtime factory: %v", err)
	}
	mgr.rtFactory = rtFactory

	publicBaseURL := os.Getenv("PUBLIC_BASE_URL")
	// Internal git provider: the clone host workspaces authenticate against is the
	// public base's host (Caddy TLS terminus). Recorded on the manager so each
	// workspace start injects a token for it (docs/reference/internal-git-provider).
	if u, err := url.Parse(publicBaseURL); err == nil {
		mgr.internalGitHost = u.Hostname()
	}
	cfg := config{
		addr:          envOr("CP_ADDR", ":8080"),
		consoleDir:    envOr("CONSOLE_DIR", "./console"),
		bbKey:         os.Getenv("BITBUCKET_OAUTH_KEY"),
		bbSecret:      os.Getenv("BITBUCKET_OAUTH_SECRET"),
		publicBaseURL: publicBaseURL,
		mgr:           mgr,
		// CP-native Google OAuth (AUTH=oauth).
		googleClientID:     os.Getenv("GOOGLE_OAUTH_CLIENT_ID"),
		googleClientSecret: os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
		cookieSecret:       decodeKey(os.Getenv("AF_COOKIE_SECRET")),
		cookieSecure:       strings.HasPrefix(publicBaseURL, "https"),
		sessionTTL:         parseDurationOr(os.Getenv("AF_SESSION_TTL"), 168*time.Hour),
		allowEmails:        emailSet(os.Getenv("AF_OAUTH_ALLOWED_EMAILS")),
		allowDomains:       domainSet(os.Getenv("AF_OAUTH_ALLOWED_DOMAINS")),
		allowEmailsFile:    os.Getenv("AF_OAUTH_ALLOWED_EMAILS_FILE"),
		// P3-9 auto-start (scale-to-zero counterpart): default on; AF_AUTOSTART=0
		// disables it so a stopped workspace only starts on an explicit start click.
		autostart: envBool("AF_AUTOSTART", true),
		// docs/20 M2 egress ingestion auth + audit dedup (empty token => endpoint 401s).
		egressToken: os.Getenv("AF_EGRESS_TOKEN"),
		egressDedup: &egressAuditDedup{},
	}

	// P3-9 idle-stop (docs/19): a background reaper halts idle claude sessions
	// (tier 1) and stops cold workspaces (tier 2). Deployment defaults are
	// DISABLED (0) — safe by default, like the P3-4 quotas; an operator opts in
	// per-tenant via limits (no restart needed) or deployment-wide via env.
	// AF_IDLE_SWEEP_INTERVAL=0 disables the reaper entirely.
	if iv := parseDurationOr(os.Getenv("AF_IDLE_SWEEP_INTERVAL"), time.Minute); iv > 0 {
		sessDef := parseDurationOr(os.Getenv("AF_SESSION_IDLE_TIMEOUT"), 0)
		wsDef := parseDurationOr(os.Getenv("AF_WS_IDLE_TIMEOUT"), 0)
		go newReaper(mgr, iv, sessDef, wsDef).run(context.Background())
	}

	// Showback usage sampler (P3-9): credits running-seconds per workspace so the
	// admin usage view can attribute infra occupancy per tenant/member. Non-
	// destructive (DB writes only), so it is on by default; AF_USAGE_SAMPLE_INTERVAL=0
	// disables it.
	if iv := parseDurationOr(os.Getenv("AF_USAGE_SAMPLE_INTERVAL"), 5*time.Minute); iv > 0 {
		go newUsageSampler(mgr, iv).run(context.Background())
	}

	// docs/20 M5 (claude self-op audit, A-第2段): a sweeper that pulls each running
	// claude session's transcript and records Write/Edit/Bash into the audit ledger
	// (actor_kind=claude). OFF by default (AF_CLAUDE_AUDIT_INTERVAL=0) — it polls every
	// session, so it's opt-in; enable per deployment once validated.
	if iv := parseDurationOr(os.Getenv("AF_CLAUDE_AUDIT_INTERVAL"), 0); iv > 0 {
		go newClaudeAuditor(mgr, iv).run(context.Background())
		log.Printf("claude-audit: sweeping transcripts every %s", iv)
	}

	// Internal git maintenance (P2/P3): repack bare repos (`git gc --auto`) and prune
	// orphaned LFS objects, sequential — cheap on the shared host. Default 24h;
	// AF_GIT_GC_INTERVAL=0 disables it. AF_LFS_GC_GRACE (default 14d) protects
	// recently-uploaded LFS objects from pruning so GC never races an in-flight push.
	if iv := parseDurationOr(os.Getenv("AF_GIT_GC_INTERVAL"), 24*time.Hour); iv > 0 {
		grace := parseDurationOr(os.Getenv("AF_LFS_GC_GRACE"), 14*24*time.Hour)
		go newGitGC(mgr.store, mgr.dataRoot, iv, grace).run(context.Background())
	}

	mux := http.NewServeMux()

	// Health + CP-native Google OAuth (AUTH=oauth). The login page, OAuth
	// endpoints and health check are reachable without a session (authGate
	// exempts them); see oauth_google.go.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /login", cfg.handleLogin)
	mux.HandleFunc("GET /oauth2/login", cfg.handleOAuthLogin)
	mux.HandleFunc("GET /oauth2/callback", cfg.handleOAuthCallback)
	mux.HandleFunc("GET /oauth2/logout", cfg.handleOAuthLogout)

	// Identity — who the AuthGateway resolved this request to (and the raw
	// gateway headers, for verifying the oauth2-proxy -> Caddy -> CP chain).
	mux.HandleFunc("GET /api/whoami", cfg.handleWhoami)

	// Tenants — the caller's memberships (Console picker) + minimal admin API
	// (super_admin only; full UI is P3-5). docs/14 P3-2.
	mux.HandleFunc("GET /api/tenants", cfg.handleTenants)
	mux.HandleFunc("GET /api/admin/tenants", cfg.handleAdminListTenants)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members", cfg.handleAdminListMembers)
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/stats", cfg.handleAdminMemberStats)       // per-member mem/CPU/disk
	mux.HandleFunc("GET /api/admin/tenants/{slug}/members/{key}/sessions", cfg.handleAdminMemberSessions) // per-member session list (read-only)
	mux.HandleFunc("POST /api/admin/tenants", cfg.handleAdminCreateTenant)
	mux.HandleFunc("POST /api/admin/memberships", cfg.handleAdminAddMembership)
	mux.HandleFunc("POST /api/admin/stop-workspace", cfg.handleAdminStopWorkspace)
	mux.HandleFunc("POST /api/admin/clean-home", cfg.handleAdminCleanHome) // wipe home (keep auth/connections)
	mux.HandleFunc("PUT /api/admin/tenants/{slug}/limits", cfg.handleAdminSetTenantLimits)
	mux.HandleFunc("PUT /api/admin/user-limits", cfg.handleAdminSetUserLimit)
	mux.HandleFunc("PUT /api/admin/membership-role", cfg.handleAdminSetMembershipRole)           // grant/revoke tenant_admin (super_admin only)
	mux.HandleFunc("GET /api/admin/host", cfg.handleHostStats)                                   // host load / memory (super_admin)
	mux.HandleFunc("GET /api/admin/usage", cfg.handleAdminUsage)                                 // showback: occupancy per tenant/member (json|csv)
	mux.HandleFunc("GET /api/admin/sessions", cfg.handleAdminAllSessions)                        // deployment-wide session overview (super_admin / tenant_admin)
	mux.HandleFunc("GET /api/admin/audit", cfg.handleAdminAudit)                                 // audit log ledger (super_admin / tenant_admin, docs/20 M1)
	mux.HandleFunc("GET /api/admin/egress", cfg.handleAdminEgress)                               // egress observation stats (super_admin, docs/20 M2)
	mux.HandleFunc("POST /internal/egress", cfg.handleEgressIngest)                              // egress proxy -> CP ingestion (AF_EGRESS_TOKEN, docs/20 M2)
	mux.HandleFunc("GET /internal/egress/policy", cfg.handleEgressPolicy)                        // effective allowlist+mode -> proxy (docs/20 M3)
	mux.HandleFunc("GET /api/admin/egress/allowlist", cfg.handleAdminAllowlistList)              // allowlist entries (super_admin, docs/20 M3)
	mux.HandleFunc("POST /api/admin/egress/allowlist", cfg.handleAdminAllowlistAdd)              // add allowlist entry (super_admin)
	mux.HandleFunc("POST /api/admin/egress/allowlist/{id}/state", cfg.handleAdminAllowlistState) // approve/retire (super_admin)
	mux.HandleFunc("GET /api/admin/egress/mode", cfg.handleAdminEgressMode)                      // read egress mode (super_admin)
	mux.HandleFunc("PUT /api/admin/egress/mode", cfg.handleAdminEgressMode)                      // set log-only/enforce (super_admin)

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
	// Own-workspace resource chip (mem / CPU vs quota) — host-read cgroup, all users.
	mux.HandleFunc("GET /api/workspace/stats", cfg.handleWorkspaceStats)

	// Session ops — proxied to the Workspace Agent.
	mux.HandleFunc("GET /api/sessions", cfg.handleSessionsList)
	mux.HandleFunc("POST /api/sessions", cfg.handleSessionCreate)
	mux.HandleFunc("POST /api/sessions/{name}/fork", cfg.handleSessionFork)
	mux.HandleFunc("POST /api/sessions/{name}/stop", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/halt", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/recreate", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/archived", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/archive", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/restore", cfg.proxyAgentREST)
	// Programmatic drive I/O (docs/0006 P3-6 E) — proxied to the Agent. Also used
	// by the MCP tools, which call the Agent directly via the resolved runtime.
	mux.HandleFunc("POST /api/sessions/{name}/input", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/paste-image", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/pasted/{file}", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/status", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/sessions/{name}/output", cfg.proxyAgentREST)
	// SSM login status polled by the New Session modal (docs/history/p3-ssm-session.md)
	// — surfaces the device-auth URL and the "ready" transition without attaching yet.
	mux.HandleFunc("GET /api/sessions/{name}/ssm-login", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/start", cfg.handleSessionStart)
	// Structured transcript for the Console chat view (case-A).
	mux.HandleFunc("GET /api/sessions/{name}/messages", cfg.proxyAgentREST)
	// Auto session-title suggestion accept/dismiss (session_title.go, Agent-side).
	mux.HandleFunc("POST /api/sessions/{name}/title/accept", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/dismiss", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/regenerate", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/suggest", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/title/set", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/sessions/{name}/suggest-branch", cfg.proxyAgentREST) // LLM branch-name suggestion (this session's convo)
	mux.HandleFunc("POST /api/sessions/{name}/rename-branch", cfg.proxyAgentREST)  // worktree deferred-naming: git branch -m

	// Assistant chat (docs/19) — headless-CLI LLM chat/translation, proxied to the
	// Agent verbatim (kind-agnostic; non-streaming, so the plain REST proxy suffices).
	mux.HandleFunc("GET /api/chat/conversations", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/chat/conversations", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/chat/conversations/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("PATCH /api/chat/conversations/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/chat/conversations/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/chat/conversations/{id}/messages", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/chat/conversations/{id}/stream", cfg.proxyAgentStream) // SSE (Phase B)
	mux.HandleFunc("POST /api/chat/conversations/{id}/paste-image", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/chat/conversations/{id}/pasted/{file}", cfg.proxyAgentREST)
	// One-shot advisory turn (docs/21 メモ整理) — stateless, tools off. Proxied verbatim.
	mux.HandleFunc("POST /api/chat/ask", cfg.proxyAgentREST)

	// Assistant templates (docs/19 Q2) — configurable chat personas, proxied verbatim.
	mux.HandleFunc("GET /api/assistants", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/assistants", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/assistants/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/assistants/{id}", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/assistants/{id}", cfg.proxyAgentREST)

	// SSM login config (docs/history/p3-ssm-session.md) — per-member profiles (common
	// auth bundle) + host bookmarks (per-instance). No AWS secrets; the aws CLI in the
	// workspace authenticates via SSO.
	mux.HandleFunc("GET /api/ssm/profiles", cfg.handleSSMProfilesList)
	mux.HandleFunc("POST /api/ssm/profiles", cfg.handleSSMProfileCreate)
	mux.HandleFunc("PUT /api/ssm/profiles/{id}", cfg.handleSSMProfileUpdate)
	mux.HandleFunc("DELETE /api/ssm/profiles/{id}", cfg.handleSSMProfileDelete)
	mux.HandleFunc("GET /api/ssm/hosts", cfg.handleSSMHostsList)
	mux.HandleFunc("POST /api/ssm/hosts", cfg.handleSSMHostCreate)
	mux.HandleFunc("PUT /api/ssm/hosts/{id}", cfg.handleSSMHostUpdate)
	mux.HandleFunc("DELETE /api/ssm/hosts/{id}", cfg.handleSSMHostDelete)

	// Memo queue (docs/21) — per-member notes accumulated across devices, then flushed
	// to a session as one message. Scoped by membership (no workspace build for CRUD).
	mux.HandleFunc("GET /api/memos", cfg.handleMemosList)
	mux.HandleFunc("POST /api/memos", cfg.handleMemoCreate)
	mux.HandleFunc("POST /api/memos/flush", cfg.handleMemoFlush)
	mux.HandleFunc("PATCH /api/memos/{id}", cfg.handleMemoUpdate)
	mux.HandleFunc("DELETE /api/memos/{id}", cfg.handleMemoDelete)

	// Repository ops — proxied to the Workspace Agent (/api stripped -> /repos*).
	mux.HandleFunc("GET /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/repos/{name}", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/status", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/branches", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/checkout", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/fetch", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/ff", cfg.proxyAgentREST)
	// Launch prompt templates (repo 起動 modal) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/prompt-templates", cfg.proxyAgentREST)
	// Source-control view + light edits (docs/17 P3-5) — proxied to the Agent.
	mux.HandleFunc("GET /api/repos/{name}/changes", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/diff", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/log", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/graph", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/show", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/stage", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/unstage", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/discard", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/repos/{name}/commit", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/repos/{name}/identity", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/repos/{name}/identity", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/git/identity", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/git/identity", cfg.proxyAgentREST)
	// File browser (docs/17 P3-5 段2) — proxied to the Agent.
	mux.HandleFunc("GET /api/fs/tree", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/file", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/download", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/upload", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/changes", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/fs/linemarks", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/mkdir", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/newfile", cfg.proxyAgentREST)
	mux.HandleFunc("POST /api/fs/rename", cfg.proxyAgentREST)
	mux.HandleFunc("DELETE /api/fs/delete", cfg.proxyAgentREST)

	// Claude settings (Remote Control / notifications / RTK) — proxied to the Agent.
	mux.HandleFunc("GET /api/claude/settings", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/claude/settings", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/claude/usage", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/codex/usage", cfg.proxyAgentREST)
	// codex / opencode rtk toggle — proxied to the Agent.
	mux.HandleFunc("GET /api/agents/rtk", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/agents/rtk", cfg.proxyAgentREST)

	// Toolchain selection (node / java) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/toolchains", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/env/toolchains", cfg.proxyAgentREST)
	// CP-owned per-workspace settings (editable while stopped; applied at start).
	mux.HandleFunc("GET /api/env/ws-settings", cfg.handleWSSettingsGet)
	mux.HandleFunc("PUT /api/env/ws-settings", cfg.handleWSSettingsPut)

	// Per-user UI preferences (Console display settings) — proxied to the Agent.
	mux.HandleFunc("GET /api/env/ui-prefs", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/env/ui-prefs", cfg.proxyAgentREST)

	// Connections ops — proxied to the Workspace Agent (/api stripped).
	mux.HandleFunc("GET /api/connections", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/connections/git/{host}/repos", cfg.proxyAgentREST)
	mux.HandleFunc("GET /api/connections/git/{host}/branches", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/connections/git/{host}", cfg.proxyAgentREST)
	mux.HandleFunc("PUT /api/connections/git/{host}/identity", cfg.proxyAgentREST)
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

	// Internal git provider (docs/reference/internal-git-provider, ADR 0010).
	// Repo management is CP-native (the CP owns the bare repos), so these are NOT
	// proxied to the Agent like other providers.
	mux.HandleFunc("GET /api/internal-git/repos", cfg.handleInternalGitReposList)
	mux.HandleFunc("POST /api/internal-git/repos", cfg.handleInternalGitRepoCreate)
	mux.HandleFunc("DELETE /api/internal-git/repos/{name}", cfg.handleInternalGitRepoDelete)
	mux.HandleFunc("POST /api/internal-git/repos/{name}/rename", cfg.handleInternalGitRepoRename)
	mux.HandleFunc("GET /api/internal-git/repos/{name}/branches", cfg.handleInternalGitBranches)
	// Read-only browsing (clone-free): tree / blob / commits, served from the bare.
	mux.HandleFunc("GET /api/internal-git/repos/{name}/tree", cfg.handleInternalGitTree)
	mux.HandleFunc("GET /api/internal-git/repos/{name}/blob", cfg.handleInternalGitBlob)
	mux.HandleFunc("GET /api/internal-git/repos/{name}/commits", cfg.handleInternalGitCommits)
	// Git LFS face (docs/reference/internal-git-provider, P3). More specific than the
	// smart-HTTP catch-all below, so these win for LFS paths; git-http-backend never
	// sees them. Same Basic git-token auth (session-exempt under /git/).
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/objects/batch", cfg.handleLFSBatch)
	mux.HandleFunc("PUT /git/{slug}/{repo}/info/lfs/objects/{oid}", cfg.handleLFSUpload)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/objects/{oid}", cfg.handleLFSDownload)
	// LFS file locking API (create / list / verify / unlock).
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks", cfg.handleLFSLockCreate)
	mux.HandleFunc("GET /git/{slug}/{repo}/info/lfs/locks", cfg.handleLFSLocksList)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/verify", cfg.handleLFSLocksVerify)
	mux.HandleFunc("POST /git/{slug}/{repo}/info/lfs/locks/{id}/unlock", cfg.handleLFSUnlock)

	// Smart-HTTP git face (clone/fetch/push). Self-authenticating via a Basic git
	// token (session-exempt, like /mcp); handles every method.
	mux.HandleFunc("/git/{slug}/{repo...}", cfg.handleGitHTTP)

	// Terminal PTY — proxied WebSocket.
	mux.HandleFunc("GET /ws/terminal", cfg.proxyTerminal)

	// Preview — proxy to a service the user started inside their container
	// (Spring Boot, dev server, ...) via the Agent's /proxy/{port}. The redirect
	// adds the trailing slash so the app resolves relative assets under the path.
	mux.HandleFunc("/preview/{port}", cfg.handlePreviewRedirect)
	mux.HandleFunc("/preview/{port}/{rest...}", cfg.handlePreview)

	// Legacy path compatibility: the deployment used to be served under
	// /agent-fleet (oauth2-proxy + Caddy stripped it). Now it's at the root, so
	// old bookmarks — and any stale post-login next=/agent-fleet/… — would 404.
	// Redirect /agent-fleet[/…] -> /… (auth-exempt, so it fires before login and
	// the dead prefix never reaches next=).
	legacyRedirect := func(w http.ResponseWriter, r *http.Request) {
		dest := strings.TrimPrefix(r.URL.Path, "/agent-fleet")
		if !strings.HasPrefix(dest, "/") {
			dest = "/" + dest
		}
		if r.URL.RawQuery != "" {
			dest += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, dest, http.StatusFound)
	}
	mux.HandleFunc("/agent-fleet", legacyRedirect)
	mux.HandleFunc("/agent-fleet/", legacyRedirect)

	// Static Console (catch-all). no-store so reloads always get fresh assets
	// during active development.
	mux.Handle("/", noStore(http.FileServer(http.Dir(cfg.consoleDir))))

	// In oauth mode the CP is the edge (behind Funnel): gate every request on a
	// verified Google session. dev/proxy modes keep the prior behavior (no gate;
	// proxy trusts the upstream oauth2-proxy header).
	var handler http.Handler = mux
	if cfg.mgr.authMode == "oauth" {
		if !cfg.oauthConfigured() {
			log.Fatalf("AUTH=oauth requires GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, AF_COOKIE_SECRET, PUBLIC_BASE_URL")
		}
		if len(cfg.allowEmails) == 0 && len(cfg.allowDomains) == 0 && cfg.allowEmailsFile == "" {
			log.Printf("WARNING: AUTH=oauth with no allowlist (AF_OAUTH_ALLOWED_EMAILS / AF_OAUTH_ALLOWED_DOMAINS / AF_OAUTH_ALLOWED_EMAILS_FILE) — every login is denied")
		}
		handler = cfg.authGate(mux)
	}

	log.Printf("control-plane on %s (console=%s, ws image=%s, auth=%s, runtime=%s)", cfg.addr, cfg.consoleDir, cfg.mgr.image, cfg.mgr.authMode, rtProfile)
	srv := &http.Server{Addr: cfg.addr, Handler: logRequests(handler), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// envBool parses a boolean env var (1/true/yes/on = true, 0/false/no/off = false);
// blank or unrecognized falls back to def.
func envBool(k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// decodeKey reads a cookie/HMAC key: base64 (std or url, padded or raw) if it
// decodes, else the raw bytes. Empty => nil (oauth mode then fails the config
// check rather than running with a zero-length key).
func decodeKey(s string) []byte {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) >= 16 {
			return b
		}
	}
	return []byte(s)
}

func parseDurationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
		return d
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

// domainSet parses a CSV of email domains into a lowercased lookup set, tolerating
// a leading "@" on each entry (AF_OAUTH_ALLOWED_DOMAINS).
func domainSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(p)), "@")
		if p != "" {
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
