package memoafter

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// "Satisfied", one delivery state at a time: step 1 (a clean close with a good first turn) and
// step 2 (the PR snapshot). A first turn that is never recorded is satisfied by a merged PR alone.
func TestDeliveryState(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	closed := func(ago time.Duration) string { return store.DeliveryTS(now.Add(-ago)) }
	good := store.MemoDelivery{State: store.DeliveryClosed, Outcome: store.DeliveryClean, ClosedAt: closed(time.Hour),
		TurnOutcome: "clean", TurnMs: 5_000, TurnEndedAt: "2026-11-19T10:00:00Z", PRState: store.PRStateNone}
	with := func(f func(*store.MemoDelivery)) store.MemoDelivery { d := good; f(&d); return d }
	noTurn := func(d *store.MemoDelivery) { d.TurnEndedAt, d.TurnOutcome = "", "" }
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
		{"failed turn", with(func(d *store.MemoDelivery) { d.TurnOutcome = "turn-failed" }), true, Failed, WhyBadFirstTurn},
		{"quick turn of a quiet kind", with(func(d *store.MemoDelivery) { d.TurnOutcome = "" }), true, Failed, WhyBadFirstTurn},
		{"a bad turn with a merged PR", with(func(d *store.MemoDelivery) { d.TurnOutcome, d.PRState = "turn-failed", store.PRStateMerged }), true, Failed, WhyBadFirstTurn},
		// Step 2.
		{"good, no snapshot yet", with(func(d *store.MemoDelivery) { d.PRState = "" }), true, Waiting, WhyPR},
		{"good, a PR open", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateWaiting }), true, Waiting, WhyPR},
		{"good, a PR closed", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateClosed }), true, Failed, WhyPRClosed},
		{"good, session gone", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateGone }), true, Failed, WhySessionGone},
		{"good, merged", with(func(d *store.MemoDelivery) { d.PRState = store.PRStateMerged }), true, Satisfied, ""},
		{"good, no PR", good, true, Satisfied, ""},
		// A first turn not recorded.
		{"no turn, no snapshot", with(func(d *store.MemoDelivery) { noTurn(d); d.PRState = "" }), true, Waiting, WhyPR},
		{"no turn, no PR yet in the window", with(noTurn), true, Waiting, WhyFirstTurn},
		{"no turn, no PR after the window", with(func(d *store.MemoDelivery) { noTurn(d); d.ClosedAt = closed(25 * time.Hour) }), true, Failed, WhyNoPR},
		{"no turn, merged, in the window", with(func(d *store.MemoDelivery) { noTurn(d); d.PRState = store.PRStateMerged }), true, Satisfied, ""},
		{"no turn, a PR open after the window", with(func(d *store.MemoDelivery) {
			noTurn(d)
			d.PRState, d.ClosedAt = store.PRStateWaiting, closed(30*time.Hour)
		}), true, Waiting, WhyPR},
	} {
		state, reason := DeliveryState(c.d, c.found, now)
		if state != c.state || reason != c.reason {
			t.Errorf("%s: %s/%s, want %s/%s", c.name, state, reason, c.state, c.reason)
		}
	}
}

// A snapshot sums up to none, closed, merged or waiting, and an unknown state never reads as merged.
func TestSnapshotState(t *testing.T) {
	for _, c := range []struct {
		prs  []PR
		want string
	}{
		{nil, store.PRStateNone},
		{[]PR{{State: "merged"}}, store.PRStateMerged},
		{[]PR{{State: "merged"}, {State: "open"}}, store.PRStateWaiting},
		{[]PR{{State: "merged"}, {State: "draft"}}, store.PRStateWaiting},
		{[]PR{{State: "merged"}, {State: ""}}, store.PRStateWaiting},
		{[]PR{{State: "open"}, {State: "closed"}}, store.PRStateClosed},
	} {
		if got := SnapshotState(c.prs); got != c.want {
			t.Errorf("%+v: %s, want %s", c.prs, got, c.want)
		}
	}
	if EncodeSnapshot(nil) != "[]" {
		t.Errorf("no PR encodes as %q", EncodeSnapshot(nil))
	}
}
