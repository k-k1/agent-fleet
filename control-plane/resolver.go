// resolver.go — アイデンティティ／テナント／メンバーシップ解決（resolve 系）。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// identity is what the AuthGateway resolves a request to.
type identity struct{ key, email string }

func (m *manager) resolveIdentity(r *http.Request) identity {
	// proxy: trust the upstream oauth2-proxy header. oauth: authGate has verified
	// the Google session and set the same header (stripping any inbound value).
	if m.authMode == "proxy" || m.authMode == "oauth" {
		e := r.Header.Get(m.emailHeader)
		return identity{key: sanitizeUser(e), email: e}
	}
	return identity{key: m.devUser}
}

func (m *manager) resolveUser(r *http.Request) string { return m.resolveIdentity(r).key }

func (m *manager) roleHintFor(email string) string {
	if email != "" && m.superAdmins[strings.ToLower(email)] {
		return "super_admin"
	}
	return ""
}

// tenantAdminFor reports whether ident may administer tenant tID: a deployment
// super_admin (any tenant) or a tenant_admin member of that specific tenant.
// This is the per-tenant admin gate; deployment-wide actions (create tenant,
// tenant quotas, clean-home, host stats, role grants) stay super_admin-only.
func (m *manager) tenantAdminFor(ctx context.Context, ident Identity, tID string) bool {
	if ident.Role == "super_admin" {
		return true
	}
	mem, ok, err := m.store.GetMembership(ctx, ident.ID, tID)
	return err == nil && ok && mem.Role == "tenant_admin"
}

// identityFor upserts and returns the caller's identity (used by /api/tenants and
// admin RBAC). 401 if the gateway gave no identity.
func (m *manager) identityFor(ctx context.Context, r *http.Request) (Identity, *apiError) {
	id := m.resolveIdentity(r)
	if id.key == "" {
		return Identity{}, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"}
	}
	ident, err := m.store.UpsertIdentity(ctx, id.email, id.key, m.roleHintFor(id.email))
	if err != nil {
		return Identity{}, internalErr(err)
	}
	return ident, nil
}

// membershipsFor returns the caller's memberships, auto-provisioning a default
// membership when the policy allows and the person has none.
func (m *manager) membershipsFor(ctx context.Context, ident Identity) ([]MembershipView, *apiError) {
	ms, err := m.store.ListMemberships(ctx, ident.ID)
	if err != nil {
		return nil, internalErr(err)
	}
	if len(ms) == 0 {
		if m.provisionMode == "invite" {
			return nil, &apiError{http.StatusForbidden, "not_provisioned", "no tenant membership; ask an administrator"}
		}
		t, err := m.store.EnsureDefaultTenant(ctx)
		if err != nil {
			return nil, internalErr(err)
		}
		if _, err := m.store.EnsureMembership(ctx, ident.ID, t.ID, "member"); err != nil {
			return nil, internalErr(err)
		}
		ms, err = m.store.ListMemberships(ctx, ident.ID)
		if err != nil {
			return nil, internalErr(err)
		}
	}
	return ms, nil
}

// resolveFull maps a request's identity + selected tenant to its runtime and
// records, creating the workspace on first use. tenantSel is the X-AF-Tenant
// value (slug or tenant id); empty means "default selection".
func (m *manager) resolveFull(ctx context.Context, key, email, tenantSel string) (*resolved, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ident, err := m.store.UpsertIdentity(ctx, email, key, m.roleHintFor(email))
	if err != nil {
		return nil, internalErr(err)
	}
	ms, aerr := m.membershipsFor(ctx, ident)
	if aerr != nil {
		return nil, aerr
	}
	mv, aerr := selectMembership(ms, tenantSel)
	if aerr != nil {
		return nil, aerr
	}
	return m.buildResolvedLocked(ctx, ident, mv)
}

// resolveMembership maps a request's identity + selected tenant to its identity and
// membership WITHOUT building/creating a workspace — for lightweight per-member
// resources (e.g. SSM host bookmarks) that don't need a running container.
func (m *manager) resolveMembership(ctx context.Context, key, email, tenantSel string) (Identity, MembershipView, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ident, err := m.store.UpsertIdentity(ctx, email, key, m.roleHintFor(email))
	if err != nil {
		return Identity{}, MembershipView{}, internalErr(err)
	}
	ms, aerr := m.membershipsFor(ctx, ident)
	if aerr != nil {
		return Identity{}, MembershipView{}, aerr
	}
	mv, aerr := selectMembership(ms, tenantSel)
	if aerr != nil {
		return Identity{}, MembershipView{}, aerr
	}
	return ident, mv, nil
}

// selectMembership picks the active membership for a request. tenantSel (slug or
// id) is required when the person belongs to more than one tenant; a single
// membership is auto-selected.
func selectMembership(ms []MembershipView, tenantSel string) (MembershipView, *apiError) {
	switch {
	case tenantSel != "":
		for _, x := range ms {
			if x.TenantSlug == tenantSel || x.TenantID == tenantSel {
				return x, nil
			}
		}
		return MembershipView{}, &apiError{http.StatusForbidden, "forbidden_tenant", "not a member of tenant " + tenantSel}
	case len(ms) == 1:
		return ms[0], nil
	default:
		return MembershipView{}, &apiError{http.StatusConflict, "tenant_selection_required", "specify X-AF-Tenant; you belong to multiple tenants"}
	}
}

// buildResolvedLocked maps an (identity, membership) to its runtime + workspace,
// creating the workspace on first use. Assumes m.mu is held.
func (m *manager) buildResolvedLocked(ctx context.Context, ident Identity, mv MembershipView) (*resolved, *apiError) {
	if c, ok := m.rts[mv.MembershipID]; ok {
		return &resolved{rt: c.rt, ws: c.ws, ident: ident, mv: mv}, nil
	}
	ws, ok, err := m.store.GetWorkspaceByMembership(ctx, mv.MembershipID)
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok {
		ws, err = m.createWorkspace(ctx, mv, ident.UserKey)
		if err != nil {
			return nil, internalErr(err)
		}
	}
	dekHex, err := m.resolveDEK(ctx, ws, ident.UserKey)
	if err != nil {
		return nil, internalErr(err)
	}
	rt := m.runtimeFor(ws, dekHex, m.workspaceExtraEnv(ctx, ws)...)
	m.rts[mv.MembershipID] = cachedRT{rt: rt, ws: ws}
	return &resolved{rt: rt, ws: ws, ident: ident, mv: mv}, nil
}

// resolveByMembership resolves a runtime from a PAT's stored identity+membership
// (the MCP path, which has no gateway headers). The membership must still be an
// active membership of the identity — so a revoked membership 403s here.
func (m *manager) resolveByMembership(ctx context.Context, identityID, membershipID string) (*resolved, *apiError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ident, ok, err := m.store.GetIdentityByID(ctx, identityID)
	if err != nil {
		return nil, internalErr(err)
	}
	if !ok {
		return nil, &apiError{http.StatusUnauthorized, "unauthenticated", "identity not found"}
	}
	ms, err := m.store.ListMemberships(ctx, identityID)
	if err != nil {
		return nil, internalErr(err)
	}
	for _, mv := range ms {
		if mv.MembershipID == membershipID {
			return m.buildResolvedLocked(ctx, ident, mv)
		}
	}
	return nil, &apiError{http.StatusForbidden, "forbidden_tenant", "membership not active"}
}

// resolve returns just the runtime (proxy/terminal handlers).
func (m *manager) resolve(ctx context.Context, key, email, tenantSel string) (Runtime, *apiError) {
	res, aerr := m.resolveFull(ctx, key, email, tenantSel)
	if aerr != nil {
		return nil, aerr
	}
	return res.rt, nil
}

var userInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUser turns an email (or any id) into a container-name-safe key.
func sanitizeUser(s string) string {
	s = userInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}
