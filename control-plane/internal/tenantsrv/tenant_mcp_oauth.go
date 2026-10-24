package tenantsrv

import (
	"encoding/json"
	"net/http"
	"time"
)

// tenantMCPOAuthWire is the policy and deployment capability read by the Console's
// console/src/features/settings/tenant/tenantMCPOAuth.tsx.
type tenantMCPOAuthWire struct {
	Enabled bool `json:"enabled"`
	Served  bool `json:"served"`
}

// TenantMCPOAuth returns policy to the tenant admin or a deployment admin.
func (a Admin) TenantMCPOAuth(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	enabled, err := a.cp.Store().GetTenantMCPOAuth(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantMCPOAuthWire{enabled, a.cp.MCPOAuthServed()})
}

// SetTenantMCPOAuth changes policy and audits revocations in the same transaction.
func (a Admin) SetTenantMCPOAuth(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Enabled == nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "enabled must be a boolean"})
		return
	}
	if err := a.cp.Store().SetTenantMCPOAuth(r.Context(), t.ID, *body.Enabled, ident.ID, time.Now().Unix()); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantMCPOAuthWire{*body.Enabled, a.cp.MCPOAuthServed()})
}
