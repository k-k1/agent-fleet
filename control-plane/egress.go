package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Egress observation ingestion + read (docs/20 M2, log-only forward proxy).
//
// The proxy (egress_proxy.go) batches destination observations and POSTs them to
// /internal/egress; the CP aggregates them into egress_daily and mirrors the FIRST
// would-block sighting of each host per day into the audit ledger (action=
// egress.observe) so operators see exfil-shaped attempts without the log flooding.
// Nothing is ENFORCED in M1/M2 — this is log-only (docs/20 §B.6).

// egressIngest is the wire body the proxy POSTs.
type egressIngest struct {
	Events []egressEvent `json:"events"`
}

type egressEvent struct {
	Host    string `json:"host"`
	Allowed bool   `json:"allowed"`
	Count   int    `json:"count"`
}

// egressAuditDedup collapses would-block audit rows to one per (day, host) so a
// chatty blocked host doesn't flood the ledger. Reset when the day rolls over.
type egressAuditDedup struct {
	mu   sync.Mutex
	day  string
	seen map[string]bool
}

func (d *egressAuditDedup) firstToday(day, host string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.day != day {
		d.day, d.seen = day, map[string]bool{}
	}
	if d.seen[host] {
		return false
	}
	d.seen[host] = true
	return true
}

// handleEgressIngest (POST /internal/egress) receives a batch of observations from the
// egress proxy. Deployment-internal: authenticated by the shared AF_EGRESS_TOKEN bearer
// (not a user session — it is authGate-exempt). Best-effort: a bad row is skipped.
func (c config) handleEgressIngest(w http.ResponseWriter, r *http.Request) {
	if c.egressToken == "" ||
		strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) != c.egressToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body egressIngest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")
	for _, e := range body.Events {
		host := strings.ToLower(strings.TrimSpace(e.Host))
		if host == "" || e.Count <= 0 {
			continue
		}
		_ = c.mgr.store.RecordEgress(ctx, day, host, e.Allowed, e.Count)
		if !e.Allowed && c.egressDedup != nil && c.egressDedup.firstToday(day, host) {
			_ = c.mgr.store.InsertAudit(ctx, AuditLog{
				ID: newID(), TenantID: "", ActorKind: "system", ActorID: "egress-proxy",
				Action: "egress.observe", Target: host, Detail: "would-block (log-only)", At: nowTS(),
			})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminEgress (GET /api/admin/egress?days=N) serves aggregated egress stats
// (busiest destination hosts with would-allow / would-block split). Attribution is
// deployment-wide in M2, so this is super_admin only.
func (c config) handleAdminEgress(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireSuperAdmin(w, r); !ok {
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	since := time.Now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	rows, err := c.mgr.store.ListEgress(r.Context(), since, 500)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		out = append(out, map[string]any{"host": e.Host, "allowed": e.Allowed, "blocked": e.Blocked})
	}
	writeJSON(w, http.StatusOK, map[string]any{"egress": out, "days": days, "log_only": true})
}
