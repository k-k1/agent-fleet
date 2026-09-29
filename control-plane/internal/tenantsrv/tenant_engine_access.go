// tenant_engine_access.go — the tenant_admin's per-member grant of the self-hosted engine
// roles (#1215).
//
// Two layers, and this file owns only the lower one. A super_admin decides whether the
// tenant may use a role at all (allow_engine_llm / allow_engine_image on the limits
// endpoint, ADR 0084 decision 7). Under that, a tenant_admin either leaves a role open to
// every member (the default) or restricts it to the members ticked here. The lower layer
// can only narrow the upper one, so a tenant_admin can never grant what the tenant lacks.
package tenantsrv

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// TenantEngineAccess (GET /api/admin/tenants/{slug}/engine-access).
func (a Admin) TenantEngineAccess(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	acc, err := a.cp.Store().GetEngineAccess(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	members, err := a.cp.Store().ListMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	lim := a.tenantLimitsFor(r, t.ID)
	out := tenantEngineAccessWire{
		Tenant: t.Slug,
		Roles: []engineAccessRoleWire{
			{Role: store.EngineAccessLLM, TenantAllowed: lim.AllowEngineLLM == nil || *lim.AllowEngineLLM,
				MembersOnly: acc.MembersOnly[store.EngineAccessLLM]},
			{Role: store.EngineAccessImage, TenantAllowed: lim.AllowEngineImage == nil || *lim.AllowEngineImage,
				MembersOnly: acc.MembersOnly[store.EngineAccessImage]},
		},
		Members: make([]engineAccessMemberWire, 0, len(members)),
	}
	for _, m := range members {
		grants := []string{}
		for _, role := range []string{store.EngineAccessLLM, store.EngineAccessImage} {
			if acc.Grants[m.MembershipID][role] {
				grants = append(grants, role)
			}
		}
		out.Members = append(out.Members, engineAccessMemberWire{
			MembershipID: m.MembershipID, UserKey: m.UserKey, Email: m.Email,
			Role: m.MemberRole, Grants: grants,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// SetTenantEngineAccess (PUT /api/admin/tenants/{slug}/engine-access {role, members_only}).
func (a Admin) SetTenantEngineAccess(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		Role        string `json:"role"`
		MembersOnly bool   `json:"members_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	if !store.ValidEngineAccessRole(body.Role) {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_role", "role must be llm or image"})
		return
	}
	if err := a.cp.Store().SetEngineMembersOnly(r.Context(), t.ID, body.Role, body.MembersOnly); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// Restricting a role takes it away from every member not on the list, so the running
	// workspaces have to re-read their catalogue now rather than after the Agent's
	// ten-minute cache (ADR 0084 decision 9).
	a.cp.PushEngineCatalogChanged(r.Context(), t.ID)
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.engine_access", Target: t.Slug,
		Detail: body.Role + " members_only=" + strconv.FormatBool(body.MembersOnly), At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, engineAccessRoleSavedWire{Tenant: t.Slug, Role: body.Role, MembersOnly: body.MembersOnly})
}

// SetMemberEngineAccess (PUT /api/admin/tenants/{slug}/engine-access/members
// {membership_id, role, granted}).
func (a Admin) SetMemberEngineAccess(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		MembershipID string `json:"membership_id"`
		Role         string `json:"role"`
		Granted      bool   `json:"granted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid json"})
		return
	}
	if !store.ValidEngineAccessRole(body.Role) {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_role", "role must be llm or image"})
		return
	}
	// The membership id comes from the client, so it is checked against THIS tenant's
	// roster: a tenant_admin must not be able to write a grant row onto another tenant's seat.
	members, err := a.cp.Store().ListMembersByTenant(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	var target *store.MemberInfo
	for i := range members {
		if members[i].MembershipID == body.MembershipID {
			target = &members[i]
			break
		}
	}
	if target == nil {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_member", "no such member in this tenant"})
		return
	}
	if err := a.cp.Store().SetEngineGrant(r.Context(), t.ID, target.MembershipID, body.Role, body.Granted); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.cp.PushEngineCatalogChanged(r.Context(), t.ID)
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "member.engine_access", Target: target.UserKey,
		Detail: body.Role + " granted=" + strconv.FormatBool(body.Granted), At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, engineAccessGrantSavedWire{MembershipID: target.MembershipID, Role: body.Role, Granted: body.Granted})
}

// tenantEngineAccessWire is the GET /api/admin/tenants/{slug}/engine-access response, read by
// the Console's `EngineAccessView` (console/src/features/settings/tenant/tenantEngineAccess.tsx).
type tenantEngineAccessWire struct {
	Tenant  string                   `json:"tenant"`
	Roles   []engineAccessRoleWire   `json:"roles"`
	Members []engineAccessMemberWire `json:"members"`
}

// engineAccessRoleWire is one role's two layers. tenant_allowed is the super_admin's gate:
// when it is false nothing on this screen can give the role back, and the screen says so.
type engineAccessRoleWire struct {
	Role          string `json:"role"`
	TenantAllowed bool   `json:"tenant_allowed"`
	MembersOnly   bool   `json:"members_only"`
}

// engineAccessMemberWire is one active member and the roles ticked for them. grants is
// always an array, never null, so the Console can test membership without a guard.
type engineAccessMemberWire struct {
	MembershipID string   `json:"membership_id"`
	UserKey      string   `json:"user_key"`
	Email        string   `json:"email"`
	Role         string   `json:"role"`
	Grants       []string `json:"grants"`
}

// engineAccessRoleSavedWire echoes a saved PUT /api/admin/tenants/{slug}/engine-access.
type engineAccessRoleSavedWire struct {
	Tenant      string `json:"tenant"`
	Role        string `json:"role"`
	MembersOnly bool   `json:"members_only"`
}

// engineAccessGrantSavedWire echoes a saved PUT /api/admin/tenants/{slug}/engine-access/members.
type engineAccessGrantSavedWire struct {
	MembershipID string `json:"membership_id"`
	Role         string `json:"role"`
	Granted      bool   `json:"granted"`
}
