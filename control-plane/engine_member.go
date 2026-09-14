package main

// engine_member.go — what a plain member (no engine role at all) may see of the fleet's
// self-hosted engines (ADR 0084 P0-A): the `engines` stream on /api/events, its REST fallback
// GET /api/engines/status, and the CP gateway's own in-flight counter (decision 6-A).
//
// The row is built WITHOUT a single ECS call of its own. engineController already calls
// c.eng.view every tick to decide whether to start or stop the engine; this reads that same
// observation back (engineController.memberSnapshot) rather than asking ECS again. Asking again
// here would multiply DescribeServices by the number of open tabs instead of by the number of
// engines — engine_ecs.go's engineViewTTL note already recorded this mistake once, for TTS.

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineMemberAPI serves the REST fallback (ADR 0084 decision 1): a CP too old to have the
// `engines` SSE stream answers /api/events with 404 (events.go), and this is what the Console
// falls back to polling in that window. Behind etagJSON like every other JSON GET, so an
// unchanged answer costs a 304 rather than a body.
type engineMemberAPI struct {
	memberAuth
	reg *engineRegistry
}

// registerEngineMemberRoutes wires the fallback. Called unconditionally, like the admin panel's
// routes next door (registerEngineAdminRoutes) and for the same reason: a deployment with no
// engines at all still has to be able to say so, rather than 404 on the one route that would
// tell a client that.
func registerEngineMemberRoutes(mux *http.ServeMux, cfg config, reg *engineRegistry) {
	a := engineMemberAPI{memberAuth{cfg.mgr}, reg}
	mux.HandleFunc("GET /api/engines/status", a.withMembership(a.status))
}

func (a engineMemberAPI) status(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	lim := tenantEngineLimitsFor(r.Context(), a.mgr, mv.TenantID)
	writeJSON(w, http.StatusOK, enginesMemberPayload(r.Context(), a.reg, lim))
}

// engineMemberFields is what a member may see of one engine row (ADR 0084 decision 3): the
// admin/tenant_admin row, trimmed to the keys nobody who cannot press a button needs. Reused
// through pickKeys exactly the way engineTenantAdminRow is (engine_ingest_perm.go) — a third
// hand-written copy would be a second list that has to be kept in step with this one, and
// pickKeys' own contract (a key absent from the source stays absent) is what makes decision 4's
// "say nothing" possible without a special case here.
var engineMemberFields = []string{"key", "api", "state", "warm", "stop_eta", "idle_secs", "lifecycle", "queue"}

// engineMemberRow trims one source row (memberSourceRow's output) to what every member may see.
func engineMemberRow(full map[string]any) map[string]any {
	return pickKeys(full, engineMemberFields)
}

// memberSourceRow is engineMemberRow's input. It answers ok=false for the two cases decision 5
// says must not produce a row at all — the pill has nothing to draw a control for, and "switched
// off" and "not offered" are the same fact to somebody who cannot press either button:
//
//   - the engine is switched off (mode == off);
//   - nothing is enabled to serve (the catalogue has no model), which is the same refusal the
//     gateway itself gives (engine_unavailable).
//
// Tenant permission (decision 7) is NOT checked here — that is P0-C's gate, layered on top of
// this stream rather than built into it (see the ADR's フェーズ note on why A lands before C).
func (e *engineRuntimeState) memberSourceRow(ctx context.Context) (map[string]any, bool) {
	mode := e.mode(ctx)
	if mode == engineModeOff {
		return nil, false
	}
	if !e.catalog.hasModels(ctx) {
		return nil, false
	}
	row := map[string]any{
		"key":   e.def.Key,
		"api":   e.def.api(),
		"warm":  e.warm(ctx),
		"queue": e.queueRow(),
	}
	if e.def.notManagedHere() {
		// Decision 4: a row this deployment does not manage carries no state, no idle window and
		// no stop time — the same omission engine_admin.go's row() makes and for the same reason,
		// nothing here started it, so nothing here may say when it will stop.
		row["lifecycle"] = e.def.lifecycle()
		return row, true
	}
	cfg := e.controlCfg()
	row["idle_secs"] = int(engineIdleWindow(cfg).Seconds())
	// The controller's OWN last tick, never a fresh e.ecs.view(ctx) here — see
	// engineController.memberSnapshot for why a member request must not pay a second
	// DescribeServices next to the controller's own.
	if state, desired, ok := e.ctrl.memberSnapshot(); ok {
		row["state"] = engineDisplayState(state, mode)
		if eta := engineStopETA(mode, desired >= 1, e.demandAt(ctx), cfg); !eta.IsZero() {
			row["stop_eta"] = eta.UTC().Format(time.RFC3339)
		}
	}
	return row, true
}

// queueRow is decision 6-A, P0's whole answer to "how many are waiting": the CP gateway's own
// in-flight count for this row (A). B (the box's own /queue) and C (a member's own pending
// batch) are P1. The wire shape is pinned in decision 6 (commit e68fece6, after the ADR briefly
// wrote it two ways — nested here, flat `queue_counted_secs` in the ⚠️ paragraph):
//
//	queue: { count: number, counted_secs?: number }
//
// counted_secs rides ONLY while this process cannot yet vouch for the whole count — the same
// honesty engine_admin.go's window_counted_secs carries for the demand window, so a CP
// mid-rolling-deployment does not draw a confident 0 while the OTHER process still holds half
// the traffic. It disappears once that grace period has passed rather than settling on a large
// steady number: a field that counted up forever would be exactly the self-ticking value
// decision 2 forbids.
func (e *engineRuntimeState) queueRow() map[string]any {
	row := map[string]any{"count": e.inflight.count()}
	if secs, ok := e.inflight.countedSecs(); ok {
		row["counted_secs"] = secs
	}
	return row
}

// enginesMemberPayload is the whole of what a member sees of the fleet's engines: the
// `engines` stream on /api/events and the GET /api/engines/status fallback both build their
// body from this one function (ADR 0084 decision 1). nil-safe on reg — a deployment with no
// engines at all answers an empty list rather than 404, the same shape as every other one.
//
// One row per PROVIDER, never folded by role (decision 11): an `image` role with a LAN box and a
// borrowed one produces two entries here, each with its own queue count. Folding those into one
// pill is the Console's job, not this function's — collapsing them here would throw away exactly
// the per-row truth decision 11's popover needs.
//
// lim is the CALLER's tenant limits (ADR 0084 decision 7), read once per call — not once per
// row here — so both callers (the events tick and the REST fallback) pay exactly one tenant
// lookup per invocation, cached or not, rather than this loop multiplying it by the row count.
func enginesMemberPayload(ctx context.Context, reg *engineRegistry, lim tenantLimits) map[string]any {
	out := []map[string]any{}
	for _, e := range reg.list() {
		// ADR 0084 decision 7/8, gate 4: a role this tenant was denied gets no row, the same
		// "do not offer and then refuse" rule gate 1 applies to the catalogue (decision 5).
		if !lim.engineRoleAllowed(e.def.api()) {
			continue
		}
		full, ok := e.memberSourceRow(ctx)
		if !ok {
			continue
		}
		out = append(out, engineMemberRow(full))
	}
	return map[string]any{"engines": out}
}

// --- gate 4's tenant-limits cache (ADR 0084 decision 8) -----------------------------------
//
// engine_gateway.go's tenantLimitsFor reads GetTenant on every call with no cache at all,
// which is fine for its three callers — each is one request, spending one store read to
// authorize it. This is a different shape: the events stream calls its equivalent once per
// SUBSCRIBER every 4 seconds for as long as a tab stays open, so reading GetTenant directly
// here would turn "how many tabs are open" into "how many tenant reads per 4 seconds" — the
// exact multiplication decision 3 forbade for e.ecs.view() (a per-subscriber, per-tick cache
// miss multiplying one DescribeServices into one per open tab).
//
// The cache is keyed by TENANT, not by subscriber: two tabs open on the same tenant share one
// read. And it is a short TTL, not a read-once-per-connection cache — the latter was
// considered and rejected, because a grant revoked mid-connection would then never reach an
// already-open tab; the tenant would have to close and reopen it to see the pill disappear.
// invalidateTenantEngineLimits (called from SetTenantLimits, tenant_wiring.go, right next to
// the Agent-side push decision 9 already does) drops a tenant's entry the moment a super_admin
// saves, so in practice a denial reaches an open tab on its very next tick — the TTL below is
// only the fallback bound for whatever calls tenantEngineLimitsFor WITHOUT going through that
// invalidation (there is none today; it exists so a future caller cannot regress to "stale
// until reconnect" by skipping the eviction).
var tenantEngineLimitsCache sync.Map // tenantID (string) -> *tenantEngineLimitsEntry

// tenantEngineLimitsTTL matches engineViewTTL's order of magnitude (engine_ecs.go): short
// enough that a missed invalidation still clears within about one events tick.
const tenantEngineLimitsTTL = 3 * time.Second

type tenantEngineLimitsEntry struct {
	mu  sync.Mutex
	at  time.Time
	lim tenantLimits
}

// tenantEngineLimitsFor is gate 4's read, cached as described above. A read failure or a nil
// store keeps whatever was cached before (zero value on a first failure, which resolves every
// role as allowed) rather than treating the error as a denial — this stream is informational
// only, and the request-time gates (engine_gateway.go) enforce access independently of what a
// tab happens to be showing.
func tenantEngineLimitsFor(ctx context.Context, mgr *manager, tenantID string) tenantLimits {
	if tenantID == "" {
		return tenantLimits{}
	}
	v, _ := tenantEngineLimitsCache.LoadOrStore(tenantID, &tenantEngineLimitsEntry{})
	e := v.(*tenantEngineLimitsEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.at.IsZero() && time.Since(e.at) < tenantEngineLimitsTTL {
		return e.lim
	}
	if mgr == nil || mgr.store == nil {
		return e.lim
	}
	t, err := mgr.store.GetTenant(ctx, tenantID)
	if err != nil {
		return e.lim
	}
	e.lim, e.at = parseLimits(t.Limits), time.Now()
	return e.lim
}

// invalidateTenantEngineLimits drops one tenant's cached entry, so the very next tick after a
// save reads the fresh row instead of waiting out tenantEngineLimitsTTL.
func invalidateTenantEngineLimits(tenantID string) {
	tenantEngineLimitsCache.Delete(tenantID)
}

// --- decision 6-A: the CP gateway's own in-flight count -----------------------------------

// engineInFlightGrace is how long a freshly started process's count carries `counted_secs`
// before the field is dropped (decision 6). Not pinned to a specific number in the ADR; chosen
// comfortably longer than an ECS rolling deployment's own drain window (30-ingress's ALB
// deregistration delay), which is the situation the field exists to flag — two CP processes
// briefly splitting one row's traffic.
const engineInFlightGrace = 60 * time.Second

// engineInFlight is "how many requests is the CP gateway holding open for this row right now",
// counted in the gateway's own memory (⚠️ decision 6): a CP replaced mid-rolling-deployment
// starts this back at 0, which under-counts for as long as the old process still holds the other
// half of the traffic. countedSecs is how a reader tells that window apart from "nobody is
// waiting" — the same honesty engine_admin.go's window_counted_secs already carries for the
// demand window, but shaped to disappear (see engineInFlightGrace) rather than tick forever.
type engineInFlight struct {
	n     int64
	since time.Time // when THIS PROCESS started counting
}

func newEngineInFlight() *engineInFlight {
	return &engineInFlight{since: time.Now()}
}

// begin/end bracket one held request. nil-safe like engineDemand's methods, for the test
// helpers across this package that build a row by hand without wiring one.
func (f *engineInFlight) begin() {
	if f != nil {
		atomic.AddInt64(&f.n, 1)
	}
}

func (f *engineInFlight) end() {
	if f != nil {
		atomic.AddInt64(&f.n, -1)
	}
}

func (f *engineInFlight) count() int {
	if f == nil {
		return 0
	}
	return int(atomic.LoadInt64(&f.n))
}

// countedSecs is how long this process has been counting, and ok is false once that has run
// long enough to be trusted (engineInFlightGrace) — the point at which decision 6 says the
// field must disappear rather than settle on a large, permanently climbing number.
func (f *engineInFlight) countedSecs() (secs int, ok bool) {
	if f == nil || f.since.IsZero() {
		return 0, false
	}
	d := time.Since(f.since)
	if d >= engineInFlightGrace {
		return 0, false
	}
	return int(d.Seconds()), true
}
