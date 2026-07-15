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
	// TTS 読み上げ（docs/24 + ADR0013）: CP が直接叩く VOICEVOX エンジンの base URL。
	// dev は host 起動の CP から docker 公開の 127.0.0.1:50021 へ。AWS は ECS + Cloud Map
	// の固定 DNS を差し込む（Phase 2）。
	voicevoxURL string
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
		memMaxBytes: mustMemBytes(os.Getenv("AF_MAX_WORKSPACE_MEM")), // 0 = no extra hard ceiling

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
		// Per-identity observed cookie expiry: lets the reaper spare a workspace whose
		// owner's login has expired (they can't re-attach to keep it warm).
		authReg: newAuthRegistry(),
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
	// Full public base (scheme+host) for the in-container memo bridge (AF_CP_BASE_URL).
	mgr.publicBaseURL = strings.TrimRight(publicBaseURL, "/")
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
		// docs/24 TTS: 既定は dev の docker 公開先（host loopback）。
		voicevoxURL: envOr("AF_VOICEVOX_URL", "http://127.0.0.1:50021"),
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

	mux := buildMux(cfg)

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
