// tts_wake.go — "call Zundamon": the explicit start trigger (ADR 0070 decision 4).
//
// The automatic trigger only fires once 2,000 characters of intent have piled up inside
// five minutes, which is the right rule for "this deployment is being read to" and the
// wrong one for one person who wants the voice for their own notifications: a few dozen
// characters at a time never crosses it, and making them earn the engine by volume is
// ceremony. So any logged-in member can ask for it directly.
//
// Three properties, and each of them is a way this could go wrong instead:
//
//   - it spends money, so every press is audited. A bill nobody can explain is a bill
//     nobody trusts (the same reason the controller audits its own starts);
//   - it is idempotent: while the engine is running or starting, a press only refreshes
//     the demand clock. Pressing it four times must not queue four starts;
//   - it is rate limited per member, because the button is in front of somebody watching
//     a 70-second cold start and there is nothing else to do while waiting.
package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The rate limit. A cold start is 70-77 s measured, so three presses inside five minutes
// is already more than impatience needs, and the fourth would only buy another
// UpdateService call and another audit row.
const (
	ttsWakeWindow = 5 * time.Minute
	ttsWakeBurst  = 3
	// Above this many tracked members the window map is swept. Wake is a rare call, so
	// this is about a long-lived process rather than about load.
	ttsWakeKeysMax = 10_000
)

type ttsWakeAPI struct {
	memberAuth
	status ttsStatusView
	eng    *ttsEngineECS  // nil = nothing here can start an engine
	ctrl   *ttsController // nil = no controller (then nothing stops it either)
	demand *ttsDemand     // nil where there is no engine to want

	mu   sync.Mutex
	seen map[string]ttsWakeCount
	now  func() time.Time // test seam
}

type ttsWakeCount struct {
	at time.Time
	n  int
}

func (a *ttsWakeAPI) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// allow is a fixed window per member, in the shape session_share.go's read limiter uses.
func (a *ttsWakeAPI) allow(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock()
	if a.seen == nil {
		a.seen = map[string]ttsWakeCount{}
	}
	if len(a.seen) > ttsWakeKeysMax {
		for k, c := range a.seen {
			if now.Sub(c.at) >= ttsWakeWindow {
				delete(a.seen, k)
			}
		}
	}
	win := a.seen[key]
	if win.at.IsZero() || now.Sub(win.at) >= ttsWakeWindow {
		win = ttsWakeCount{at: now}
	}
	if win.n >= ttsWakeBurst {
		a.seen[key] = win
		return false
	}
	win.n++
	a.seen[key] = win
	return true
}

// post (POST /api/tts/wake) starts the engine, or refreshes the demand clock when it is
// already coming. It answers with the same body as GET /api/tts/status plus "started",
// so the screen that pressed it shows the new state without a second round trip.
func (a *ttsWakeAPI) post(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	if a.eng == nil {
		// Not a failure of this request: the engine's lifecycle belongs to whoever runs it
		// (a standing dev docker), or there is no engine at all.
		writeAPIErr(w, &apiError{http.StatusNotImplemented, "tts_engine_unmanaged",
			"the VOICEVOX engine is not managed by this deployment; nothing here can start it"})
		return
	}
	mode := a.status.mode(r.Context())
	if mode == ttsModeOff {
		// An administrator turned speech off. A member asking for the voice cannot
		// overrule that, and starting an engine whose output routing is off would buy a
		// task nobody can hear.
		writeAPIErr(w, &apiError{http.StatusConflict, "tts_engine_off",
			"speech is switched off for this deployment"})
		return
	}
	if !a.allow(ident.ID) {
		writeAPIErr(w, &apiError{http.StatusTooManyRequests, "tts_wake_rate_limited",
			"the engine has already been called; it takes about 70 seconds to arrive"})
		return
	}
	view, err := a.eng.view(r.Context())
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, "tts_engine_error", "ecs describe failed: " + err.Error()})
		return
	}
	// Stamped whatever the state, and before the start rather than after: pressing the
	// button is somebody saying they are listening, and that is exactly what has to keep
	// the idle window from closing under them while the engine is still on its way.
	a.demand.stamp(r.Context())

	started := false
	if view.state != "none" && view.desired < 1 {
		// A cooldown is there to stop an automatic retry loop, never to refuse a person
		// who pressed a button — the same rule the admin toggle follows.
		a.ctrl.noteAdminAction()
		if err := a.eng.setEnabled(r.Context(), true); err != nil {
			writeAPIErr(w, &apiError{http.StatusBadGateway, "tts_engine_error", "ecs update failed: " + err.Error()})
			return
		}
		started = true
	}
	a.audit(r.Context(), ident, started, view.state)

	body := a.status.body(r.Context())
	body["started"] = started
	writeJSON(w, http.StatusOK, body)
}

// audit records the press. Target distinguishes the press that spent money from the one
// that only said "still listening", because that is the question anyone reading the
// ledger next to a Fargate charge is actually asking.
func (a *ttsWakeAPI) audit(ctx context.Context, ident store.Identity, started bool, state string) {
	if a.mgr == nil || a.mgr.store == nil {
		return
	}
	target := "demand"
	if started {
		target = "start"
	}
	_ = a.mgr.store.InsertAudit(context.WithoutCancel(ctx), store.AuditLog{
		ID: store.NewID(), TenantID: "", ActorKind: "user", ActorID: ident.ID,
		Action: "tts.engine.wake", Target: target, Detail: "engine " + state, At: store.NowTS(),
	})
}
