package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func writeAPIErr(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.status, map[string]any{
		"error": map[string]string{"code": e.code, "message": e.message},
	})
}

// handleTenants (GET /api/tenants) returns the caller's memberships for the
// Console tenant picker (docs/14 P3-2). Single-membership users get one entry,
// so the Console can auto-select and hide the picker.
func (c config) handleTenants(w http.ResponseWriter, r *http.Request) {
	ident, aerr := c.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	ms, aerr := c.mgr.membershipsFor(r.Context(), ident)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		// allow_agent_self_update: the operator gate, surfaced so the Console can
		// show/hide the member's "keep CLIs updated" toggle for this tenant.
		allowUpd := false
		if t, err := c.mgr.store.GetTenant(r.Context(), m.TenantID); err == nil {
			allowUpd = parseLimits(t.Limits).AllowAgentSelfUpdate
		}
		out = append(out, map[string]any{
			"slug": m.TenantSlug, "name": m.TenantName, "role": m.Role,
			"allow_agent_self_update": allowUpd,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out, "super_admin": ident.Role == "super_admin"})
}

// requireSuperAdmin gates the minimal admin API (full UI is P3-5).
func (c config) requireSuperAdmin(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	ident, aerr := c.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return Identity{}, false
	}
	if ident.Role != "super_admin" {
		writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "super_admin required"})
		return Identity{}, false
	}
	return ident, true
}

// requireTenantAdmin gates a per-tenant admin endpoint: allows a deployment
// super_admin (any tenant) or a tenant_admin of `slug`. Resolves and returns the
// caller's identity and the target tenant. Writes 401/403/404 and returns ok=false
// on failure. slug comes from the path on some routes and the body on others, so
// it is passed explicitly.
func (c config) requireTenantAdmin(w http.ResponseWriter, r *http.Request, slug string) (Identity, Tenant, bool) {
	ident, aerr := c.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return Identity{}, Tenant{}, false
	}
	t, ok, err := c.mgr.store.GetTenantBySlug(r.Context(), slug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return Identity{}, Tenant{}, false
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return Identity{}, Tenant{}, false
	}
	if !c.mgr.tenantAdminFor(r.Context(), ident, t.ID) {
		writeAPIErr(w, &apiError{http.StatusForbidden, "forbidden", "tenant admin required"})
		return Identity{}, Tenant{}, false
	}
	return ident, t, true
}

// handleAdminListTenants (GET /api/admin/tenants) — overview for the admin UI.
// super_admin sees every tenant; a tenant_admin sees only the tenants they
// administer. The super_admin flag lets the Console hide deployment-wide controls
// (create tenant, tenant quotas, clean-home, role grants) for tenant_admins.
func (c config) handleAdminListTenants(w http.ResponseWriter, r *http.Request) {
	ident, aerr := c.mgr.identityFor(r.Context(), r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	isSuper := ident.Role == "super_admin"

	var tenants []Tenant
	if isSuper {
		ts, err := c.mgr.store.ListTenants(r.Context())
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		tenants = ts
	} else {
		ms, err := c.mgr.store.ListMemberships(r.Context(), ident.ID)
		if err != nil {
			writeAPIErr(w, internalErr(err))
			return
		}
		for _, m := range ms {
			if m.Role != "tenant_admin" {
				continue
			}
			if t, err := c.mgr.store.GetTenant(r.Context(), m.TenantID); err == nil {
				tenants = append(tenants, t)
			}
		}
	}

	out := make([]map[string]any, 0, len(tenants))
	for _, t := range tenants {
		members, _ := c.mgr.store.ListMembersByTenant(r.Context(), t.ID)
		running, _ := c.mgr.countRunningInTenant(r.Context(), t.ID)
		lim := parseLimits(t.Limits)
		out = append(out, map[string]any{
			"slug": t.Slug, "name": t.Name, "status": t.Status, "isolation": t.Isolation,
			"users": len(members), "running": running,
			"max_workspaces": lim.MaxWorkspaces, "max_sessions": lim.MaxSessions,
			"max_git_repos":        lim.MaxGitRepos,
			"max_lfs_bytes":        lim.MaxLFSBytes,
			"session_idle_timeout": lim.SessionIdleTimeout, "ws_idle_timeout": lim.WSIdleTimeout,
			"allow_agent_self_update": lim.AllowAgentSelfUpdate,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out, "super_admin": isSuper})
}

// handleAdminListMembers (GET /api/admin/tenants/{slug}/members).
func (c config) handleAdminListMembers(w http.ResponseWriter, r *http.Request) {
	_, t, ok := c.requireTenantAdmin(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	members, err := c.mgr.store.ListMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		container, state := c.mgr.workspaceStateByMembership(r.Context(), m.MembershipID)
		row := map[string]any{
			"user_key": m.UserKey, "email": m.Email, "role": m.MemberRole,
			"super_admin": m.IdentityRole == "super_admin",
			"container":   container, "state": state,
		}
		if ul, ok, _ := c.mgr.store.GetUserLimit(r.Context(), m.MembershipID); ok {
			row["max_sessions"] = ul.MaxSessions
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t.Slug, "members": out})
}

// handleAdminStopWorkspace (POST /api/admin/stop-workspace {tenant_slug,user_key}).
func (c config) handleAdminStopWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	_, t, ok := c.requireTenantAdmin(w, r, body.TenantSlug)
	if !ok {
		return
	}
	ident, err := c.mgr.store.UpsertIdentity(r.Context(), "", body.UserKey, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := c.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if err := c.mgr.stopWorkspaceByMembership(r.Context(), mem.ID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": body.UserKey, "tenant": t.Slug})
}

// handleAdminCleanHome (POST /api/admin/clean-home {tenant_slug,user_key}) wipes a
// member's workspace home except auth/connection state. Same target resolution as
// stop-workspace; the container is stopped first.
func (c config) handleAdminCleanHome(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
	var body struct {
		UserKey    string `json:"user_key"`
		TenantSlug string `json:"tenant_slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	t, ok, err := c.mgr.store.GetTenantBySlug(r.Context(), body.TenantSlug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	ident, err := c.mgr.store.UpsertIdentity(r.Context(), "", body.UserKey, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := c.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member"})
		return
	}
	if err := c.mgr.cleanHomeByMembership(r.Context(), mem.ID); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleaned": body.UserKey, "tenant": t.Slug})
}

// handleAdminCreateTenant (POST /api/admin/tenants {slug,name}).
func (c config) handleAdminCreateTenant(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
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
	if t, ok, err := c.mgr.store.GetTenantBySlug(r.Context(), slug); err != nil {
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
	t, err := c.mgr.store.CreateTenant(r.Context(), slug, name)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": t.Slug, "name": t.Name})
}

// handleAdminAddMembership (POST /api/admin/memberships {email|user_key, tenant_slug, role}).
// Pre-creates the target identity if needed (invite-by-key/email).
func (c config) handleAdminAddMembership(w http.ResponseWriter, r *http.Request) {
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
	caller, t, ok := c.requireTenantAdmin(w, r, body.TenantSlug)
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
	ident, err := c.mgr.store.UpsertIdentity(r.Context(), body.Email, key, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if _, err := c.mgr.store.EnsureMembership(r.Context(), ident.ID, t.ID, role); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_key": key, "tenant": t.Slug, "role": role})
}

// handleAdminSetTenantLimits (PUT /api/admin/tenants/{slug}/limits) — docs/16 P3-4.
func (c config) handleAdminSetTenantLimits(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
	var body struct {
		MaxWorkspaces int   `json:"max_workspaces"`
		MaxSessions   int   `json:"max_sessions"`
		MaxGitRepos   int   `json:"max_git_repos"` // internal git repo cap (P2); 0 = unlimited
		MaxLFSBytes   int64 `json:"max_lfs_bytes"` // internal git LFS byte cap (P3); 0 = unlimited
		// P3-9 idle-stop: duration strings ("30m"); "" => deployment default,
		// "0" => disabled for this tenant.
		SessionIdleTimeout string `json:"session_idle_timeout"`
		WSIdleTimeout      string `json:"ws_idle_timeout"`
		// Operator gate for member CLI self-update (claude/opencode/codex).
		AllowAgentSelfUpdate bool `json:"allow_agent_self_update"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
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
	t, ok, err := c.mgr.store.GetTenantBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	lj, _ := json.Marshal(tenantLimits{
		MaxWorkspaces:        body.MaxWorkspaces,
		MaxSessions:          body.MaxSessions,
		MaxGitRepos:          body.MaxGitRepos,
		MaxLFSBytes:          body.MaxLFSBytes,
		SessionIdleTimeout:   body.SessionIdleTimeout,
		WSIdleTimeout:        body.WSIdleTimeout,
		AllowAgentSelfUpdate: body.AllowAgentSelfUpdate,
	})
	if err := c.mgr.store.SetTenantLimits(r.Context(), t.ID, string(lj)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Rebuild cached runtimes for this tenant so the new gate reaches the next
	// container start (the gate is injected as env when the runtime is built).
	c.mgr.evictTenantCache(t.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant": t.Slug, "max_workspaces": body.MaxWorkspaces, "max_sessions": body.MaxSessions,
		"session_idle_timeout": body.SessionIdleTimeout, "ws_idle_timeout": body.WSIdleTimeout,
		"allow_agent_self_update": body.AllowAgentSelfUpdate,
	})
}

// handleAdminSetUserLimit (PUT /api/admin/user-limits) — per-membership override.
func (c config) handleAdminSetUserLimit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email       string `json:"email"`
		UserKey     string `json:"user_key"`
		TenantSlug  string `json:"tenant_slug"`
		MaxSessions int    `json:"max_sessions"`
		DiskGB      int    `json:"disk_gb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	_, t, ok := c.requireTenantAdmin(w, r, body.TenantSlug)
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
	ident, err := c.mgr.store.UpsertIdentity(r.Context(), "", key, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := c.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "user is not a member of " + t.Slug})
		return
	}
	if err := c.mgr.store.PutUserLimit(r.Context(), mem.ID, body.MaxSessions, body.DiskGB); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_key": key, "tenant": t.Slug, "max_sessions": body.MaxSessions, "disk_gb": body.DiskGB})
}

// handleAdminSetMembershipRole (PUT /api/admin/membership-role
// {tenant_slug, user_key, role}) grants or revokes a member's tenant-scoped admin
// role (member | tenant_admin). super_admin only: minting a tenant_admin is a
// privilege escalation kept to the deployment operator (a tenant_admin cannot
// promote others). Deployment-wide super_admin stays env-only (SUPER_ADMIN_EMAILS).
func (c config) handleAdminSetMembershipRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
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
	t, ok, err := c.mgr.store.GetTenantBySlug(r.Context(), body.TenantSlug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant"})
		return
	}
	ident, err := c.mgr.store.UpsertIdentity(r.Context(), "", body.UserKey, "")
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	mem, ok, err := c.mgr.store.GetMembership(r.Context(), ident.ID, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_membership", "not a member of " + t.Slug})
		return
	}
	if err := c.mgr.store.SetMembershipRole(r.Context(), mem.ID, role); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_key": body.UserKey, "tenant": t.Slug, "role": role})
}
