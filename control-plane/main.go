// Command control-plane is the Phase 1 MVP Control Plane: it serves the static
// Console, drives the per-user Workspace via the local Docker Runtime adapter,
// and proxies REST + the terminal WebSocket through to the Workspace Agent.
// dev 形態（認証バイパス・単一ユーザー）。docs/11-phase1-plan.md 参照。
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// buildVersion is stamped by the release pipeline via
// `-ldflags "-X main.buildVersion=<v>"` (docs/35 §35.6.1); dev builds stay "dev".
// Surfaced in the startup log and GET /api/version (authenticated).
var buildVersion = "dev"

type config struct {
	addr       string
	consoleDir string
	mgr        *manager
	// ★ The git providers' OAuth apps are NOT here. Since docs/71 they are per-tenant
	// rows read from the database at the moment a member presses "connect"
	// (tenant_git_oauth.go); BITBUCKET_OAUTH_KEY/SECRET and the workspace's
	// GITHUB_OAUTH_CLIENT_ID are not read at all, not even as a fallback.
	publicBaseURL string // external base, e.g. https://host (for redirect_uri)
	// CP-native OIDC login (AUTH=oauth) — replaces oauth2-proxy. See oauth.go /
	// oauth_oidc.go. Google keeps its historical env names and is one instance of
	// the generic OIDC client (docs/61 §61.6).
	googleClientID     string
	googleClientSecret string
	cookieSecret       []byte        // HMAC key for the signed session cookie
	cookieSecure       bool          // Secure flag (true behind https Funnel)
	sessionTTL         time.Duration // session cookie lifetime
	allowEmails        map[string]bool
	allowDomains       map[string]bool // allowed email domains (lowercased, no leading @)
	allowEmailsFile    string          // emails.txt-style allowlist, read live per callback
	// Enabled login providers, in login-page display order, plus the id lookup the
	// callback resolves state against. Built by buildLoginProviders/setProviders.
	providers    []loginProvider
	providerByID map[string]loginProvider
	autostart    bool // P3-9: on-demand start of a stopped workspace on intentful access
	// Egress observation (docs/20 M2, log-only). egressToken authenticates the
	// forward proxy's POST /internal/egress; egressDedup collapses would-block audit
	// rows to one per (day, host). Both empty/nil unless egress is configured.
	egressToken string
	egressDedup *egressAuditDedup
	// egressProxyAddr mirrors AF_EGRESS_PROXY_ADDR: set = workspace containers are
	// actually routed through the forward proxy. The member-facing check reports it as
	// `configured` so the Console only warns about unreachable MCP hosts on deployments
	// where egress really is constrained (docs/48 §9).
	egressProxyAddr string
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
	// Subcommand: `control-plane drawio-preseed` fills the stencil cache ahead of
	// time (docs/65 §65.5.5). It runs where the cache lives and reuses the embedded
	// manifest, so it cannot drift from what the server will verify against.
	if len(os.Args) > 1 && os.Args[1] == "drawio-preseed" {
		runDrawioPreseed(os.Args[2:])
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
	}
	// ★ GITHUB_OAUTH_CLIENT_ID is deliberately NOT injected into the workspace any more
	// (docs/71 §71.5). The GitHub device flow moved into the CP, where the app can be
	// read per tenant from the database; container env is fixed at container start and
	// is implemented once per runtime, so a per-tenant value delivered that way would
	// have needed all four runtimes plumbed and a workspace restart to take effect. The
	// env var still exists for GitHub SIGN-IN (oauth_github.go) — a different feature.
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
	} else {
		// 初回起動でも素で立ち上がるよう DB の親ディレクトリは自作する（docs/35 P1
		// ゲートで露見: WS_DATA 未作成の素のコンテナ/native 初回起動だと sqlite が
		// ファイルを作れず即死していた。実デプロイはマウント済み dir で不発だった）。
		if mkerr := os.MkdirAll(filepath.Dir(dbPath), 0o755); mkerr != nil {
			log.Printf("WARN: create db dir %s: %v", filepath.Dir(dbPath), mkerr)
		}
		if store, err = openSQLite(dbPath); err != nil {
			log.Fatalf("open db %s: %v", dbPath, err)
		}
	}
	ctx := context.Background()
	if err := store.migrate(ctx); err != nil {
		log.Fatalf("db migrate: %v", err)
	}
	mgr.store = store
	mgr.tenantLogin = newTenantLoginCache(store)
	// Tenant-defined login providers (docs/61 §61.11). Unlike the env providers built
	// below, this set is read from the database on demand, so approving a subsidiary's
	// IdP needs no restart (決定 29).
	mgr.tenantIdP = newTenantIdPRegistry(store, mgr.openTenantSecret)
	if dt, err := store.EnsureDefaultTenant(ctx); err != nil {
		log.Fatalf("ensure default tenant: %v", err)
	} else {
		mgr.defaultTenantID = dt.ID // used by rootedDataDir to detect flat paths
	}
	// ★ SUPER_ADMIN_EMAILS is the single source of truth for the deployment role,
	// and this is where removing somebody from it takes effect (docs/61 §61.10.7 +
	// ADR0043 決定 24). UpsertIdentity only ever upgrades, on purpose — it is called
	// with roleHint="" from addMembership / cleanHome / stopWorkspace, so demoting
	// there would strip an operator the moment anyone added a member. And a
	// login-time sync would never reach the case that matters: the person who left
	// does not log in again. A startup pass lines up exactly with the documented
	// handover, which is "edit the env, restart CP".
	if demoted, err := store.DemoteSuperAdmins(ctx, superAdminEmailList()); err != nil {
		log.Printf("WARNING: super_admin sync: %v", err)
	} else if len(demoted) > 0 {
		log.Printf("super_admin revoked (not in SUPER_ADMIN_EMAILS): %s", strings.Join(demoted, ", "))
	}
	if err := mgr.backfill(ctx); err != nil {
		log.Printf("backfill warning: %v", err)
	}

	// docs/20 M2 (egress, log-only): when a forward-proxy address is configured, route
	// every workspace container's HTTP(S) egress through it by injecting proxy env.
	// DEFAULT OFF — nothing changes unless AF_EGRESS_PROXY_ADDR is set. Must run BEFORE
	// the runtime factory below, which snapshots mgr.extraEnv by value. Attribution is
	// coarse in this first cut (shared env, no per-workspace identity; docs/20 §D.3).
	egressProxyAddr := os.Getenv("AF_EGRESS_PROXY_ADDR")
	if addr := egressProxyAddr; addr != "" {
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
	mgr.nativeRuntime = rtProfile == "native" || rtProfile == "wsl"

	publicBaseURL := os.Getenv("PUBLIC_BASE_URL")
	// Internal git provider: the clone host workspaces authenticate against is the
	// public base's host (Caddy TLS terminus). Recorded on the manager so each
	// workspace start injects a token for it (docs/reference/internal-git-provider).
	if u, err := url.Parse(publicBaseURL); err == nil {
		mgr.internalGitHost = u.Hostname()
		wsAllowedOriginHost = u.Host // WS origin allowlist (checkWSOrigin)
	}
	// Full public base (scheme+host) for the in-container memo bridge (AF_CP_BASE_URL).
	mgr.publicBaseURL = strings.TrimRight(publicBaseURL, "/")
	cfg := config{
		addr:          envOr("CP_ADDR", ":8080"),
		consoleDir:    envOr("CONSOLE_DIR", "./console"),
		publicBaseURL: publicBaseURL,
		mgr:           mgr,
		// CP-native OIDC login (AUTH=oauth). Google's env names are unchanged.
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
		// Whether containers are actually routed through the proxy (docs/48 §9).
		egressProxyAddr: egressProxyAddr,
		// docs/24 TTS: 既定は dev の docker 公開先（host loopback）。
		voicevoxURL: envOr("AF_VOICEVOX_URL", "http://127.0.0.1:50021"),
	}

	// P3-9 idle-stop (docs/19): a background reaper halts idle claude sessions
	// (tier 1) and stops cold workspaces (tier 2).
	//
	// The deployment defaults are ON — 1h for a session, 2h for the workspace. They used
	// to be 0 (off), which read as "safe by default" and was not: a workspace nobody had
	// touched since the night before was still running the next morning, and on the EC2
	// pool that also pins an m7i.large, because a slot only sleeps once its task is gone
	// (measured on a real deployment, 9.4h — docs/64 §64.26). Nothing is lost when they
	// fire: a halted claude session is resumable, and a stopped workspace restarts on the
	// next visit.
	//
	// A tenant admin overrides either one in the tenant's limits, INCLUDING "0" to turn
	// it off for that tenant (idleTimeout), and a deployment sets its own default in env.
	// AF_IDLE_SWEEP_INTERVAL=0 disables the reaper entirely — see intervalOff, which is
	// what makes that true (measured: it was not).
	if iv := intervalOff(os.Getenv("AF_IDLE_SWEEP_INTERVAL"), time.Minute); iv > 0 {
		// intervalOff, not parseDurationOr: now that the default is non-zero, "0" has to
		// mean OFF for the whole deployment. parseDurationOr treats any non-positive value
		// as "use the default", which would have silently turned an operator's explicit
		// off switch into 1h/2h — the same trap AF_IDLE_SWEEP_INTERVAL was in.
		sessDef := intervalOff(os.Getenv("AF_SESSION_IDLE_TIMEOUT"), time.Hour)
		wsDef := intervalOff(os.Getenv("AF_WS_IDLE_TIMEOUT"), 2*time.Hour)
		// Tier 3 (ecs-ec2 only): the deployment default for home hibernation. Kept in the
		// AF_ECS_EC2_* namespace and in seconds because that is where it started life, as
		// a setting of the pool sweeper; the trigger moved up here so a tenant can override
		// it (ADR 0045 決定 13-2). Still 0 = off by default.
		hibDef := time.Duration(envInt("AF_ECS_EC2_HIBERNATE_AFTER_SEC", 0)) * time.Second
		// Tier 4 (ecs-ec2 only): the deployment default for how often a home is copied
		// somewhere its AZ is not. Also 0 = off — a backup is cheap but not free, and a
		// deployment that has not thought about retention should not be paying for it.
		backupDef := time.Duration(envInt("AF_ECS_EC2_BACKUP_EVERY_SEC", 0)) * time.Second
		go newReaper(mgr, iv, sessDef, wsDef, hibDef, backupDef).run(context.Background())
	}

	// Golden snapshot auto-bake (ecs-ec2 only — ADR 0045 決定 9 / docs/64 §64.28).
	// The CP already refuses a golden stamped with another image; this is the CP acting
	// on what it knows instead of logging it and waiting for somebody to run
	// bake-golden.sh. Default ON: the failure it removes is a release nobody re-baked
	// for, and a feature that has to be switched on is a feature that stays off.
	//
	// It is deliberately NOT inside the reaper's `if` above: switching off idle-stop
	// must not also switch this off. goldenBakerFor returns nil on every profile that
	// does not seed homes from a shared snapshot, so no other deployment pays anything.
	if b := goldenBakerFor(mgr, envBool("AF_ECS_EC2_GOLDEN_AUTOBAKE", true)); b != nil {
		// Recorded, not re-read later: the pool screen has to say "switched off" rather
		// than leave "there is no golden and nothing is happening" unexplained (§64.30).
		mgr.autoBakeGolden = true
		go b.run(context.Background(), time.Duration(envInt("AF_ECS_EC2_GOLDEN_BAKE_SEC", 60))*time.Second)
	}

	// Showback usage sampler (P3-9): credits running-seconds per workspace so the
	// admin usage view can attribute infra occupancy per tenant/member. Non-
	// destructive (DB writes only), so it is on by default; AF_USAGE_SAMPLE_INTERVAL=0
	// disables it.
	if iv := parseDurationOr(os.Getenv("AF_USAGE_SAMPLE_INTERVAL"), 5*time.Minute); iv > 0 {
		go newUsageSampler(mgr, iv).run(context.Background())
	}

	// Cloud cost (docs/67 + ADR 0048): the AWS invoice, attributed per member by cost
	// allocation tag. A different claim from the sampler above — that one counts
	// seconds on every runtime, this one reads real money and only where there is a
	// bill. No-op unless the runtime declares one (docker/native have no invoice).
	startCloudCostPoller(context.Background(), mgr)

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

	// Scheduled execution (docs/38 + ADR0021): a CP-resident scheduler watches the
	// wall clock and drives due schedules (cron/interval/once). ON by default with a
	// 1m tick (opt-out): the tick is a single indexed due-query and is a no-op while no
	// schedule exists, so the sane default is "a schedule you create actually fires."
	// Set AF_SCHEDULER_INTERVAL=0 to hard-disable it (no timed wakes at all) on a
	// deployment where unattended workspace wakes are unwanted (cost/policy). A firing
	// schedule wakes a stopped workspace and injects a session unattended; the P2 wake
	// firer resolves the owner, applies the wake policy, holds a reaper keep-alive for
	// AF_SCHEDULE_SETTLE, and injects via create_session.
	if iv := parseDurationOr(os.Getenv("AF_SCHEDULER_INTERVAL"), time.Minute); iv > 0 {
		settle := parseDurationOr(os.Getenv("AF_SCHEDULE_SETTLE"), 5*time.Minute)
		ready := parseDurationOr(os.Getenv("AF_SCHEDULE_WAKE_TIMEOUT"), 90*time.Second)
		// Per-schedule fire jitter (★2) spreads aligned cron times (everyone at 09:00)
		// so simultaneous wakes don't OOM the shared host. Default 2m; AF_SCHEDULE_JITTER=0
		// disables. Set before any schedule's next_run is computed so it takes effect.
		if strings.TrimSpace(os.Getenv("AF_SCHEDULE_JITTER")) == "0" {
			scheduleJitterMax = 0
		} else {
			scheduleJitterMax = parseDurationOr(os.Getenv("AF_SCHEDULE_JITTER"), 2*time.Minute)
		}
		firer := newWakeFirer(mgr, settle, ready)
		schedulerRunning = true // so the operator API stops warning that fires never happen
		go newScheduler(mgr.store, firer, iv).run(context.Background())
		logSchedulerFirerNote(firer)
		log.Printf("scheduler: enabled (interval=%s settle=%s wake_timeout=%s jitter=%s)", iv, settle, ready, scheduleJitterMax)
	}

	// Login providers (docs/61 §61.6): Google (historical env names) plus every id
	// listed in AF_OIDC_PROVIDERS. A provider with incomplete config is dropped
	// with a warning inside; only the unsafe multi-tenant Entra case errors, and
	// that is fatal below. Must run before buildMux — it captures cfg by value.
	provs, provErr := buildLoginProviders(cfg)
	cfg.setProviders(provs)
	// The admin API validates a tenant's allowed_providers against this set, so a
	// rule can't name an IdP the deployment doesn't have (docs/61 §61.9.4).
	mgr.knownProviderIDs = map[string]bool{}
	for _, p := range provs {
		mgr.knownProviderIDs[p.ID()] = true
		// ★ Stamp the realm on the logins this provider recorded before the column
		// existed (docs/61 §61.15). Here rather than in the migration because only
		// the set just built knows which id is which IdP — and rule 1.5 refuses to
		// act on a realm it had to guess. A failure is logged, never fatal: the
		// consequence is one join that falls back to today's behavior.
		if realm := providerRealm(p); realm != "" {
			if err := mgr.store.FillProviderRealm(ctx, p.ID(), realm); err != nil {
				log.Printf("WARNING: recording the realm of login provider %q: %v", p.ID(), err)
			}
		}
	}

	mux := buildMux(cfg)

	// In oauth mode the CP is the edge (behind Funnel): gate every request on a
	// verified session. dev/proxy modes keep the prior behavior (no gate; proxy
	// trusts the upstream oauth2-proxy header).
	var handler http.Handler = mux
	if cfg.mgr.authMode == "oauth" {
		if provErr != nil {
			log.Fatalf("AUTH=oauth: %v", provErr)
		}
		if !cfg.oauthConfigured() {
			log.Fatalf("AUTH=oauth requires AF_COOKIE_SECRET, PUBLIC_BASE_URL and at least one login provider " +
				"(GOOGLE_OAUTH_CLIENT_ID + GOOGLE_OAUTH_CLIENT_SECRET, AF_OIDC_PROVIDERS with AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST}, " +
				"and/or GITHUB_OAUTH_CLIENT_ID + GITHUB_OAUTH_CLIENT_SECRET + AF_GITHUB_ALLOWED_ORGS)")
		}
		// ★ "No allowlist" no longer means "nobody can sign in": since P3 the entry
		// gate also admits anyone on a tenant's roster or matching an auto-join
		// domain (docs/61 §61.9.6), which is the whole point of the invite-run
		// deployment — no AF_OAUTH_ALLOWED_* at all. So check the database before
		// warning, and only say "every login is denied" when that is actually true.
		if !cfg.hasDeploymentAllowlist() && !anyProviderAllowlist(cfg.providers) {
			hasRoster, err := mgr.store.AnyActiveMembership(ctx)
			// A tenant-defined provider carries its own (mandatory) domain list
			// (docs/61 §61.11), so an approved one is also a way in — counting it keeps
			// the warning from claiming "every login is denied" on a deployment that
			// runs entirely on a subsidiary's own IdP.
			if !hasRoster && err == nil {
				if rows, _, lerr := mgr.store.ListActiveTenantIdPs(ctx); lerr == nil && len(rows) > 0 {
					hasRoster = true
				}
			}
			switch {
			case err != nil:
				log.Printf("WARNING: could not check for existing memberships: %v", err)
			case hasRoster:
				log.Printf("login: no email allowlist configured — access is governed by tenant membership and auto_join_domains (docs/61 §61.9.6)")
			default:
				log.Printf("WARNING: AUTH=oauth with no allowlist (AF_OAUTH_ALLOWED_EMAILS / AF_OAUTH_ALLOWED_DOMAINS / AF_OAUTH_ALLOWED_EMAILS_FILE / AF_OIDC_<ID>_ALLOWED_EMAILS / AF_OIDC_<ID>_ALLOWED_DOMAINS / AF_GITHUB_ALLOWED_ORGS) and no tenant membership or auto_join_domains — every login is denied")
			}
		}
		ids := make([]string, 0, len(cfg.providers))
		for _, p := range cfg.providers {
			ids = append(ids, p.ID())
		}
		log.Printf("login providers: %s", strings.Join(ids, ", "))
		handler = cfg.authGate(mux)
	} else if provErr != nil {
		log.Printf("WARNING: login provider config rejected (AUTH=%s so it is unused): %v", cfg.mgr.authMode, provErr)
	}

	// Ask the runtime what it will really run rather than printing the docker default:
	// on ECS the image comes from AF_ECS_WORKSPACE_IMAGE, and a banner naming the wrong
	// one is worse than no banner when somebody is trying to tell which build is live.
	wsImage := cfg.mgr.image
	if f, ok := cfg.mgr.rtFactory.(interface{ WorkspaceImage() string }); ok {
		if img := f.WorkspaceImage(); img != "" {
			wsImage = img
		}
	}
	log.Printf("control-plane %s on %s (console=%s, ws image=%s, auth=%s, runtime=%s)", buildVersion, cfg.addr, cfg.consoleDir, wsImage, cfg.mgr.authMode, rtProfile)
	log.Print("edge: " + clientIPBanner())
	// withClientIP is OUTERMOST on purpose: it is the only place the forwarding
	// headers are read, so nothing downstream can be tempted to trust a client's own
	// claim about where it is calling from (docs/66 §66.6).
	srv := &http.Server{Addr: cfg.addr, Handler: withClientIP(logRequests(gzipMiddleware(etagJSON(handler)))), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// superAdminEmailList returns SUPER_ADMIN_EMAILS as a slice (the manager keeps the
// same value as a lookup set). Read once at startup — unlike the allowlist file it
// is NOT live-read, so changing it takes a CP restart, which is also when the
// revocation pass above runs.
func superAdminEmailList() []string {
	var out []string
	for e := range emailSet(os.Getenv("SUPER_ADMIN_EMAILS")) {
		out = append(out, e)
	}
	return out
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

// intervalOff is parseDurationOr for the settings where an explicit "0" means OFF rather
// than "use the default". parseDurationOr cannot say that: it falls back on anything that
// is not a POSITIVE duration, so AF_IDLE_SWEEP_INTERVAL=0 — documented right where it is
// read as "disables the reaper entirely" — quietly gave the 1-minute default instead
// (measured: the reaper logged interval=1m0s with the variable set to 0).
//
// Garbage still falls back. A misspelled value should not silently switch a sweep off;
// only a duration the operator actually wrote as non-positive does.
func intervalOff(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil {
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

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		uri := r.URL.RequestURI()
		// OAuth 系パスのクエリには認可コード等の機微値が乗るためマスクする
		if r.URL.RawQuery != "" && strings.Contains(r.URL.Path, "oauth") {
			uri = r.URL.Path + "?<redacted>"
		}
		log.Printf("%s %s %d %s", r.Method, uri, sw.code(), time.Since(start).Round(time.Millisecond))
	})
}

// statusWriter captures the response status for the access log.
//
// Why it exists: a public deployment is found by vulnerability scanners within hours
// (measured on af.lazmix.jp — 172 probes for /actuator/heapdump, /.env and friends in
// the first 9 hours), and the log could not answer the only question that matters about
// them: was that a 401 or a 200? Every line looked identical.
//
// It forwards the two optional interfaces the CP actually depends on. A wrapper that
// hides them does not show up as a bad log line — it shows up as an SSE stream that
// never streams (Flusher) or a terminal that never connects (Hijacker).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK // handler wrote nothing explicit; net/http sends 200
	}
	return w.status
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("connection does not support hijacking")
	}
	// The response never gets a status through WriteHeader after this point; the
	// upgrade IS the outcome, so record it as one instead of logging a bare 200.
	w.status = http.StatusSwitchingProtocols
	return hj.Hijack()
}
