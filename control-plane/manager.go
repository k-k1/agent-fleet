package main

import (
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// manager owns the set of per-membership Workspace runtimes. As of P3-2 (docs/14)
// identity↔tenant is many-to-many: the gateway email identifies the person
// (identity), the active tenant is chosen explicitly per request (X-AF-Tenant)
// and validated against the person's memberships, and a Workspace (container) is
// resolved per membership (= identity × tenant, fully isolated). The DB
// (MetadataStore) is the source of truth; `rts` is an in-memory cache of built
// runtimes keyed by membership id. The Agent contract is unchanged.
type manager struct {
	// mu guards ONLY the in-memory maps below (runtime/lock/activity caches) — it is
	// never held across store/docker I/O（docs/log/23 P2-W2、以前は resolve 全体を
	// この 1 本で直列化していた）。初回解決の I/O は per-membership の
	// buildLocks で直列化する（buildResolved, resolver.go）。
	mu                     sync.Mutex
	rts                    map[string]cachedRT    // cache keyed by membership id; DB is source of truth
	buildLocks             map[string]*sync.Mutex // per-membership first-resolve serialization
	startLocks             map[string]*sync.Mutex // per-workspace start/recreate serialization
	shareLocks             map[string]*sync.Mutex // per-owner share ACL/catalog/approval serialization
	activityLocks          map[string]*sync.Mutex
	activityProtectedUntil map[string]time.Time
	store                  store.Store
	conns                  *connRegistry // P3-9: live activity/attachment tracking for idle-stop
	// idleForecasts は reaper が毎スイープで置いていく「この Workspace はいつ止まるか /
	// なぜ止まらないか」（docs/log/75 P4）。管理画面はここを読むだけで、判定をやり直さない。
	//
	// ★再導出しないことが要件そのもの: 画面が自前で計算すると、reaper が実際に見ている
	// もの（在席・保留中の対話・ピン・共有ウォーターマーク）とズレて、「止まらない理由」を
	// 調べるための画面が別の答えを出す。それなら無い方がましなので、reaper の**決定を
	// そのまま**公開する。鮮度はスイープ間隔（既定 60 秒）。
	idleForecasts map[string]idleForecast

	// rtFactory is the profile-selected Runtime adapter builder (Docker locally,
	// ECS on AWS; P3-7). Every runtime is constructed through it — see runtimeFor.
	rtFactory RuntimeFactory

	// costPoller is the Cost Explorer poller when this deployment has an AWS bill and
	// the credentials to read it (docs/log/67, ADR 0048), else nil. Held only so the API can
	// surface the poller's last failure — an AccessDenied there is indistinguishable
	// from "nothing was spent" if it is not shown.
	costPoller *cloudCostPoller

	// usageInterval is AF_USAGE_SAMPLE_INTERVAL, recorded at boot because the 稼働時間
	// heatmap needs it as a DENOMINATOR: an hour's cell is running_secs ÷ (samples ×
	// interval), and a deployment that samples every minute stores twelve times the
	// samples for the same occupancy (docs/log/83). 0 when the sampler is switched off,
	// which is also the honest answer to "why is the heatmap empty".
	usageInterval time.Duration

	// autoBakeGolden is AF_ECS_EC2_GOLDEN_AUTOBAKE, recorded at boot for the pool
	// screen. It is the one thing about the golden that is NOT visible in AWS, and
	// without it "no golden, nothing under way" cannot be told apart from "no golden,
	// and nothing ever will be" (docs/log/64 §64.30).
	autoBakeGolden bool

	// nativeRuntime is true when AF_RUNTIME is native/wsl (containerless, single-user;
	// docs/log/34). Native is a personal dev host with no shared-host contention, so the
	// concurrent-session quota is not enforced there — see sessionQuotaExceeded.
	nativeRuntime bool

	// template fields shared by every runtime
	image    string
	dataRoot string
	// defaultTenantID is cached at boot so rootedDataDir can tell a flat
	// (<dataRoot>/<key>) default-tenant path from a nested (<dataRoot>/<slug>/<key>)
	// one without a per-call store lookup.
	defaultTenantID string
	agentHost       string
	memory          string
	// memMaxBytes is the deployment-wide HARD ceiling for a per-workspace RAM cap
	// (AF_MAX_WORKSPACE_MEM, bytes; 0 = no extra ceiling). It bounds a tenant_admin's
	// per-user mem_limit on top of the per-tenant cap so no single workspace can be
	// sized past what the shared host can bear. See resolveWorkspaceMemBytes.
	memMaxBytes int64
	sessionCmd  string
	extraEnv    []string

	portBase int

	// user resolution (AuthGateway port). authMode: "dev" | "proxy".
	authMode    string
	devUser     string
	emailHeader string

	// provisioning policy (docs/14): "auto" (gateway-trusted auto-provision into
	// the default tenant) | "invite" (deny unknown identities). superAdmins are
	// emails granted identity.role=super_admin (deployment-wide).
	//
	// docs/log/61 §61.9.8 puts a third case FIRST: a tenant whose auto_join_domains
	// matches the address wins over both of these, so a deployment can split into
	// departments by domain without touching AF_PROVISION.
	provisionMode string
	superAdmins   map[string]bool

	// tenantLogin caches the per-tenant login rules (docs/log/61 §61.9.7). Read by the
	// entry gate on every request and by the tenant gate on every resolution;
	// dropped by every admin write that can change who may enter.
	tenantLogin *tenantLoginCache
	// tenantIdP is the RUNTIME set of tenant-defined login providers (docs/log/61
	// §61.11). It lives here rather than in config for the reason tenant_idp.go
	// gives: config is copied by value into every handler, so a set stored there
	// could never change without a restart — and approving or suspending a
	// subsidiary's IdP has to take effect at once.
	tenantIdP *tenantIdPRegistry
	// knownProviderIDs is the set of ENV-defined login provider ids this deployment
	// enabled, so the admin API can refuse a tenant rule naming one that does not
	// exist. nil in AUTH=proxy/dev, where the check is skipped.
	//
	// ★ Tenant-defined ids are deliberately absent: they come and go at runtime, and
	// tenant.allowed_providers is validated against this set at SAVE time, when a
	// still-pending provider legitimately has no entry yet. tenantProviderIDsFor
	// answers that half of the question instead.
	knownProviderIDs map[string]bool

	// at-rest encryption (P3-3). master32 = SHA-256 of AF_MASTER_KEY (nil in dev).
	// custodian wraps/unwraps per-workspace DEKs; nil in dev (no encryption).
	master32  []byte
	custodian KeyCustodian

	// git token signing without AF_MASTER_KEY (dev): a per-deployment random
	// master persisted under dataRoot, lazily created (git_http.go gitSignKey).
	gitDevMasterOnce sync.Once
	gitDevMaster     []byte

	// internalGitHost is the host of PUBLIC_BASE_URL (docs/reference/internal-git-
	// provider). When set, each workspace gets a deterministic per-membership git
	// token injected for this host so clone/push against the CP's self-hosted repos
	// authenticate transparently via the cred helper. Empty (no PUBLIC_BASE_URL) =
	// internal git disabled.
	internalGitHost string

	// publicBaseURL is PUBLIC_BASE_URL verbatim (scheme + host). Injected into each
	// workspace as AF_CP_BASE_URL so the in-container フリート・オペレーター can reach
	// the CP's /internal/memos bridge over the public hairpin (memo_bridge.go). Empty
	// (no PUBLIC_BASE_URL) = the memo bridge is not reachable, so it is not injected.
	publicBaseURL string

	// previewDomain is AF_PREVIEW_DOMAIN — the parent of the per-start preview
	// subdomains (docs/log/81). Empty = host-mode preview is off for this deployment
	// (no wildcard DNS / certificate), and only the path-mode /preview/{port}
	// exists. Held here as well as on config because the container env injection
	// (workspaceExtraEnv) has to name the URLs the app will be reachable at.
	previewDomain string
}

// apiError carries an HTTP status + machine code for handlers to return.
type apiError struct {
	status  int
	code    string
	message string
}

func internalErr(err error) *apiError {
	return &apiError{status: http.StatusInternalServerError, code: "internal", message: err.Error()}
}

// cachedRT memoizes a built runtime + its workspace record per membership.
type cachedRT struct {
	rt Runtime
	ws store.Workspace
}

// resolved is the full per-request resolution: runtime + workspace record +
// identity + selected membership. Handlers needing tenant/quota context use this.
type resolved struct {
	rt    Runtime
	ws    store.Workspace
	ident store.Identity
	mv    store.MembershipView
}

// workspaceNames derives the container/network/home for a (tenant, user). The
// default tenant keeps the flat af-ws-<key> scheme so the existing live
// deployment is reused unchanged; other tenants are scoped by slug.
func (m *manager) workspaceNames(slug, key string) (name, network, dataDir string) {
	if slug == defaultTenantSlug {
		return "af-ws-" + key, "af-net-" + key, filepath.Join(m.dataRoot, key)
	}
	return "af-ws-" + slug + "-" + key, "af-net-" + slug + "-" + key, filepath.Join(m.dataRoot, slug, key)
}

// rootedDataDir re-bases a workspace's on-disk root onto the CURRENT dataRoot.
// data_dir is persisted at creation with the then-current dataRoot, so a restore
// or move to a different DATA_DIR (or a changed WS_DATA) leaves the stored value
// stale — mounting it would silently give the workspace an empty home. The stable
// part is the suffix (<key> for the default tenant, <slug>/<key> otherwise, per
// workspaceNames); we keep the trailing segment(s) and swap the root. Idempotent
// when the path is already current. See docs/log/p3-10-packaging.md §20.3(B).
func (m *manager) rootedDataDir(ws store.Workspace) string {
	return runtime.RootedDataDir(m.dataRoot, m.defaultTenantID, runtime.Workspace(ws))
}

// runtimeFor builds the Runtime for a workspace through the profile-selected
// factory (Docker locally, ECS on AWS). It is the one construction call the rest
// of the CP uses; the state/stop-only sites below also route through it (secretKey
// "") so no concrete adapter leaks into manager.
func (m *manager) runtimeFor(ws store.Workspace, secretKey string, extraEnv ...string) Runtime {
	// The conversion is the seam: the adapters declare their own copy of this record
	// (internal/runtime/deps.go) so they need not import the store while it is being
	// moved in parallel. Field-for-field identical, so a divergence stops compiling here.
	return m.rtFactory.New(runtime.Workspace(ws), secretKey, extraEnv)
}

// evictTenantCache drops the memoized runtimes for a tenant so they are rebuilt
// with fresh per-tenant env (e.g. the AF_AGENT_SELF_UPDATE_ALLOWED gate) on the
// next request — called after an admin edits the tenant's limits, so the policy
// takes effect at the following container start without a CP restart.
func (m *manager) evictTenantCache(tenantID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, c := range m.rts {
		if c.ws.TenantID == tenantID {
			delete(m.rts, k)
		}
	}
}

// evictMembershipCache drops one membership's memoized runtime so the next resolve
// rebuilds it — used when a per-workspace setting changes so the new env reaches the
// next container start.
func (m *manager) evictMembershipCache(membershipID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rts, membershipID)
}
