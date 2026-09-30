package main

import (
	"net/http"
	"strconv"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// beginIrreversible records the request for an action that cannot be undone before it runs
// (store.BeginIrreversible), and answers 503 audit_unavailable when that record cannot be
// written. The caller acts only on ok, and closes the intent exactly once, directly or
// through refuseIrreversible. internal/tenantsrv has the same pair for its admin routes.
func beginIrreversible(w http.ResponseWriter, r *http.Request, st store.AuditStore, e store.AuditLog) (*store.AuditIntent, bool) {
	in, err := store.BeginIrreversible(r.Context(), st, e)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusServiceUnavailable, errCodeAuditUnavailable, "nothing was done: " + err.Error()})
		return nil, false
	}
	return in, true
}

// refuseIrreversible answers e and records it as the outcome of in. The detail does not
// claim nothing happened: an internal error can come from halfway through the action.
func refuseIrreversible(w http.ResponseWriter, r *http.Request, in *store.AuditIntent, e *apiError) {
	in.Done(r.Context(), "error "+e.code+": "+e.message, e.status)
	writeAPIErr(w, e)
}

// Audit log read side (docs/log/20 M1). The write side lives in proxy.go (auditActionTarget
// + InsertAudit) and mcp.go/ssm.go. This exposes the ledger to operators; there is no
// read path anywhere else yet.

// audit (GET /api/admin/audit?tenant=<slug>&limit=N) serves the most recent
// audit entries. super_admin sees the whole deployment; a tenant_admin is scoped to a
// tenant they administer (via ?tenant=), enforced by tenantScope. Entries are
// enriched best-effort with the tenant slug and the actor's email for display.
func (a adminAPI) audit(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := a.tenantScope(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	rows, err := a.mgr.store.ListAuditByTenant(ctx, tenantID, limit)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// tenant id -> slug (one query), and a small actor id -> email cache (best-effort).
	slug := map[string]string{}
	if ts, e := a.mgr.store.ListTenants(ctx); e == nil {
		for _, t := range ts {
			slug[t.ID] = t.Slug
		}
	}
	email := map[string]string{}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		em, seen := email[row.ActorID]
		if !seen && row.ActorID != "" {
			if id, found, _ := a.mgr.store.GetIdentityByID(ctx, row.ActorID); found {
				em = id.Email
			}
			email[row.ActorID] = em
		}
		out = append(out, map[string]any{
			"id":          row.ID,
			"tenant":      slug[row.TenantID],
			"tenant_id":   row.TenantID,
			"actor_kind":  row.ActorKind,
			"actor_id":    row.ActorID,
			"actor_email": em,
			"action":      row.Action,
			"target":      row.Target,
			"detail":      row.Detail,
			"http_status": row.HTTPStatus,
			"at":          row.At,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": out})
}
