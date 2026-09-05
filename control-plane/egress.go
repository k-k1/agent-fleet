package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Egress observation ingestion + read (docs/log/20 M2, log-only forward proxy).
//
// The proxy (egress_proxy.go) batches destination observations and POSTs them to
// /internal/egress; the CP aggregates them into egress_daily and mirrors the FIRST
// would-block sighting of each host per day into the audit ledger (action=
// egress.observe) so operators see exfil-shaped attempts without the log flooding.
// Nothing is ENFORCED in M1/M2 — this is log-only (docs/log/20 §B.6).

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

// bearerToken extracts a "Bearer <tok>" Authorization value.
func bearerToken(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// tokenOK checks the shared AF_EGRESS_TOKEN bearer in constant time, so the token cannot
// be guessed by timing.
func (a egressAPI) tokenOK(r *http.Request) bool {
	return a.token != "" &&
		subtle.ConstantTimeCompare([]byte(bearerToken(r)), []byte(a.token)) == 1
}

// egressStore is the narrow store view the egress feature needs: observation
// aggregation + allowlist (EgressStore), the admin-action / observe audit rows
// (AuditStore) and the egress_mode setting (SettingsStore).
type egressStore interface {
	store.EgressStore
	store.AuditStore
	store.SettingsStore
}

// egressAPI is the egress observation/allowlist handler set.
// The /internal/* routes (ingest, policy) are authenticated by the shared
// AF_EGRESS_TOKEN bearer — NOT a user session — so they register directly;
// the /api/admin/egress* routes register through withSuperAdmin.
type egressAPI struct {
	memberAuth
	token string // AF_EGRESS_TOKEN ("" = ingestion/policy disabled)
	// proxy is AF_EGRESS_PROXY_ADDR — the ONLY thing that actually constrains a
	// workspace (main.go injects http(s)_proxy into every container when it is set).
	// Empty means this deployment has no egress control at all, which the member-facing
	// check reports as configured=false so the Console does not warn about a restriction
	// that is not there (egress_member.go).
	proxy string
	dedup *egressAuditDedup
	store egressStore
}

func newEgressAPI(m *manager, token, proxy string, dedup *egressAuditDedup) egressAPI {
	return egressAPI{memberAuth{m}, token, proxy, dedup, m.store}
}

// auditAdmin records a deployment-wide admin action (docs/log/20 M3: allowlist/mode edits
// are themselves audited). Best-effort.
func (a egressAPI) auditAdmin(ctx context.Context, ident store.Identity, action, target, detail string) {
	_ = a.store.InsertAudit(ctx, store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: target, Detail: detail, At: store.NowTS(),
	})
}

// effectivePolicy is what the proxy enforces: the built-in product-critical defaults
// (docs/log/20 §B.5) plus every ACTIVE allowlist entry, and the deployment egress mode
// (log-only unless set to enforce).
func (a egressAPI) effectivePolicy(ctx context.Context) (entries []string, enforce bool) {
	entries = append(entries, defaultEgressAllowlist...)
	if extra, err := a.store.EffectiveAllowlist(ctx); err == nil {
		entries = append(entries, extra...)
	}
	mode, _ := a.store.GetSetting(ctx, "egress_mode")
	return entries, mode == "enforce"
}

// policy (GET /internal/egress/policy) serves the effective allowlist +
// mode to the forward proxy, which polls it so admin edits take effect without a proxy
// restart. AF_EGRESS_TOKEN bearer (authGate-exempt), same as ingestion.
func (a egressAPI) policy(w http.ResponseWriter, r *http.Request) {
	if !a.tokenOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	entries, enforce := a.effectivePolicy(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"allowlist": entries, "enforce": enforce})
}

// ingest (POST /internal/egress) receives a batch of observations from the
// egress proxy. Deployment-internal: authenticated by the shared AF_EGRESS_TOKEN bearer
// (not a user session — it is authGate-exempt). Best-effort: a bad row is skipped.
func (a egressAPI) ingest(w http.ResponseWriter, r *http.Request) {
	if !a.tokenOK(r) {
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
		_ = a.store.RecordEgress(ctx, day, host, e.Allowed, e.Count)
		if !e.Allowed && a.dedup != nil && a.dedup.firstToday(day, host) {
			_ = a.store.InsertAudit(ctx, store.AuditLog{
				ID: store.NewID(), TenantID: "", ActorKind: "system", ActorID: "egress-proxy",
				Action: "egress.observe", Target: host, Detail: "would-block (log-only)", At: store.NowTS(),
			})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// stats (GET /api/admin/egress?days=N) serves aggregated egress stats
// (busiest destination hosts with would-allow / would-block split). Attribution is
// deployment-wide in M2, so this is super_admin only.
func (a egressAPI) stats(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	since := time.Now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	rows, err := a.store.ListEgress(r.Context(), since, 500)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		out = append(out, map[string]any{"host": e.Host, "allowed": e.Allowed, "blocked": e.Blocked})
	}
	mode, _ := a.store.GetSetting(r.Context(), "egress_mode")
	if mode == "" {
		mode = "log-only"
	}
	writeJSON(w, http.StatusOK, map[string]any{"egress": out, "days": days, "mode": mode, "enforce": mode == "enforce"})
}

// allowlistList (GET /api/admin/egress/allowlist?state=) lists allowlist
// entries, optionally filtered by state (active | proposed | retired). super_admin only.
func (a egressAPI) allowlistList(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	rows, err := a.store.ListAllowlist(r.Context(), r.URL.Query().Get("state"), 500)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		out = append(out, map[string]any{
			"id": e.ID, "tenant_id": e.TenantID, "entry": e.Entry, "state": e.State,
			"reason": e.Reason, "added_by": e.AddedBy, "added_at": e.AddedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"allowlist": out, "defaults": defaultEgressAllowlist})
}

// allowlistAdd (POST /api/admin/egress/allowlist) adds an ACTIVE entry.
// Body: {entry, reason?, tenant?}. super_admin only; the change is audited.
func (a egressAPI) allowlistAdd(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var b struct{ Entry, Reason, Tenant string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	// Same normalisation and validation as the member request path, so dead entries and
	// whole-TLD openings like ".com" cannot get in through the admin route either.
	entry, aerr := normalizeEgressEntry(b.Entry)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	e := store.AllowlistEntry{
		ID: store.NewID(), TenantID: b.Tenant, Entry: entry, State: "active",
		Reason: b.Reason, AddedBy: ident.Email, AddedAt: store.NowTS(),
	}
	if err := a.store.AddAllowlist(r.Context(), e); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditAdmin(r.Context(), ident, "egress.allow.add", entry, "reason="+b.Reason)
	writeJSON(w, http.StatusOK, map[string]any{"id": e.ID, "entry": e.Entry, "state": e.State})
}

// allowlistState (POST /api/admin/egress/allowlist/{id}/state) transitions
// an entry: approve a proposed one (active), or retire one. Body: {state}. super_admin.
func (a egressAPI) allowlistState(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var b struct{ State string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	if b.State != "active" && b.State != "retired" && b.State != "proposed" {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_state", "state must be active|retired|proposed"})
		return
	}
	id := r.PathValue("id")
	if err := a.store.SetAllowlistState(r.Context(), id, b.State); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditAdmin(r.Context(), ident, "egress.allow."+b.State, id, "")
	w.WriteHeader(http.StatusNoContent)
}

// mode (GET|PUT /api/admin/egress/mode) reads or sets the deployment
// egress mode. PUT body: {enforce:bool}. super_admin only; a change is audited.
func (a egressAPI) mode(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	if r.Method == http.MethodGet {
		mode, _ := a.store.GetSetting(r.Context(), "egress_mode")
		if mode == "" {
			mode = "log-only"
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "enforce": mode == "enforce"})
		return
	}
	var b struct{ Enforce bool }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	mode := "log-only"
	if b.Enforce {
		mode = "enforce"
	}
	if err := a.store.SetSetting(r.Context(), "egress_mode", mode); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	a.auditAdmin(r.Context(), ident, "egress.mode", mode, "")
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "enforce": mode == "enforce"})
}
