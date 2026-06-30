package main

import (
	"encoding/json"
	"net/http"
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
		out = append(out, map[string]any{"slug": m.TenantSlug, "name": m.TenantName, "role": m.Role})
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

// handleAdminListTenants (GET /api/admin/tenants) — overview for the admin UI.
func (c config) handleAdminListTenants(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
	tenants, err := c.mgr.store.ListTenants(r.Context())
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
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
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
}

// handleAdminListMembers (GET /api/admin/tenants/{slug}/members).
func (c config) handleAdminListMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
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
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
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
	key := body.UserKey
	if key == "" {
		key = sanitizeUser(body.Email)
	}
	if key == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "email or user_key required"})
		return
	}
	t, ok, err := c.mgr.store.GetTenantBySlug(r.Context(), body.TenantSlug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant " + body.TenantSlug})
		return
	}
	role := body.Role
	if role != "tenant_admin" {
		role = "member"
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
		MaxWorkspaces int `json:"max_workspaces"`
		MaxSessions   int `json:"max_sessions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
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
	lj, _ := json.Marshal(map[string]int{"max_workspaces": body.MaxWorkspaces, "max_sessions": body.MaxSessions})
	if err := c.mgr.store.SetTenantLimits(r.Context(), t.ID, string(lj)); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": t.Slug, "max_workspaces": body.MaxWorkspaces, "max_sessions": body.MaxSessions})
}

// handleAdminSetUserLimit (PUT /api/admin/user-limits) — per-membership override.
func (c config) handleAdminSetUserLimit(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
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
	key := body.UserKey
	if key == "" {
		key = sanitizeUser(body.Email)
	}
	if key == "" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", "email or user_key required"})
		return
	}
	t, ok, err := c.mgr.store.GetTenantBySlug(r.Context(), body.TenantSlug)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, "no_tenant", "unknown tenant " + body.TenantSlug})
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
