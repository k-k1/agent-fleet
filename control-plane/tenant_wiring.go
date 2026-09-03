package main

// tenant_wiring.go — tenant 家系（`internal/tenantsrv`）の**切断面**。3 つのものを持つ。
//
// 🔴 **ここに「素の別名」は 1 本も無い**（RECLAIM-D で 0 を確認）。元は `alias_tenant.go` という
// 名前だったが、**エイリアスを持たないので実態と合わず**、姉妹（`git_wiring.go` /
// `mcp_wiring.go` / `memory_wiring.go` / `session_wiring.go`）に合わせて改名した。
// **`alias_*.go` が残っていると「回収の剥がし残し」と誤読される**——回収の完了は
// 「`alias_*.go` が消えたこと」で測られてきたため。
//
//  1. `tenantAPI` / `adminAPI` の**型そのもの**。エイリアスにはできない: `adminAPI` は
//     cloudcost.go / usage.go / audit.go / admin_stats.go / admin_sessions.go /
//     metrics.go / usage_hourly.go / cost_profile.go / workspace_sizing.go が
//     **メソッドのレシーバとして使っている**（Go は他パッケージの型にメソッドを
//     定義できない）ので、型は package main に残るしかない。
//  2. 移した各ハンドラの**薄いラッパ**。routes.go の登録は `adm.listTenants` のような
//     **非公開メソッド**で、非公開メソッドは境界を越えられずエイリアスでも解けない。
//     ラッパは「素の別名」とは別勘定（ADR 0067 決定 8 / #315 が数を外した箇所）。
//     🔴 **routes.go は 1 行も変えていない**（`routes.golden` 無変更で担保）。
//  3. `tenantsrv.CP` の実装（`cpTenant`）。tenantsrv は package main を import できない
//     ので、manager / memberAuth / package 関数へ届く道はこれ 1 本だけ。
//
// ここに製品ロジックは無い。唯一「main 側でしか書けない」のは tenant.limits の
// エンコード（limits.go の tenantLimits が json タグの唯一の出所）で、その写し漏れは
// tenants_test.go の reflect 検査が落とす。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/tenantsrv"
)

// --- 受け皿の型（package main に残る。理由は上の 1.）---------------------------------

// tenantAPI serves the caller-facing tenant picker（docs/log/23 残③: memberAuth 埋め
// 込み + TenantStore の narrow view。登録側で withIdentity に包む）。
type tenantAPI struct {
	memberAuth
	store store.TenantStore
}

func newTenantAPI(m *manager) tenantAPI { return tenantAPI{memberAuth{m}, m.store} }

func (a tenantAPI) srv() tenantsrv.Tenants { return tenantsrv.NewTenants(cpTenant{a.mgr}, a.store) }

// adminAPI is the tenant/membership admin handler set（docs/log/23 残③）: tenant
// CRUD, memberships, quotas plus the deployment-wide admin views (usage.go,
// audit.go, admin_sessions.go, admin_stats.go, metrics.go hostStats). The
// handlers span most sub-stores (tenant / identity / membership / workspace /
// quota / usage / audit / session index), so no narrow view — they reach the
// full store via a.mgr.store. Handlers gated up-front register through
// withSuperAdmin; per-tenant ones gate mid-handler via memberAuth.tenantAdminFor
// (slug comes from the path on some routes and the body on others).
//
// テナント家系のハンドラ本体は internal/tenantsrv にあり、下のラッパが繋ぐ。
// 横断ビュー（usage / audit / sessions / stats / cloud cost / host）は今も
// package main のメソッドのまま。
type adminAPI struct{ memberAuth }

func newAdminAPI(m *manager) adminAPI { return adminAPI{memberAuth{m}} }

func (a adminAPI) srv() tenantsrv.Admin { return tenantsrv.NewAdmin(cpTenant{a.mgr}) }

// --- 薄いラッパ（19 本。routes.go / 既存テストの呼び出しは 1 行も変わらない）-----------

func (a tenantAPI) list(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().List(w, r, ident)
}

func (a adminAPI) listTenants(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().ListTenants(w, r, ident)
}

func (a adminAPI) setTenantLogin(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().SetTenantLogin(w, r, ident)
}

func (a adminAPI) listMembers(w http.ResponseWriter, r *http.Request) { a.srv().ListMembers(w, r) }

func (a adminAPI) stopWorkspace(w http.ResponseWriter, r *http.Request) { a.srv().StopWorkspace(w, r) }

func (a adminAPI) cleanHome(w http.ResponseWriter, r *http.Request) { a.srv().CleanHome(w, r) }

func (a adminAPI) destroyWorkspace(w http.ResponseWriter, r *http.Request) {
	a.srv().DestroyWorkspace(w, r)
}

func (a adminAPI) createTenant(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().CreateTenant(w, r, ident)
}

func (a adminAPI) addMembership(w http.ResponseWriter, r *http.Request) { a.srv().AddMembership(w, r) }

func (a adminAPI) removeMembership(w http.ResponseWriter, r *http.Request) {
	a.srv().RemoveMembership(w, r)
}

func (a adminAPI) deleteMembership(w http.ResponseWriter, r *http.Request) {
	a.srv().DeleteMembership(w, r)
}

func (a adminAPI) deleteTenant(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().DeleteTenant(w, r, ident)
}

func (a adminAPI) setTenantLimits(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().SetTenantLimits(w, r, ident)
}

func (a adminAPI) setUserLimit(w http.ResponseWriter, r *http.Request) { a.srv().SetUserLimit(w, r) }

func (a adminAPI) setMembershipRole(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().SetMembershipRole(w, r, ident)
}

func (a adminAPI) poolStatus(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	a.srv().PoolStatus(w, r, ident)
}

func (a adminAPI) tenantNetwork(w http.ResponseWriter, r *http.Request) { a.srv().TenantNetwork(w, r) }

func (a adminAPI) setTenantNetwork(w http.ResponseWriter, r *http.Request) {
	a.srv().SetTenantNetwork(w, r)
}

func (a adminAPI) tenantSlotClass(w http.ResponseWriter, r *http.Request) {
	a.srv().TenantSlotClass(w, r)
}

func (a adminAPI) setTenantSlotClass(w http.ResponseWriter, r *http.Request) {
	a.srv().SetTenantSlotClass(w, r)
}

// --- 切断面のアダプタ ----------------------------------------------------------------

// cpTenant implements tenantsrv.CP over the CP manager. Every method is a one-liner
// onto something that already existed; nothing here is new behaviour.
type cpTenant struct{ m *manager }

var _ tenantsrv.CP = cpTenant{}

func (d cpTenant) Store() store.Store                { return d.m.store }
func (d cpTenant) KnownProviderIDs() map[string]bool { return d.m.knownProviderIDs }
func (d cpTenant) EvictMembershipCache(mid string)   { d.m.evictMembershipCache(mid) }
func (d cpTenant) EvictTenantCache(tid string)       { d.m.evictTenantCache(tid) }
func (d cpTenant) InvalidateTenantLogin()            { d.m.tenantLogin.invalidate() }
func (d cpTenant) IdleForecastFor(wsID string) (any, bool) {
	f, ok := d.m.idleForecastFor(wsID)
	if !ok {
		// ⚠️ A typed nil-ish value must not cross as a non-nil `any`: the caller only
		// stores the forecast when ok is true, so return the untyped zero here.
		return nil, false
	}
	return f, true
}
func (d cpTenant) WorkspaceSizing() runtime.WorkspaceSizing { return d.m.workspaceSizing() }
func (d cpTenant) IsSystemTenantSlug(slug string) bool      { return isSystemTenantSlug(slug) }
func (d cpTenant) SanitizeUser(s string) string             { return sanitizeUser(s) }
func (d cpTenant) SplitCSVLower(s string) []string          { return splitCSVLower(s) }
func (d cpTenant) SplitDomainCSV(s string) []string         { return splitDomainCSV(s) }
func (d cpTenant) JoinCSV(v []string) string                { return joinCSV(v) }
func (d cpTenant) TrustedProxyHops() int                    { return trustedProxyHops }
func (d cpTenant) IPInAny(ip netip.Addr, prefixes []netip.Prefix) bool {
	return ipInAny(ip, prefixes)
}

func (d cpTenant) DomainMatches(domains []string, email string) bool {
	return domainMatches(domains, email)
}

func (d cpTenant) WorkspaceLifecycleLeaseError(err error) *tenantsrv.APIError {
	return tenantAPIError(workspaceLifecycleLeaseError(err))
}

func (d cpTenant) MembershipsFor(ctx context.Context, ident store.Identity) ([]store.MembershipView, *tenantsrv.APIError) {
	ms, aerr := d.m.membershipsFor(ctx, ident)
	return ms, tenantAPIError(aerr)
}

func (d cpTenant) CountRunningInTenant(ctx context.Context, tenantID string) (int, error) {
	return d.m.countRunningInTenant(ctx, tenantID)
}

func (d cpTenant) WorkspaceStateByMembership(ctx context.Context, mid string) (string, string) {
	return d.m.workspaceStateByMembership(ctx, mid)
}

func (d cpTenant) StopWorkspaceByMembership(ctx context.Context, mid string) error {
	return d.m.stopWorkspaceByMembership(ctx, mid)
}

func (d cpTenant) CleanHomeByMembership(ctx context.Context, mid string) error {
	return d.m.cleanHomeByMembership(ctx, mid)
}

func (d cpTenant) DestroyWorkspaceByMembership(ctx context.Context, mid string) ([]string, error) {
	return d.m.destroyWorkspaceByMembership(ctx, mid)
}

func (d cpTenant) ResolveWorkspaceSize(ctx context.Context, ws store.Workspace) (int64, int, int) {
	return d.m.resolveWorkspaceSize(ctx, ws)
}

func (d cpTenant) ResolveSlotClass(ctx context.Context, ws store.Workspace) (string, string) {
	return d.m.resolveSlotClass(ctx, ws)
}

func (d cpTenant) PoolBudget(ctx context.Context, overrideTenantID string, overrideMax int) (runtime.PoolBudget, bool, error) {
	return d.m.poolBudget(ctx, overrideTenantID, overrideMax)
}

func (d cpTenant) PoolStatus(ctx context.Context) (runtime.EC2PoolStatus, bool, error) {
	return d.m.poolStatus(ctx)
}

func (d cpTenant) TenantAdminFor(w http.ResponseWriter, r *http.Request, slug string) (store.Identity, store.Tenant, bool) {
	return memberAuth{d.m}.tenantAdminFor(w, r, slug)
}

func (d cpTenant) ResolveMember(r *http.Request, slug, key string) (store.Membership, store.Workspace, bool, *tenantsrv.APIError) {
	mem, ws, hasWS, aerr := adminAPI{memberAuth{d.m}}.resolveMember(r, slug, key)
	return mem, ws, hasWS, tenantAPIError(aerr)
}

func (d cpTenant) ClientIPFrom(ctx context.Context) tenantsrv.ClientIP {
	info := clientIPFrom(ctx)
	return tenantsrv.ClientIP{IP: info.IP, OK: info.OK, Forwarded: info.Forwarded}
}

func (d cpTenant) ParseCIDRList(s string) ([]netip.Prefix, []string, *tenantsrv.APIError) {
	prefixes, normalized, aerr := parseCIDRList(s)
	return prefixes, normalized, tenantAPIError(aerr)
}

// --- tenant.limits の blob（エンコードは limits.go 側が唯一の出所）---------------------

func (d cpTenant) ParseLimits(raw string) tenantsrv.Limits { return tenantLimitsOut(parseLimits(raw)) }

// LimitsFor reads and parses a tenant's limits, or the zero value. Losing the rest of
// the blob on a read error would silently reset quotas the operator set, so callers
// that WRITE must treat a failure here as "no change is safe" — which the zero value
// is not. Every caller reads it immediately before writing it back, and a store that
// cannot be read cannot be written either, so the write fails too.
func (d cpTenant) LimitsFor(ctx context.Context, tenantID string) tenantsrv.Limits {
	t, err := d.m.store.GetTenant(ctx, tenantID)
	if err != nil {
		return tenantsrv.Limits{}
	}
	return tenantLimitsOut(parseLimits(t.Limits))
}

func (d cpTenant) StoreTenantLimits(ctx context.Context, tenantID string, l tenantsrv.Limits) error {
	lj, err := json.Marshal(tenantLimitsIn(l))
	if err != nil {
		return err
	}
	return d.m.store.SetTenantLimits(ctx, tenantID, string(lj))
}

// tenantLimitsOut / tenantLimitsIn convert between limits.go's tenantLimits (which
// owns the json tags — the stored blob's ONLY encoder) and the seam's projection.
// 🔴 They are field-for-field on purpose, and tenants_test.go's reflect check fails
// the build if either struct grows a field the other does not have: a silently
// dropped field here would erase a stored quota on the next save.
func tenantLimitsOut(l tenantLimits) tenantsrv.Limits {
	return tenantsrv.Limits{
		MaxWorkspaces:                l.MaxWorkspaces,
		MaxSessions:                  l.MaxSessions,
		MaxGitRepos:                  l.MaxGitRepos,
		MaxLFSBytes:                  l.MaxLFSBytes,
		MaxWorkspaceMem:              l.MaxWorkspaceMem,
		MaxWorkspaceCPU:              l.MaxWorkspaceCPU,
		MaxWorkspaceDiskGB:           l.MaxWorkspaceDiskGB,
		SlotClass:                    l.SlotClass,
		AllowedSlotClasses:           l.AllowedSlotClasses,
		SessionIdleTimeout:           l.SessionIdleTimeout,
		WSIdleTimeout:                l.WSIdleTimeout,
		InteractionIdleTimeout:       l.InteractionIdleTimeout,
		HomeHibernateAfter:           l.HomeHibernateAfter,
		HomeBackupEvery:              l.HomeBackupEvery,
		AllowAgentSelfUpdate:         l.AllowAgentSelfUpdate,
		TerminalHistoryRetentionDays: l.TerminalHistoryRetentionDays,
	}
}

func tenantLimitsIn(l tenantsrv.Limits) tenantLimits {
	return tenantLimits{
		MaxWorkspaces:                l.MaxWorkspaces,
		MaxSessions:                  l.MaxSessions,
		MaxGitRepos:                  l.MaxGitRepos,
		MaxLFSBytes:                  l.MaxLFSBytes,
		MaxWorkspaceMem:              l.MaxWorkspaceMem,
		MaxWorkspaceCPU:              l.MaxWorkspaceCPU,
		MaxWorkspaceDiskGB:           l.MaxWorkspaceDiskGB,
		SlotClass:                    l.SlotClass,
		AllowedSlotClasses:           l.AllowedSlotClasses,
		SessionIdleTimeout:           l.SessionIdleTimeout,
		WSIdleTimeout:                l.WSIdleTimeout,
		InteractionIdleTimeout:       l.InteractionIdleTimeout,
		HomeHibernateAfter:           l.HomeHibernateAfter,
		HomeBackupEvery:              l.HomeBackupEvery,
		AllowAgentSelfUpdate:         l.AllowAgentSelfUpdate,
		TerminalHistoryRetentionDays: l.TerminalHistoryRetentionDays,
	}
}

// tenantAPIError converts the CP's apiError (unexported fields) to the seam's copy.
func tenantAPIError(e *apiError) *tenantsrv.APIError {
	if e == nil {
		return nil
	}
	return &tenantsrv.APIError{Status: e.status, Code: e.code, Message: e.message}
}
