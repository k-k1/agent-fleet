package main

// tenant_wiring.go — the seam onto the tenant family (`internal/tenantsrv`). It carries
// three things, and no bare aliases.
//
//  1. The `tenantAPI` / `adminAPI` types themselves, which cannot be aliases: cloudcost.go,
//     usage.go, audit.go, admin_stats.go, admin_sessions.go, metrics.go, usage_hourly.go,
//     cost_profile.go and workspace_sizing.go all use `adminAPI` as a method receiver, and
//     Go cannot define a method on another package's type, so the type has to stay in
//     package main.
//  2. A thin wrapper per moved handler. routes.go registers unexported methods such as
//     `adm.listTenants`, and an unexported method cannot cross the package boundary, so an
//     alias cannot stand in for one. A wrapper is not a bare alias and is counted
//     separately (ADR 0067 decision 8).
//  3. The implementation of `tenantsrv.CP` (`cpTenant`). tenantsrv cannot import package
//     main, so this is the only path to manager / memberAuth / package-level functions.
//
// There is no product logic here. The one thing that can only be written on the main side
// is the encoding of tenant.limits (limits.go's tenantLimits owns the json tags); a field
// missed in that copy is caught by the reflect check in tenants_test.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
	"github.com/k-k1/agent-fleet/control-plane/internal/tenantsrv"
)

// --- The types that stay in package main (reason 1 above) ----------------------------

// tenantAPI serves the caller-facing tenant picker: memberAuth embedded plus a narrow view
// of TenantStore. The registration side wraps it in withIdentity.
type tenantAPI struct {
	memberAuth
	store store.TenantStore
}

func newTenantAPI(m *manager) tenantAPI { return tenantAPI{memberAuth{m}, m.store} }

func (a tenantAPI) srv() tenantsrv.Tenants { return tenantsrv.NewTenants(cpTenant{a.mgr}, a.store) }

// adminAPI is the tenant/membership admin handler set (docs/log/23): tenant
// CRUD, memberships, quotas plus the deployment-wide admin views (usage.go,
// audit.go, admin_sessions.go, admin_stats.go, metrics.go hostStats). The
// handlers span most sub-stores (tenant / identity / membership / workspace /
// quota / usage / audit / session index), so no narrow view — they reach the
// full store via a.mgr.store. Handlers gated up-front register through
// withSuperAdmin; per-tenant ones gate mid-handler via memberAuth.tenantAdminFor
// (slug comes from the path on some routes and the body on others).
//
// The tenant family's handler bodies live in internal/tenantsrv and the wrappers below
// connect to them. The cross-cutting views (usage / audit / sessions / stats / cloud cost /
// host) are still methods on package main.
type adminAPI struct{ memberAuth }

func newAdminAPI(m *manager) adminAPI { return adminAPI{memberAuth{m}} }

func (a adminAPI) srv() tenantsrv.Admin { return tenantsrv.NewAdmin(cpTenant{a.mgr}) }

// --- The thin wrappers (routes.go and the existing tests call these unchanged) --------

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

// --- The seam adapter -----------------------------------------------------------------

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

// --- The tenant.limits blob (limits.go is its only encoder) ---------------------------

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
