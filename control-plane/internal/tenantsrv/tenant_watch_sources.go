// tenant_watch_sources.go — the tenant-wide switch for member-added watch sources (ADR 0133 §8,
// #3631).
//
// With it off (the default) a member cannot mark an MCP server as a watch source and the
// Workspace Agent runs none, whatever the member registered. It belongs to the tenant_admin
// because a source is member-chosen code in the watch engine's path.
package tenantsrv

import (
	"encoding/json"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// tenantWatchSourcesWire is the GET and PUT body of /api/admin/tenants/{slug}/watch-sources,
// read by the Console's `TenantWatchSourcesView`.
type tenantWatchSourcesWire struct {
	Tenant  string `json:"tenant"`
	Enabled bool   `json:"enabled"`
}

// TenantWatchSources (GET /api/admin/tenants/{slug}/watch-sources) returns the switch.
func (a Admin) TenantWatchSources(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	on, err := a.cp.Store().GetTenantWatchSources(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantWatchSourcesWire{Tenant: t.Slug, Enabled: on})
}

// SetTenantWatchSources (PUT /api/admin/tenants/{slug}/watch-sources) stores it and audits
// the change. The body must carry `enabled`: a missing key must not read as "off".
func (a Admin) SetTenantWatchSources(w http.ResponseWriter, r *http.Request) {
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
	if err := a.cp.Store().SetTenantWatchSources(r.Context(), t.ID, *body.Enabled); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	detail := "enabled=false"
	if *body.Enabled {
		detail = "enabled=true"
	}
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.watch_sources", Target: t.Slug, Detail: detail, At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, tenantWatchSourcesWire{Tenant: t.Slug, Enabled: *body.Enabled})
}
