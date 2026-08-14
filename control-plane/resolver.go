// resolver.go — アイデンティティ／テナント／メンバーシップ解決（resolve 系）。
// manager.go からの機械的分割（docs/23 P2-W2）。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
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

// upsertIdentity resolves a request to the person behind it. Under AUTH=oauth the
// request context carries the (provider, subject) the session was minted with
// (authGate), and that pair — not the email — decides who this is: an email
// change must not move user_key, which is the workspace home directory name
// (docs/61 §61.5). AUTH=proxy and AUTH=dev have no IdP subject to offer, so they
// keep resolving by email exactly as before; failing closed here would break
// every existing proxy deployment and dev itself.
//
// key/email are the caller's own, taken from resolveIdentity on the same request,
// so the context pair always describes the same person. Do not call this with
// another person's key while a session context is in scope — the pair wins.
// ★ It is also the choke point for the two things a TENANT-DEFINED provider must
// not be able to do (docs/61 §61.11 + 決定 31/32), and they are enforced here rather
// than at each caller on purpose — identityFor, resolveFull and resolveMembership
// all pass roleHintFor(email), and a rule that has to be repeated three times is a
// rule that will be missed the fourth time:
//
//   - no deployment role. roleHintFor matches SUPER_ADMIN_EMAILS on the address
//     alone, so without this a subsidiary's administrator could mint a token
//     carrying the operator's address and be upgraded to super_admin — and
//     UpsertIdentity never downgrades, so removing the provider afterwards would
//     not take it back (決定 30 の乗っ取り経路 4).
//   - no join by email. The (provider, subject) pair is the only key, because that
//     issuer belongs to the subsidiary and its assertion about an address is not
//     proof of being that person (LinkIdentity's rule 2').
func (m *manager) upsertIdentity(ctx context.Context, email, key, roleHint string) (Identity, error) {
	if ref, ok := loginRefFrom(ctx); ok {
		tenantDefined := isTenantProviderID(ref.provider)
		if tenantDefined {
			roleHint = ""
		}
		ident, _, err := m.store.LinkIdentity(ctx, ref.provider, ref.subject, email, key, roleHint, !tenantDefined)
		return ident, err
	}
	return m.store.UpsertIdentity(ctx, email, key, roleHint)
}

// identityErr maps an identity-resolution failure to the response. Only one of them
// is the caller's business: errIdentityClaimed means a tenant-defined provider
// asserted an address that already belongs to somebody (LinkIdentity rule 2'), and
// answering 500 would send an operator looking for a fault that does not exist.
// The normal path refuses it at the callback, before a session exists.
func identityErr(err error) *apiError {
	if errors.Is(err, errIdentityClaimed) {
		return &apiError{http.StatusForbidden, "email_taken", errIdentityClaimed.Error()}
	}
	return internalErr(err)
}

func (m *manager) roleHintFor(email string) string {
	if email != "" && m.superAdmins[strings.ToLower(email)] {
		return "super_admin"
	}
	return ""
}

// tenantAdminFor reports whether ident may administer tenant tID: a deployment
// super_admin (any tenant) or a tenant_admin member of that specific tenant.
// This is the per-tenant admin gate; deployment-wide actions (create tenant,
// tenant quotas, host stats, role grants) stay super_admin-only.
//
// ★ The membership must be ACTIVE. GetMembership deliberately returns
// deactivated rows too — the offboarding sequence needs them, since stopping the
// workspace and wiping the home happen after the person is off the roster — so
// the status check has to be here. Without it, a tenant_admin who was removed
// would still administer the tenant from a session cookie that stays valid for
// up to AF_SESSION_TTL, and could put themselves straight back on the roster
// (docs/61 §61.10.7 の穴 2).
func (m *manager) tenantAdminFor(ctx context.Context, ident Identity, tID string) bool {
	if ident.Role == "super_admin" {
		return true
	}
	mem, ok, err := m.store.GetMembership(ctx, ident.ID, tID)
	return err == nil && ok && mem.Status == "active" && mem.Role == "tenant_admin"
}

// identityFor upserts and returns the caller's identity (used by /api/tenants and
// admin RBAC). 401 if the gateway gave no identity.
func (m *manager) identityFor(ctx context.Context, r *http.Request) (Identity, *apiError) {
	id := m.resolveIdentity(r)
	if id.key == "" {
		return Identity{}, &apiError{http.StatusUnauthorized, "unauthenticated", "no gateway identity"}
	}
	ident, err := m.upsertIdentity(ctx, id.email, id.key, m.roleHintFor(id.email))
	if err != nil {
		return Identity{}, identityErr(err)
	}
	return ident, nil
}

// membershipsFor returns the caller's memberships, provisioning one when the
// person has none. The order is docs/61 §61.9.8, and it matters:
//
//  1. an existing membership always wins — that is the roster, and somebody put
//     this person on it deliberately (§61.9.5)
//  2. otherwise a tenant whose auto_join_domains matches the address
//  3. otherwise AF_PROVISION=invite → not_provisioned (unchanged)
//  4. otherwise AF_PROVISION=auto → the default tenant (unchanged)
//
// An invite-run deployment lives entirely in 1; a small single-tenant one lives
// entirely in 2 and never opens the invite screen. Steps 3 and 4 are exactly what
// they were, so a deployment that sets no auto_join_domains sees no change.
func (m *manager) membershipsFor(ctx context.Context, ident Identity) ([]MembershipView, *apiError) {
	ms, err := m.store.ListMemberships(ctx, ident.ID)
	if err != nil {
		return nil, internalErr(err)
	}
	if len(ms) > 0 {
		return ms, nil
	}
	if t, contested, ok := m.tenantLogin.autoJoinTenant(ctx, ident.Email); ok && !m.hasAnyMembershipRow(ctx, ident.ID, t.ID) {
		if _, err := m.store.EnsureMembership(ctx, ident.ID, t.ID, "member"); err != nil {
			return nil, internalErr(err)
		}
		// The person now holds a membership, which is also an entry-gate term — the
		// cached "no" for this address has to go (docs/61 §61.9.7).
		m.tenantLogin.invalidate()
		if contested {
			// ★ Never join silently when the configuration is ambiguous: more than one
			// tenant claimed this domain and the lowest slug won by rule, which is a
			// decision an administrator has to be able to find afterwards (§61.9.8).
			log.Printf("WARNING: auto-join: %s matched more than one tenant; joined %q (lowest slug)", ident.Email, t.Slug)
			_ = m.store.InsertAudit(ctx, AuditLog{
				ID: newID(), TenantID: t.ID, ActorKind: "system", ActorID: ident.ID,
				Action: "tenant.auto_join_conflict", Target: t.Slug,
				Detail: "domain claimed by multiple tenants; lowest slug won", At: nowTS(),
			})
		}
		ms, err = m.store.ListMemberships(ctx, ident.ID)
		if err != nil {
			return nil, internalErr(err)
		}
		return ms, nil
	}
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
	m.tenantLogin.invalidate()
	ms, err = m.store.ListMemberships(ctx, ident.ID)
	if err != nil {
		return nil, internalErr(err)
	}
	if len(ms) == 0 {
		// The only way to land here is a membership row that exists but is
		// inactive — somebody was taken off this roster. Say so, rather than
		// falling through to selectMembership's "specify X-AF-Tenant" 409, which is
		// what a removed person used to see.
		return nil, &apiError{http.StatusForbidden, "not_provisioned", "no tenant membership; ask an administrator"}
	}
	return ms, nil
}

// hasAnyMembershipRow reports whether a membership row exists at all, ACTIVE OR
// NOT. An inactive row is a decision somebody made — auto-join must respect it
// rather than quietly putting the person back (docs/61 §61.9.8 rule 1: an existing
// membership is authoritative, and "removed" is an answer too).
func (m *manager) hasAnyMembershipRow(ctx context.Context, identityID, tenantID string) bool {
	_, ok, err := m.store.GetMembership(ctx, identityID, tenantID)
	return err == nil && ok
}

// checkTenantProvider enforces tenant.allowed_providers (docs/61 §61.9.4 + 決定
// 14). This is the ENFORCEMENT point; filtering the login page's buttons is only
// presentation, and without this check a person could sign in on the generic
// /login with any enabled provider and then simply send X-AF-Tenant for a tenant
// that was configured to accept one specific IdP.
//
// ★ It answers with provider_required, not a bare forbidden. A session carries
// exactly one provider (決定 18), so moving between departments with different
// IdPs legitimately requires signing in again — and being told "not allowed" when
// the remedy is one click away is how support tickets are made. The Console builds
// that link from the tenant's allowed_providers, which /api/tenants reports per
// membership (tenants.go), so this error itself needs no extra payload.
// ★ It also pins a TENANT-DEFINED session to its own tenant (docs/61 §61.11 + 決定
// 32-3). That check cannot be left to allowed_providers: a tenant that names no
// providers accepts every one of them, so without this a subsidiary's own IdP would
// be a way into every such tenant in the deployment. The subsidiary's administrator
// controls that issuer, so the only tenant its assertions may open is theirs.
func (m *manager) checkTenantProvider(ctx context.Context, mv MembershipView) *apiError {
	prov := sessionProviderFrom(ctx)
	if slug, _, ok := parseTenantProviderID(prov); ok && slug != mv.TenantSlug {
		return &apiError{http.StatusForbidden, "provider_required",
			"tenant " + mv.TenantSlug + " cannot be used with a sign-in method defined by tenant " + slug}
	}
	ok, allowed := m.tenantLogin.providerAllowed(ctx, mv.TenantID, prov)
	if ok {
		return nil
	}
	return &apiError{http.StatusForbidden, "provider_required",
		"tenant " + mv.TenantSlug + " requires signing in with: " + strings.Join(allowed, ", ")}
}

// resolveFull maps a request's identity + selected tenant to its runtime and
// records, creating the workspace on first use. tenantSel is the X-AF-Tenant
// value (slug or tenant id); empty means "default selection".
func (m *manager) resolveFull(ctx context.Context, key, email, tenantSel string) (*resolved, *apiError) {
	ident, err := m.upsertIdentity(ctx, email, key, m.roleHintFor(email))
	if err != nil {
		return nil, identityErr(err)
	}
	ms, aerr := m.membershipsFor(ctx, ident)
	if aerr != nil {
		return nil, aerr
	}
	mv, aerr := selectMembership(ms, tenantSel)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := m.checkTenantProvider(ctx, mv); aerr != nil {
		return nil, aerr
	}
	return m.buildResolved(ctx, ident, mv)
}

// resolveMembership maps a request's identity + selected tenant to its identity and
// membership WITHOUT building/creating a workspace — for lightweight per-member
// resources (e.g. SSM host bookmarks) that don't need a running container.
func (m *manager) resolveMembership(ctx context.Context, key, email, tenantSel string) (Identity, MembershipView, *apiError) {
	ident, err := m.upsertIdentity(ctx, email, key, m.roleHintFor(email))
	if err != nil {
		return Identity{}, MembershipView{}, identityErr(err)
	}
	ms, aerr := m.membershipsFor(ctx, ident)
	if aerr != nil {
		return Identity{}, MembershipView{}, aerr
	}
	mv, aerr := selectMembership(ms, tenantSel)
	if aerr != nil {
		return Identity{}, MembershipView{}, aerr
	}
	if aerr := m.checkTenantProvider(ctx, mv); aerr != nil {
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

// buildResolved maps an (identity, membership) to its runtime + workspace,
// creating the workspace on first use. docs/23 P2-W2: 旧実装（buildResolvedLocked）
// は manager.mu を store/custodian I/O 越しに保持し、全メンバーシップの解決を
// 1 本のロックで直列化していた。現在 m.mu が守るのはキャッシュ map だけで、
// I/O は per-membership の build ロック下で走る — 同一メンバーシップの初回同時
// リクエストが workspace を二重作成しない一方、別メンバーシップは並行に解決する。
// 注: evict*Cache との競合はベストエフォート（ビルド中に evict されると直後の
// キャッシュ格納が旧 env のまま残り得るが、ポリシー変更は「次のコンテナ起動で
// 反映」というこれまでの意味論の範囲内）。
func (m *manager) buildResolved(ctx context.Context, ident Identity, mv MembershipView) (*resolved, *apiError) {
	if c, ok := m.cachedRTFor(mv.MembershipID); ok {
		return &resolved{rt: c.rt, ws: c.ws, ident: ident, mv: mv}, nil
	}
	bl := m.buildLockFor(mv.MembershipID)
	bl.Lock()
	defer bl.Unlock()
	if c, ok := m.cachedRTFor(mv.MembershipID); ok { // built while we waited
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
	// Resolve the per-workspace RAM cap (0 = deployment default) so the factory can
	// size the next container start; the built runtime captures it by value.
	ws.MemBytes = m.resolveWorkspaceMemBytes(ctx, ws)
	rt := m.runtimeFor(ws, dekHex, m.workspaceExtraEnv(ctx, ws)...)
	m.mu.Lock()
	m.rts[mv.MembershipID] = cachedRT{rt: rt, ws: ws}
	m.mu.Unlock()
	return &resolved{rt: rt, ws: ws, ident: ident, mv: mv}, nil
}

// cachedRTFor reads the runtime cache under the (now cache-only) manager lock.
func (m *manager) cachedRTFor(membershipID string) (cachedRT, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rts[membershipID]
	return c, ok
}

// buildLockFor returns the per-membership build mutex (lazily allocated).
func (m *manager) buildLockFor(membershipID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.buildLocks == nil {
		m.buildLocks = map[string]*sync.Mutex{}
	}
	l, ok := m.buildLocks[membershipID]
	if !ok {
		l = &sync.Mutex{}
		m.buildLocks[membershipID] = l
	}
	return l
}

// startLockFor returns the per-workspace start mutex (lazily allocated). It
// serializes concurrent container starts of the SAME workspace (explicit start,
// the ×4 auto-start paths, scheduler wake, recreate/clean-home): unserialized,
// a later docker `rm -f` can destroy the container an earlier start just
// brought up, and on native a pidfile overwrite orphans the running agent.
func (m *manager) startLockFor(wsID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startLocks == nil {
		m.startLocks = map[string]*sync.Mutex{}
	}
	l, ok := m.startLocks[wsID]
	if !ok {
		l = &sync.Mutex{}
		m.startLocks[wsID] = l
	}
	return l
}

// shareLockFor serializes the brief ACL/catalog claim and the one authorized
// Agent operation with owner-side share changes. Unlike the previous database
// transaction, this does not hold a DB connection or write lock during HTTP I/O.
func (m *manager) shareLockFor(ownerMembershipID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shareLocks == nil {
		m.shareLocks = map[string]*sync.Mutex{}
	}
	l, ok := m.shareLocks[ownerMembershipID]
	if !ok {
		l = &sync.Mutex{}
		m.shareLocks[ownerMembershipID] = l
	}
	return l
}

// resolveByMembership resolves a runtime from a PAT's stored identity+membership
// (the MCP path, which has no gateway headers). The membership must still be an
// active membership of the identity — so a revoked membership 403s here.
//
// No allowed_providers check: this path has no browser session and therefore no
// provider to match. A PAT is authorized by the membership it was issued against,
// and deactivating that membership (docs/61 §61.10.6) is what revokes it — which
// ListMemberships already enforces by only returning active rows.
func (m *manager) resolveByMembership(ctx context.Context, identityID, membershipID string) (*resolved, *apiError) {
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
			return m.buildResolved(ctx, ident, mv)
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
