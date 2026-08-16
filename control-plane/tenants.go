package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// tenantAPI serves the caller-facing tenant picker（docs/23 残③: memberAuth 埋め
// 込み + TenantStore の narrow view。登録側で withIdentity に包む）。
type tenantAPI struct {
	memberAuth
	store TenantStore
}

func newTenantAPI(m *manager) tenantAPI { return tenantAPI{memberAuth{m}, m.store} }

// list (GET /api/tenants) returns the caller's memberships for the Console
// tenant picker (docs/14 P3-2). Single-membership users get one entry, so the
// Console can auto-select and hide the picker.
func (a tenantAPI) list(w http.ResponseWriter, r *http.Request, ident Identity) {
	isSuper := ident.Role == "super_admin"
	ms, aerr := a.mgr.membershipsFor(r.Context(), ident)
	if aerr != nil {
		// ★ A super_admin with no membership still gets an answer (docs/61 §61.10.2
		// + 決定 23). Bootstrapping a deployment with AF_PROVISION=invite used to
		// dead-end right here: this endpoint 403'd, the Console's error branch left
		// superAdmin false, and the admin menu — whose condition is
		// `superAdmin || tenant_admin` — never appeared, so the one person entitled
		// to create the first tenant could not reach the screen that creates it.
		// The admin API itself was always reachable (it gates on identityFor), so
		// this only ever blocked the UI; it is still not a workable procedure.
		if aerr.code == "not_provisioned" && isSuper {
			writeJSON(w, http.StatusOK, map[string]any{"tenants": []any{}, "super_admin": true})
			return
		}
		writeAPIErr(w, aerr)
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		// allow_agent_self_update: the operator gate, surfaced so the Console can
		// show/hide the member's "keep CLIs updated" toggle for this tenant.
		allowUpd := false
		var provs []string
		if t, err := a.store.GetTenant(r.Context(), m.TenantID); err == nil {
			allowUpd = parseLimits(t.Limits).AllowAgentSelfUpdate
			provs = splitCSVLower(t.AllowedProviders)
		}
		out = append(out, map[string]any{
			"slug": m.TenantSlug, "name": m.TenantName, "role": m.Role,
			"allow_agent_self_update": allowUpd,
			// Which sign-in methods this tenant accepts (docs/61 §61.9.4; empty = any).
			// The Console needs it to turn a provider_required refusal into a
			// re-sign-in link instead of a dead end.
			"allowed_providers": provs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out, "super_admin": isSuper})
}

// adminAPI is the tenant/membership admin handler set（docs/23 残③）: tenant
// CRUD, memberships, quotas plus the deployment-wide admin views (usage.go,
// audit.go, admin_sessions.go, admin_stats.go, metrics.go hostStats). The
// handlers span most sub-stores (tenant / identity / membership / workspace /
// quota / usage / audit / session index), so no narrow view — they reach the
// full store via a.mgr.store. Handlers gated up-front register through
// withSuperAdmin; per-tenant ones gate mid-handler via memberAuth.tenantAdminFor
// (slug comes from the path on some routes and the body on others).
type adminAPI struct{ memberAuth }

func newAdminAPI(m *manager) adminAPI { return adminAPI{memberAuth{m}} }

// listTenants (GET /api/admin/tenants) — overview for the admin UI.
// super_admin sees every tenant; a tenant_admin sees only the tenants they
// administer. The super_admin flag lets the Console hide deployment-wide controls
// (create tenant, tenant quotas, clean-home, role grants) for tenant_admins.
func (a adminAPI) listTenants(w http.ResponseWriter, r *http.Request, ident Identity) {
	isSuper := ident.Role == "super_admin"

	var tenants []Tenant
	if isSuper {
		ts, err := a.mgr.store.ListTenants(r.Context())
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		tenants = ts
	} else {
		ms, err := a.mgr.store.ListMemberships(r.Context(), ident.ID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		for _, m := range ms {
			if m.Role != "tenant_admin" {
				continue
			}
			if t, err := a.mgr.store.GetTenant(r.Context(), m.TenantID); err == nil {
				tenants = append(tenants, t)
			}
		}
	}

	out := make([]map[string]any, 0, len(tenants))
	for _, t := range tenants {
		members, _ := a.mgr.store.ListMembersByTenant(r.Context(), t.ID)
		running, _ := a.mgr.countRunningInTenant(r.Context(), t.ID)
		lim := parseLimits(t.Limits)
		out = append(out, map[string]any{
			"slug": t.Slug, "name": t.Name, "status": t.Status, "isolation": t.Isolation,
			"users": len(members), "running": running,
			"max_workspaces": lim.MaxWorkspaces, "max_sessions": lim.MaxSessions,
			"max_git_repos":         lim.MaxGitRepos,
			"max_lfs_bytes":         lim.MaxLFSBytes,
			"max_workspace_mem":     lim.MaxWorkspaceMem,
			"max_workspace_cpu":     lim.MaxWorkspaceCPU,
			"max_workspace_disk_gb": lim.MaxWorkspaceDiskGB,
			"session_idle_timeout":  lim.SessionIdleTimeout, "ws_idle_timeout": lim.WSIdleTimeout,
			"allow_agent_self_update":         lim.AllowAgentSelfUpdate,
			"terminal_history_retention_days": lim.TerminalHistoryRetentionDays,
			// Per-tenant login rules (docs/61 §61.9.7), for the admin editor.
			"allowed_providers": t.AllowedProviders,
			"auto_join_domains": t.AutoJoinDomains,
			"allowed_domains":   t.AllowedDomains,
			"hidden_providers":  t.HiddenProviders,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out, "super_admin": isSuper})
}

// setTenantLogin (PUT /api/admin/tenants/{slug}/login) stores the per-tenant login
// rules (docs/61 §61.9.7). super_admin only, deliberately: two of the three fields
// reach past the tenant. auto_join_domains widens the DEPLOYMENT's entry gate — a
// whole email domain gets in — and allowed_providers decides which IdP is trusted
// to say who someone is. Those are the operator's calls (決定 24/25), while what a
// tenant_admin owns is the roster inside their own tenant.
func (a adminAPI) setTenantLogin(w http.ResponseWriter, r *http.Request, ident Identity) {
	var body struct {
		AllowedProviders string `json:"allowed_providers"`
		AutoJoinDomains  string `json:"auto_join_domains"`
		AllowedDomains   string `json:"allowed_domains"`
		HiddenProviders  string `json:"hidden_providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	t, ok, err := a.mgr.store.GetTenantBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	provs := splitCSVLower(body.AllowedProviders)
	autoJoin := splitDomainCSV(body.AutoJoinDomains)
	allowed := splitDomainCSV(body.AllowedDomains)
	hidden := splitCSVLower(body.HiddenProviders)

	// Naming a provider the deployment does not have would silently produce a login
	// page with no buttons, so refuse it here rather than at 3am.
	//
	// ★ A "t:<slug>:<name>" id is one of the tenant's OWN sign-in methods (docs/61
	// §61.11), which is how a subsidiary says "only our Entra, please". It is checked
	// against that tenant's rows instead of knownProviderIDs (which holds env ids
	// only), and the slug must be this tenant's: naming another tenant's method would
	// produce a button nobody here can use, since the tenant gate pins such a session
	// to its own tenant anyway (決定 32-3).
	var ownIdP map[string]bool
	// ★ hidden_providers is validated by the same rule, and for the same reason: a
	// typo there is silent (the button simply keeps appearing) and nothing else in
	// the system will ever mention it. It is checked TOGETHER with allowed_providers
	// so both halves of "which methods does this tenant know about" stay one rule
	// (docs/61 §61.15.9).
	for _, p := range append(append([]string(nil), provs...), hidden...) {
		if slug, name, isTenant := parseTenantProviderID(p); isTenant {
			if ownIdP == nil {
				rows, err := a.mgr.store.ListTenantIdPs(r.Context(), t.ID)
				if err != nil {
					writeAPIErr(w, internalErr(err))
					return
				}
				ownIdP = map[string]bool{}
				for _, row := range rows {
					ownIdP[row.Name] = true
				}
			}
			if slug != t.Slug || !ownIdP[name] {
				writeAPIErr(w, &apiError{http.StatusBadRequest, "unknown_provider",
					"tenant " + t.Slug + " has no sign-in method named " + p})
				return
			}
			continue
		}
		if a.mgr.knownProviderIDs != nil && !a.mgr.knownProviderIDs[p] {
			writeAPIErr(w, &apiError{http.StatusBadRequest, "unknown_provider",
				"no login provider named " + p + " is enabled on this deployment"})
			return
		}
	}
	// ★ One domain, one tenant (docs/61 §61.9.8). The resolution rule ("lowest slug
	// wins") exists for rows that predate this check; the check exists so nobody
	// has to rely on it. Rejecting on save is the only place a human is present to
	// read the reason.
	rules, err := a.mgr.store.ListTenantLoginRules(r.Context())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	for _, other := range rules {
		if other.ID == t.ID {
			continue
		}
		for _, d := range autoJoin {
			for _, od := range other.AutoJoinDomains {
				if d == od {
					writeAPIErr(w, &apiError{http.StatusConflict, "auto_join_conflict",
						"domain " + d + " is already an auto-join domain of tenant " + other.Slug})
					return
				}
			}
		}
	}

	if err := a.mgr.store.SetTenantLogin(r.Context(), t.ID, joinCSV(provs), joinCSV(autoJoin), joinCSV(allowed), joinCSV(hidden)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// These rules ARE the entry gate; a cached copy would keep letting people in.
	a.mgr.tenantLogin.invalidate()
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.login_rules", Target: t.Slug,
		Detail: "providers=" + joinCSV(provs) + " auto_join=" + joinCSV(autoJoin) +
			" allowed=" + joinCSV(allowed) + " hidden=" + joinCSV(hidden),
		At: nowTS(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant": t.Slug, "allowed_providers": joinCSV(provs),
		"auto_join_domains": joinCSV(autoJoin), "allowed_domains": joinCSV(allowed),
		"hidden_providers": joinCSV(hidden),
	})
}

// listMembers (GET /api/admin/tenants/{slug}/members).
func (a adminAPI) listMembers(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	members, err := a.mgr.store.ListMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Removed members stay on the ADMIN roster (and only here) so the rest of the
	// offboarding sequence — stop the workspace, wipe the home — is still
	// reachable after access has been revoked (docs/61 §61.10.6).
	removed, err := a.mgr.store.ListRemovedMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(members)+len(removed))
	add := func(list []MemberInfo, status string) {
		for _, m := range list {
			container, state := a.mgr.workspaceStateByMembership(r.Context(), m.MembershipID)
			row := map[string]any{
				"user_key": m.UserKey, "email": m.Email, "role": m.MemberRole,
				"super_admin": m.IdentityRole == "super_admin",
				"container":   container, "state": state,
				"status": status,
			}
			if ul, ok, _ := a.mgr.store.GetUserLimit(r.Context(), m.MembershipID); ok {
				row["max_sessions"] = ul.MaxSessions
				row["mem_limit"] = ul.MemLimit
				row["cpu_limit"] = ul.CPULimit
				row["disk_gb"] = ul.DiskGB
			}
			out = append(out, row)
		}
	}
	add(members, "active")
	add(removed, "removed")
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t.Slug, "members": out})
}

// stopWorkspace (POST /api/admin/stop-workspace {tenant_slug,user_key}).
func (a adminAPI) stopWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	_, t, ok := a.tenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	ident, err := a.mgr.store.UpsertIdentity(r.Context(), "", body.UserKey, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := a.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if err := a.mgr.stopWorkspaceByMembership(r.Context(), mem.ID); err != nil {
		if errors.Is(err, errSessionShareOwnerBusy) {
			writeAPIErr(w, workspaceLifecycleLeaseError(err))
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": body.UserKey, "tenant": t.Slug})
}

// cleanHome (POST /api/admin/clean-home {tenant_slug,user_key}) wipes a
// member's workspace home except auth/connection state. Same target resolution as
// stop-workspace; the container is stopped first.
//
// ★ tenant_admin, not super_admin (docs/61 §61.10.6 + 決定 26). The offboarding
// sequence is "deactivate the membership → stop the workspace → wipe the home",
// and the department is who knows that somebody left. Leaving only this last step
// with the operator meant every leaver became a ticket to IT. The gate is
// tenantAdminFor, exactly as stopWorkspace already does it, so a tenant_admin can
// only reach their OWN members' homes.
//
// ★ This widens a permission, so it is audited: who wiped whose home in which
// tenant, always.
func (a adminAPI) cleanHome(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.tenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	ident, err := a.mgr.store.UpsertIdentity(r.Context(), "", body.UserKey, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := a.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if err := a.mgr.cleanHomeByMembership(r.Context(), mem.ID); err != nil {
		if errors.Is(err, errSessionShareOwnerBusy) {
			writeAPIErr(w, workspaceLifecycleLeaseError(err))
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "workspace.clean_home", Target: ident.UserKey, At: nowTS(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"cleaned": body.UserKey, "tenant": t.Slug})
}

// destroyWorkspace (DELETE /api/admin/workspaces {tenant_slug,user_key}) is the
// irreversible one: it deletes the home and every per-membership resource the runtime
// created, then the DB row. ADR 0045 決定 13-2.
//
// ★ It is a SEPARATE operation from removing the membership on purpose. Offboarding is a
// logical delete that keeps the home so a returning member is just a re-invite
// (docs/61 §61.10.6); destroying is the second, deliberate step you take later — when the
// EBS volume behind a long-gone member is still being billed for. Doing both at once is
// possible (removeMembership's purge flag) but never the default.
//
// ★ Only an INACTIVE membership can be destroyed. In the admin UI this operation sits one
// misclick away from a member who is at their desk, and there is no undo.
//
// ★ It overrides the deletion locks of ADR 0028 and cannot do otherwise: the locks live
// inside the home, which is unreadable while the workspace is stopped
// (docs/64 §64.18.1). The Console has to say so.
//
// tenant_admin (their own tenant) or super_admin — the same gate as clean-home, which is
// already "destroy this person's work" in every sense except the billing.
func (a adminAPI) destroyWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.tenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	ident, found, err := a.mgr.store.GetIdentityByUserKey(r.Context(), body.UserKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	mem, ok, err := a.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if mem.Status == "active" {
		writeAPIErr(w, &apiError{http.StatusConflict, "membership_active",
			"remove the membership first; an active member's workspace cannot be destroyed"})
		return
	}
	leftovers, err := a.mgr.destroyWorkspaceByMembership(r.Context(), mem.ID)
	if err != nil {
		if errors.Is(err, errSessionShareOwnerBusy) {
			writeAPIErr(w, workspaceLifecycleLeaseError(err))
			return
		}
		writeAPIErr(w, internalErr(err))
		return
	}
	writeAuditDestroy(r, a.mgr.store, t.ID, caller.ID, ident.UserKey, leftovers)
	writeJSON(w, http.StatusOK, map[string]any{
		"destroyed": ident.UserKey, "tenant": t.Slug, "leftovers": leftovers,
	})
}

// writeAuditDestroy records who destroyed whose workspace, and — the part that matters —
// what could NOT be deleted. On Fargate the EFS directories survive their access points
// and keep billing (docs/64 §64.18.4); if that only ever appeared in an HTTP response
// nobody would ever find it again.
func writeAuditDestroy(r *http.Request, store Store, tenantID, actorID, userKey string, leftovers []string) {
	detail := "workspace destroyed (home and runtime resources deleted)"
	if len(leftovers) > 0 {
		detail += "; NOT deleted: " + strings.Join(leftovers, ", ")
	}
	_ = store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: tenantID, ActorKind: "user", ActorID: actorID,
		Action: "workspace.destroy", Target: userKey, Detail: detail, At: nowTS(),
	})
}

// createTenant (POST /api/admin/tenants {slug,name}).
func (a adminAPI) createTenant(w http.ResponseWriter, r *http.Request, _ Identity) {
	var body struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	slug := sanitizeUser(body.Slug)
	if slug == "" || slug == "default" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid slug"})
		return
	}
	if t, ok, err := a.mgr.store.GetTenantBySlug(r.Context(), slug); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	} else if ok {
		writeJSON(w, http.StatusOK, map[string]any{"slug": t.Slug, "name": t.Name, "existed": true})
		return
	}
	name := body.Name
	if name == "" {
		name = slug
	}
	t, err := a.mgr.store.CreateTenant(r.Context(), slug, name)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": t.Slug, "name": t.Name})
}

// addMembership (POST /api/admin/memberships {email|user_key, tenant_slug, role}).
// Pre-creates the target identity if needed (invite-by-key/email).
func (a adminAPI) addMembership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email      string `json:"email"`
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.tenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	key := body.UserKey
	if key == "" {
		key = sanitizeUser(body.Email)
	}
	if key == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "email or user_key required"})
		return
	}
	// Only a super_admin may mint a tenant_admin (privilege escalation); a
	// tenant_admin adding members can only add plain members.
	role := "member"
	if body.Role == "tenant_admin" && caller.Role == "super_admin" {
		role = "tenant_admin"
	}
	// ★ allowed_domains is a guard on THIS call and nowhere else (docs/61 §61.9.5).
	// It stops a tenant_admin from putting an address outside their department's
	// domain on the roster. It is deliberately not re-checked per request: doing so
	// would lock out the contractor somebody invited on purpose, which would then
	// need an exception list — a second roster, which is what this design avoids.
	if aerr := a.checkInviteDomain(r, t, body.Email, key); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	ident, err := a.mgr.store.UpsertIdentity(r.Context(), body.Email, key, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, err := a.mgr.store.EnsureMembership(r.Context(), ident.ID, t.ID, role)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// ★ Re-inviting somebody who was removed puts them back. EnsureMembership
	// deliberately does not reactivate (it also serves the auto-provisioning paths,
	// where that would undo an offboarding on the person's next visit) — so an
	// invite, which IS an explicit decision, does it here.
	if mem.Status != "active" {
		if err := a.mgr.store.SetMembershipStatus(r.Context(), mem.ID, "active"); err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
	}
	// Being on a roster is an entry-gate term now (docs/61 §61.9.6) — an invited
	// person must be able to sign in immediately, not after the cache expires.
	a.mgr.tenantLogin.invalidate()
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "membership.add", Target: ident.UserKey, Detail: "role=" + role, At: nowTS(),
	})
	writeJSON(w, http.StatusOK, map[string]any{"user_key": ident.UserKey, "tenant": t.Slug, "role": role})
}

// checkInviteDomain applies tenant.allowed_domains to an invite. The address is
// the one being invited, or — when the invite names only a user_key — the one
// already on that identity. An invite with no address at all cannot be checked, so
// a tenant that set a guard refuses it rather than letting it through unexamined.
func (a adminAPI) checkInviteDomain(r *http.Request, t Tenant, email, key string) *apiError {
	domains := splitDomainCSV(t.AllowedDomains)
	if len(domains) == 0 {
		return nil
	}
	if email == "" {
		if ident, ok, err := a.mgr.store.GetIdentityByUserKey(r.Context(), key); err == nil && ok {
			email = ident.Email
		}
	}
	if email == "" {
		return &apiError{http.StatusBadRequest, "email_required",
			"tenant " + t.Slug + " restricts invites to " + joinCSV(domains) + "; invite by email address"}
	}
	if !domainMatches(domains, email) {
		return &apiError{http.StatusForbidden, "domain_not_allowed",
			"tenant " + t.Slug + " only accepts members from: " + joinCSV(domains)}
	}
	return nil
}

// removeMembership (DELETE /api/admin/memberships {tenant_slug,user_key}) takes
// somebody off a tenant's roster — the transfer/leaver operation docs/61 §61.10.6
// found missing, and the one that P3 makes load-bearing: once the roster is also
// the entry gate (§61.9.6), being unable to remove a row means being unable to
// offboard at all. A signed session cookie is valid for up to AF_SESSION_TTL
// (7 days by default) and cannot be revoked individually, so without this the
// person keeps their access for a week after they leave (決定 22/27).
//
// The delete is LOGICAL (status='inactive'): the workspace, its home and its
// encrypted secrets survive, and every resolution path already requires an active
// membership, so access stops on the very next request. Deleting the row outright
// would orphan the schedules, audit entries and shares that reference it.
// Reinstating is just re-inviting — EnsureMembership reactivates.
//
// tenant_admin (their own tenant) or super_admin, matching who owns the roster.
func (a adminAPI) removeMembership(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
		// Purge additionally destroys the workspace — home, and every per-membership
		// resource the runtime created (ADR 0045 決定 13-2). Default false: the logical
		// delete above is what offboarding means, and this is a second, irreversible
		// thing that happens to be convenient to do in the same click. It runs AFTER
		// the membership is deactivated, so a failure here still leaves the person
		// locked out.
		Purge bool `json:"purge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	caller, t, ok := a.tenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	ident, found, err := a.mgr.store.GetIdentityByUserKey(r.Context(), body.UserKey)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	// ★ Not yourself. Removing your own last admin membership from the UI is an
	// easy misclick with no undo from inside the product — the remaining path
	// would be another admin, or the host's env.
	if ident.ID == caller.ID {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "self_removal",
			"you cannot remove your own membership; ask another administrator"})
		return
	}
	mem, ok, err := a.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if err := a.mgr.store.SetMembershipStatus(r.Context(), mem.ID, "inactive"); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Drop the cached runtime as well as the login caches: the workspace stays on
	// disk, but nothing should keep serving it from memory for this membership.
	a.mgr.evictMembershipCache(mem.ID)
	a.mgr.tenantLogin.invalidate()
	detail := "status=inactive (workspace and home kept)"
	var leftovers []string
	if body.Purge {
		leftovers, err = a.mgr.destroyWorkspaceByMembership(r.Context(), mem.ID)
		if err != nil {
			// The membership IS deactivated at this point — say so rather than
			// returning a bare 500 that reads as "nothing happened".
			_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
				ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
				Action: "membership.remove", Target: ident.UserKey,
				Detail: "status=inactive; purge FAILED: " + err.Error(), At: nowTS(),
			})
			writeAPIErr(w, &apiError{http.StatusInternalServerError, "purge_failed",
				"the membership was deactivated but the workspace could not be destroyed: " + err.Error()})
			return
		}
		detail = "status=inactive; workspace destroyed (purge)"
		if len(leftovers) > 0 {
			detail += "; NOT deleted: " + strings.Join(leftovers, ", ")
		}
	}
	_ = a.mgr.store.InsertAudit(r.Context(), AuditLog{
		ID: newID(), TenantID: t.ID, ActorKind: "user", ActorID: caller.ID,
		Action: "membership.remove", Target: ident.UserKey,
		Detail: detail, At: nowTS(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"removed": ident.UserKey, "tenant": t.Slug,
		"purged": body.Purge, "leftovers": leftovers,
	})
}

// setTenantLimits (PUT /api/admin/tenants/{slug}/limits) — docs/16 P3-4.
func (a adminAPI) setTenantLimits(w http.ResponseWriter, r *http.Request, _ Identity) {
	var body struct {
		MaxWorkspaces int   `json:"max_workspaces"`
		MaxSessions   int   `json:"max_sessions"`
		MaxGitRepos   int   `json:"max_git_repos"` // internal git repo cap (P2); 0 = unlimited
		MaxLFSBytes   int64 `json:"max_lfs_bytes"` // internal git LFS byte cap (P3); 0 = unlimited
		// Per-workspace RAM cap in BYTES (roadmap P3-4); 0 = no tenant cap. Bounds a
		// tenant_admin's per-user mem_limit at container start.
		MaxWorkspaceMem int64 `json:"max_workspace_mem"`
		// Per-workspace CPU (Fargate units) and working disk (GiB) ceilings; 0 = no cap.
		MaxWorkspaceCPU    int `json:"max_workspace_cpu"`
		MaxWorkspaceDiskGB int `json:"max_workspace_disk_gb"`
		// P3-9 idle-stop: duration strings ("30m"); "" => deployment default,
		// "0" => disabled for this tenant.
		SessionIdleTimeout string `json:"session_idle_timeout"`
		WSIdleTimeout      string `json:"ws_idle_timeout"`
		// Operator gate for member CLI self-update (claude/opencode/codex).
		AllowAgentSelfUpdate         bool `json:"allow_agent_self_update"`
		TerminalHistoryRetentionDays int  `json:"terminal_history_retention_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	if d := body.TerminalHistoryRetentionDays; d != 0 && d != 1 && d != 7 && d != 30 {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_retention", "terminal history retention must be 0, 1, 7, or 30 days"})
		return
	}
	// Reject unparseable durations up front (empty stays empty = use default).
	for _, v := range []string{body.SessionIdleTimeout, body.WSIdleTimeout} {
		if v != "" {
			if _, err := time.ParseDuration(v); err != nil {
				writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_duration", "invalid idle timeout: " + v})
				return
			}
		}
	}
	t, ok, err := a.mgr.store.GetTenantBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	lj, _ := json.Marshal(tenantLimits{
		MaxWorkspaces:                body.MaxWorkspaces,
		MaxSessions:                  body.MaxSessions,
		MaxGitRepos:                  body.MaxGitRepos,
		MaxLFSBytes:                  body.MaxLFSBytes,
		MaxWorkspaceMem:              body.MaxWorkspaceMem,
		MaxWorkspaceCPU:              body.MaxWorkspaceCPU,
		MaxWorkspaceDiskGB:           body.MaxWorkspaceDiskGB,
		SessionIdleTimeout:           body.SessionIdleTimeout,
		WSIdleTimeout:                body.WSIdleTimeout,
		AllowAgentSelfUpdate:         body.AllowAgentSelfUpdate,
		TerminalHistoryRetentionDays: body.TerminalHistoryRetentionDays,
	})
	if err := a.mgr.store.SetTenantLimits(r.Context(), t.ID, string(lj)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Rebuild cached runtimes for this tenant so the new gate reaches the next
	// container start (the gate is injected as env when the runtime is built).
	a.mgr.evictTenantCache(t.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant": t.Slug, "max_workspaces": body.MaxWorkspaces, "max_sessions": body.MaxSessions,
		"max_workspace_mem":     body.MaxWorkspaceMem,
		"max_workspace_cpu":     body.MaxWorkspaceCPU,
		"max_workspace_disk_gb": body.MaxWorkspaceDiskGB,
		"session_idle_timeout":  body.SessionIdleTimeout, "ws_idle_timeout": body.WSIdleTimeout,
		"allow_agent_self_update":         body.AllowAgentSelfUpdate,
		"terminal_history_retention_days": body.TerminalHistoryRetentionDays,
	})
}

// setUserLimit (PUT /api/admin/user-limits) — per-membership override.
func (a adminAPI) setUserLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email       string `json:"email"`
		UserKey     string `json:"user_key"`
		TenantSlug  string `json:"tenant_slug"`
		MaxSessions int    `json:"max_sessions"`
		DiskGB      int    `json:"disk_gb"`
		// MemLimit is the per-workspace RAM cap in BYTES (0 = unset → deployment
		// default). Clamped to the tenant cap + host ceiling at container start; the
		// response echoes the effective (post-clamp) value so the admin sees it.
		MemLimit int64 `json:"mem_limit"`
		// CPULimit is the per-workspace CPU cap in Fargate CPU units (1024 = 1 vCPU),
		// independent of MemLimit. 0 = unset (ADR 0044 決定 1).
		CPULimit int `json:"cpu_limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	_, t, ok := a.tenantAdminFor(w, r, body.TenantSlug)
	if !ok {
		return
	}
	key := body.UserKey
	if key == "" {
		key = sanitizeUser(body.Email)
	}
	if key == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "email or user_key required"})
		return
	}
	ident, err := a.mgr.store.UpsertIdentity(r.Context(), "", key, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := a.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "user is not a member of " + t.Slug})
		return
	}
	q := UserQuota{MaxSessions: body.MaxSessions, DiskGB: body.DiskGB, MemLimit: body.MemLimit, CPULimit: body.CPULimit}
	if err := a.mgr.store.PutUserLimit(r.Context(), mem.ID, q); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// The size axes feed the built runtime (docker --memory/--cpus, ECS task size +
	// ephemeral storage), so drop the cached runtime — the new values reach the next
	// container start. Also compute the effective post-clamp values so the caller sees
	// what will actually be applied rather than what they typed.
	a.mgr.evictMembershipCache(mem.ID)
	effMem, effCPU, effDisk := a.mgr.resolveWorkspaceSize(r.Context(), Workspace{MembershipID: mem.ID, TenantID: t.ID})
	writeJSON(w, http.StatusOK, map[string]any{
		"user_key": key, "tenant": t.Slug, "max_sessions": body.MaxSessions, "disk_gb": body.DiskGB,
		"mem_limit": body.MemLimit, "mem_effective": effMem,
		"cpu_limit": body.CPULimit, "cpu_effective": effCPU, "disk_effective": effDisk,
	})
}

// setMembershipRole (PUT /api/admin/membership-role
// {tenant_slug, user_key, role}) grants or revokes a member's tenant-scoped admin
// role (member | tenant_admin). super_admin only: minting a tenant_admin is a
// privilege escalation kept to the deployment operator (a tenant_admin cannot
// promote others). Deployment-wide super_admin stays env-only (SUPER_ADMIN_EMAILS).
func (a adminAPI) setMembershipRole(w http.ResponseWriter, r *http.Request, _ Identity) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
		Role       string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	role := "member"
	if body.Role == "tenant_admin" {
		role = "tenant_admin"
	}
	t, ok, err := a.mgr.store.GetTenantBySlug(r.Context(), body.TenantSlug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	ident, err := a.mgr.store.UpsertIdentity(r.Context(), "", body.UserKey, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := a.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member of " + t.Slug})
		return
	}
	if err := a.mgr.store.SetMembershipRole(r.Context(), mem.ID, role); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_key": body.UserKey, "tenant": t.Slug, "role": role})
}

// poolStatus (GET /api/admin/ec2-pool) reports the EC2 slot pool: how many boxes are
// provisioned, which are asleep, whose home is on which one, what is hibernating, and
// whether the golden snapshot still matches the running image (docs/64 §64.18.6).
//
// super_admin only. This is deployment infrastructure — slots are shared across tenants,
// so there is no view of it that belongs to one tenant_admin.
//
// On every other runtime profile it answers {"runtime": ...} with no pool, and the
// Console hides the screen. Reporting an empty pool instead would read as "your slots all
// vanished" on a Fargate deployment.
func (a adminAPI) poolStatus(w http.ResponseWriter, r *http.Request, _ Identity) {
	st, ok, err := a.mgr.poolStatus(r.Context())
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"runtime": "other"})
		return
	}
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}
