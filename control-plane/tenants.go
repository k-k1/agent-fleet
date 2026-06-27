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
