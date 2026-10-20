// tenant_external_memory.go — the tenant-wide switch for AF memory reached from outside Agent
// Fleet through the CP's /mcp endpoint (#2318, ADR 0108).
//
// It sits above the member's own "use AF memory from outside Agent Fleet" setting: with the
// switch off, /mcp neither lists nor runs the memory tools for anyone in the tenant, whatever
// their own setting says. It belongs to the tenant_admin because the data it guards is the
// tenant's members' and it reaches nothing outside the tenant.
package tenantsrv

import (
	"encoding/json"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// tenantExternalMemoryWire is the GET and PUT body of /api/admin/tenants/{slug}/external-memory,
// read by the Console's `TenantExternalMemoryView`.
type tenantExternalMemoryWire struct {
	Tenant  string `json:"tenant"`
	Enabled bool   `json:"enabled"`
}

// TenantExternalMemory (GET /api/admin/tenants/{slug}/external-memory) returns the switch.
func (a Admin) TenantExternalMemory(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	on, err := a.cp.Store().GetTenantExternalMemory(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantExternalMemoryWire{Tenant: t.Slug, Enabled: on})
}

// SetTenantExternalMemory (PUT /api/admin/tenants/{slug}/external-memory) stores it and audits
// the change. The body must carry `enabled`: a missing key must not read as "off".
func (a Admin) SetTenantExternalMemory(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "enabled (true or false) is required"})
		return
	}
	if err := a.cp.Store().SetTenantExternalMemory(r.Context(), t.ID, *body.Enabled); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	detail := "enabled=false"
	if *body.Enabled {
		detail = "enabled=true"
	}
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.external_memory", Target: t.Slug, Detail: detail, At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, tenantExternalMemoryWire{Tenant: t.Slug, Enabled: *body.Enabled})
}
