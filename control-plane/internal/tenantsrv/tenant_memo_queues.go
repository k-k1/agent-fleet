// tenant_memo_queues.go — the tenant-wide switch for memo queues (ADR 0137, #4156).
//
// With it off (the default) no member of the tenant can make a memo category a queue. It is
// turned on once every Control Plane of the deployment runs a binary that has the queue rules:
// an older binary's token face and /mcp would flush, delete or rename a queue's messages
// without them, and with the switch off no queue row exists for it to reach. Turning it off
// later stops new queues and leaves the existing ones, and their rules, as they are.
package tenantsrv

import (
	"encoding/json"
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// tenantMemoQueuesWire is the GET and PUT body of /api/admin/tenants/{slug}/memo-queues.
type tenantMemoQueuesWire struct {
	Tenant  string `json:"tenant"`
	Enabled bool   `json:"enabled"`
}

// TenantMemoQueues (GET /api/admin/tenants/{slug}/memo-queues) returns the switch.
func (a Admin) TenantMemoQueues(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.cp.TenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	on, err := a.cp.Store().GetTenantMemoQueues(r.Context(), t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantMemoQueuesWire{Tenant: t.Slug, Enabled: on})
}

// SetTenantMemoQueues (PUT /api/admin/tenants/{slug}/memo-queues) stores it and audits the
// change. The body must carry `enabled`: a missing key must not read as "off".
func (a Admin) SetTenantMemoQueues(w http.ResponseWriter, r *http.Request) {
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
	if err := a.cp.Store().SetTenantMemoQueues(r.Context(), t.ID, *body.Enabled); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	detail := "enabled=false"
	if *body.Enabled {
		detail = "enabled=true"
	}
	_ = a.cp.Store().InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.memo_queues", Target: t.Slug, Detail: detail, At: store.NowTS(),
	})
	writeJSON(w, http.StatusOK, tenantMemoQueuesWire{Tenant: t.Slug, Enabled: *body.Enabled})
}
