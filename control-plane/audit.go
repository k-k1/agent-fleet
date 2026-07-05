package main

import (
	"net/http"
	"strconv"
)

// Audit log read side (docs/20 M1). The write side lives in proxy.go (auditActionTarget
// + InsertAudit) and mcp.go/ssm.go. This exposes the ledger to operators; there is no
// read path anywhere else yet.

// handleAdminAudit (GET /api/admin/audit?tenant=<slug>&limit=N) serves the most recent
// audit entries. super_admin sees the whole deployment; a tenant_admin is scoped to a
// tenant they administer (via ?tenant=), enforced by adminTenantScope. Entries are
// enriched best-effort with the tenant slug and the actor's email for display.
func (c config) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := c.adminTenantScope(w, r)
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
	rows, err := c.mgr.store.ListAuditByTenant(ctx, tenantID, limit)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	// tenant id -> slug (one query), and a small actor id -> email cache (best-effort).
	slug := map[string]string{}
	if ts, e := c.mgr.store.ListTenants(ctx); e == nil {
		for _, t := range ts {
			slug[t.ID] = t.Slug
		}
	}
	email := map[string]string{}
	out := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		em, seen := email[a.ActorID]
		if !seen && a.ActorID != "" {
			if id, found, _ := c.mgr.store.GetIdentityByID(ctx, a.ActorID); found {
				em = id.Email
			}
			email[a.ActorID] = em
		}
		out = append(out, map[string]any{
			"id":          a.ID,
			"tenant":      slug[a.TenantID],
			"tenant_id":   a.TenantID,
			"actor_kind":  a.ActorKind,
			"actor_id":    a.ActorID,
			"actor_email": em,
			"action":      a.Action,
			"target":      a.Target,
			"detail":      a.Detail,
			"at":          a.At,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": out})
}
