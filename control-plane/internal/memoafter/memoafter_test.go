package memoafter

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Step 1 of "satisfied", one delivery state at a time. A first turn that is never recorded stops
// waiting once the collection window is over: without the PR snapshot (step 2) nothing can show a
// success for it, so the dependent goes to the person instead of waiting forever.
func TestDeliveryState(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	closed := func(ago time.Duration) string { return store.DeliveryTS(now.Add(-ago)) }
	good := store.MemoDelivery{State: store.DeliveryClosed, Outcome: store.DeliveryClean, ClosedAt: closed(time.Hour),
		TurnOutcome: "clean", TurnMs: 5_000, TurnEndedAt: "2026-11-19T10:00:00Z"}
	with := func(f func(*store.MemoDelivery)) store.MemoDelivery { d := good; f(&d); return d }
	for _, c := range []struct {
		name          string
		d             store.MemoDelivery
		found         bool
		state, reason string
	}{
		{"gone", store.MemoDelivery{}, false, Failed, WhyUnknown},
		{"open", with(func(d *store.MemoDelivery) { d.State = store.DeliveryOpen }), true, Waiting, WhyRunning},
		{"sending", with(func(d *store.MemoDelivery) { d.State = store.DeliverySending }), true, Waiting, WhyRunning},
		{"aborted", with(func(d *store.MemoDelivery) { d.Outcome = store.DeliveryAborted }), true, Failed, WhyAborted},
		{"unknown", with(func(d *store.MemoDelivery) { d.Outcome = store.DeliveryUnknown }), true, Failed, WhyUnknown},
		{"ended by a person", with(func(d *store.MemoDelivery) { d.Outcome = store.DeliveryEnded }), true, Failed, WhyNotClean},
		{"overdue retire", with(func(d *store.MemoDelivery) { d.OverdueAt, d.RetireGen = closed(2*time.Hour), 4 }), true, Failed, WhyOverdue},
		{"overdue, no retire", with(func(d *store.MemoDelivery) { d.OverdueAt = closed(2 * time.Hour) }), true, Satisfied, ""},
		{"turn not collected yet", with(func(d *store.MemoDelivery) { d.TurnEndedAt, d.TurnOutcome = "", "" }), true, Waiting, WhyFirstTurn},
		{"turn never collected", with(func(d *store.MemoDelivery) { d.TurnEndedAt, d.TurnOutcome, d.ClosedAt = "", "", closed(25*time.Hour) }), true, Failed, WhyNoFirstTurn},
		{"failed turn", with(func(d *store.MemoDelivery) { d.TurnOutcome = "turn-failed" }), true, Failed, WhyBadFirstTurn},
		{"quick turn of a quiet kind", with(func(d *store.MemoDelivery) { d.TurnOutcome = "" }), true, Failed, WhyBadFirstTurn},
		{"good", good, true, Satisfied, ""},
	} {
		state, reason := DeliveryState(c.d, c.found, now)
		if state != c.state || reason != c.reason {
			t.Errorf("%s: %s/%s, want %s/%s", c.name, state, reason, c.state, c.reason)
		}
	}
}
