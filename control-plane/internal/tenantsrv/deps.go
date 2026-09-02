// deps.go — この package が CP から必要とするものを、切断面の**こちら側**で宣言する。
//
// internal/tenantsrv はテナント/メンバーシップの **HTTP・管理層**（`/api/tenants` の
// テナント選択、`/api/admin/**` のテナント CRUD・ロスター・クォータ・ネットワーク規則・
// スロットクラス）で、CP の `manager` / `memberAuth` には依存しない。manager は CP の
// god object で、そのメソッドは resolver.go / workspace_lifecycle.go / reaper.go ほかに
// 散っている——運べないし、運ぶべきでもない。
//
// なので外向きの依存は 1 本のインタフェース CP に集める。**struct の公開フィールド
// （関数フィールドの束）にしていない**のは ADR 0067 決定 5 の但し書きどおり: フィールド
// を 1 本埋め忘れると nil のまま無言で no-op になり、reflect の網羅検査を別に書かないと
// 気付けない。インタフェースなら埋め忘れは**コンパイルエラー**なので、検査そのものが要らない。
// （CP-MCP の internal/mcpsrv が同じ形で 26 依存を 1 本にまとめた先例。）
//
// 逆向き（main が tenantsrv を呼ぶ側）は control-plane/alias_tenant.go 1 枚で吸収する。
// ハンドラは `adminAPI` / `tenantAPI` の**非公開メソッド**として登録されており（routes.go）、
// 非公開メソッドは境界を越えられないので、あちらは素の別名ではなく**薄いラッパ**になる。
//
// 🔴 **認証家系（tenant_idp_api.go / tenant_login.go / tenant_git_oauth*.go /
// tenant_idp_secret.go）はここに入らない。** それらは internal/auth と往復しており、
// ADR 0067 決定 1（元パッケージへ手を伸ばし返す家系は外す）で対象外。ただしその家系が
// 持つ CSV/ドメインのヘルパ（splitCSVLower / splitDomainCSV / joinCSV / domainMatches）は
// 入口ゲートと**同じ規則**なので、写さずに CP 経由で借りる（写すと二つ目の出所になる）。
package tenantsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// CP is everything tenantsrv needs from the control plane. The implementation is
// control-plane/alias_tenant.go's adapter over *manager.
type CP interface {
	// --- 素材 ------------------------------------------------------------------
	// Store is the CP metadata store. The tenant/admin handlers span most sub-stores
	// (tenant / identity / membership / workspace / quota / git / audit), so there is
	// no narrow view here — that is the same call tenants.go documented for adminAPI.
	Store() store.Store
	// KnownProviderIDs is the set of ENV-defined login provider ids this deployment
	// has (main.go). nil means "not built yet" and is checked as such: setTenantLogin
	// only refuses a provider when the set exists.
	KnownProviderIDs() map[string]bool

	// --- 解決 / ライフサイクル（manager のメソッド） ------------------------------
	MembershipsFor(ctx context.Context, ident store.Identity) ([]store.MembershipView, *APIError)
	CountRunningInTenant(ctx context.Context, tenantID string) (int, error)
	WorkspaceStateByMembership(ctx context.Context, membershipID string) (container, state string)
	StopWorkspaceByMembership(ctx context.Context, membershipID string) error
	CleanHomeByMembership(ctx context.Context, membershipID string) error
	DestroyWorkspaceByMembership(ctx context.Context, membershipID string) ([]string, error)
	ResolveWorkspaceSize(ctx context.Context, ws store.Workspace) (memBytes int64, cpuUnits, diskGB int)
	ResolveSlotClass(ctx context.Context, ws store.Workspace) (id, note string)
	EvictMembershipCache(membershipID string)
	EvictTenantCache(tenantID string)
	// InvalidateTenantLogin drops the cached per-tenant login rules. Those rules ARE
	// the entry gate, so every write that changes who may sign in calls it.
	InvalidateTenantLogin()
	// IdleForecastFor is the reaper's last recorded "when does this stop / who is
	// holding it" reading, returned verbatim for the admin roster. It is `any` on
	// purpose: the CP's idleForecast is only ever JSON-encoded from here, and copying
	// its shape would put a second declaration of a wire contract in this package.
	IdleForecastFor(wsID string) (any, bool)
	PoolBudget(ctx context.Context, overrideTenantID string, overrideMax int) (runtime.PoolBudget, bool, error)
	PoolStatus(ctx context.Context) (runtime.EC2PoolStatus, bool, error)
	WorkspaceSizing() runtime.WorkspaceSizing

	// --- HTTP の認可（memberAuth と admin_stats.go） ------------------------------
	// TenantAdminFor gates the per-tenant admin face. It writes the 401/403 itself
	// and returns ok=false, exactly as memberAuth does for every other feature API.
	TenantAdminFor(w http.ResponseWriter, r *http.Request, slug string) (store.Identity, store.Tenant, bool)
	// ResolveMember is admin_stats.go's member lookup (membership + workspace row).
	ResolveMember(r *http.Request, slug, key string) (mem store.Membership, ws store.Workspace, hasWS bool, aerr *APIError)

	// --- 方針・一次情報（写すと二つ目の出所になるもの） ----------------------------
	// IsSystemTenantSlug is system_tenant.go's reserved-tenant rule.
	IsSystemTenantSlug(slug string) bool
	// SanitizeUser is resolver.go's key/slug normalization — the same function that
	// decides what a user key looks like everywhere else in the CP.
	SanitizeUser(s string) string
	// SplitCSVLower / SplitDomainCSV / JoinCSV / DomainMatches are tenant_login.go's
	// login-rule text conventions. Borrowed rather than copied: they are read back by
	// the entry gate, and a divergence would let a rule be saved that the gate then
	// reads differently.
	SplitCSVLower(s string) []string
	SplitDomainCSV(s string) []string
	JoinCSV(v []string) string
	DomainMatches(domains []string, email string) bool
	// WorkspaceLifecycleLeaseError renders the "somebody else holds the lifecycle
	// lease" refusal (workspace_handlers.go).
	WorkspaceLifecycleLeaseError(err error) *APIError

	// --- tenant.limits の blob（**エンコードは main 側の tenantLimits が唯一の出所**）---
	// ParseLimits parses a stored limits blob; LimitsFor reads and parses a tenant's.
	// StoreTenantLimits marshals a Limits back through the CP's own tenantLimits and
	// writes it. Encoding deliberately does NOT happen here: limits.go owns the json
	// tags, and a second encoder would silently drop a field it had not heard of.
	// Limits below is an input/read projection of that struct, and
	// control-plane/tenants_test.go's reflect check fails the build if the two drift.
	ParseLimits(raw string) Limits
	LimitsFor(ctx context.Context, tenantID string) Limits
	StoreTenantLimits(ctx context.Context, tenantID string, l Limits) error

	// --- 呼び元のアドレス（clientip.go・ADR 0047） --------------------------------
	// TrustedProxyHops is AF_TRUSTED_PROXY_HOPS, read once at boot. It is a func on
	// the seam rather than a value passed in, because the network handlers read it
	// per request and the tests in package main set it per case.
	TrustedProxyHops() int
	ClientIPFrom(ctx context.Context) ClientIP
	ParseCIDRList(s string) (prefixes []netip.Prefix, normalized []string, aerr *APIError)
	IPInAny(ip netip.Addr, prefixes []netip.Prefix) bool
}

// ClientIP is what the edge middleware could work out about the caller, as this
// package consumes it — the three fields of the CP's clientIPInfo. Forwarded is here
// for the same reason it is there: "hops=0 AND an XFF arrived" is the
// misconfiguration that would otherwise let an administrator allowlist the load
// balancer and believe they had restricted something (ADR 0047 決定 4).
type ClientIP struct {
	IP        netip.Addr
	OK        bool
	Forwarded bool
}

// Limits is the read/input projection of the CP's tenantLimits (limits.go). It has
// no json tags on purpose: this package never encodes it — StoreTenantLimits hands it
// back to the CP, which marshals the real struct. The field set must stay identical,
// which control-plane/tenants_test.go checks with reflect.
type Limits struct {
	MaxWorkspaces                int
	MaxSessions                  int
	MaxGitRepos                  int
	MaxLFSBytes                  int64
	MaxWorkspaceMem              int64
	MaxWorkspaceCPU              int
	MaxWorkspaceDiskGB           int
	SlotClass                    string
	AllowedSlotClasses           []string
	SessionIdleTimeout           string
	WSIdleTimeout                string
	InteractionIdleTimeout       string
	HomeHibernateAfter           string
	HomeBackupEvery              string
	AllowAgentSelfUpdate         bool
	TerminalHistoryRetentionDays int
}

// APIError carries an HTTP status + machine code, mirroring the CP's apiError
// (manager.go). It is a copy rather than an import because apiError's fields are
// unexported and the CP is package main; the adapter converts at the seam, and the
// field ORDER matches so the positional literals in this package read the same.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func internalErr(err error) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: err.Error()}
}

// writeJSON / writeAPIErr mirror httpapi.go, exactly as internal/mcpsrv does. They
// are five-line, side-effect-free encoders and duplicating them is cheaper — and far
// less coupling — than putting an HTTP writer on the CP seam. The response SHAPE is
// the contract, and it is identical.
func writeJSON(w http.ResponseWriter, status int, v any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIErr(w http.ResponseWriter, e *APIError) {
	writeJSON(w, e.Status, map[string]any{
		"error": map[string]string{"code": e.Code, "message": e.Message},
	})
}
