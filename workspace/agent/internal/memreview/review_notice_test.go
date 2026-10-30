package memreview

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A run that leaves proposals in the queue says so once, with counts; a run that queues nothing
// says nothing.
func TestAReviewTellsTheQueueWhenItLeavesProposals(t *testing.T) {
	type call struct {
		run             string
		queued, pending int
	}
	var calls []call
	OnQueued = func(run string, queued, pending int) error {
		calls = append(calls, call{run, queued, pending})
		return nil
	}
	t.Cleanup(func() { OnQueued = nil })

	h := newHarness(t)
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.Queued != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(calls) != 1 || calls[0].run != res.RunID || calls[0].queued != 1 || calls[0].pending != 1 {
		t.Fatalf("calls = %+v", calls)
	}

	// A window with nothing to propose: no call.
	h2 := newHarness(t)
	h2.replies = []func() (string, error){say(`{"ops":[]}`)}
	calls = nil
	if res, err := h2.reviewer().Review(context.Background(), h2.req(TriggerAuto, turns(4))); err != nil || res.Queued != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(calls) != 0 {
		t.Fatalf("calls = %+v", calls)
	}
}

// A stop after the run was saved as done and before its notification was made must not lose the
// notification: the pending mark is in the write that ends the run, and the reconcile finishes it
// once, even after more restarts.
func TestAStopBeforeTheNotificationDoesNotLoseIt(t *testing.T) {
	var calls []string
	OnQueued = func(run string, queued, pending int) error { calls = append(calls, run); return nil }
	t.Cleanup(func() { OnQueued = nil })

	h := newHarness(t)
	if !h.reviewUntilCrash(h.reviewer(), "ended", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no crash at the ended stage")
	}
	if len(calls) != 0 || len(Pending()) != 1 {
		t.Fatalf("setup: calls %v, pending %d", calls, len(Pending()))
	}
	// Positive control: the crashed run is done, so nothing else would announce it.
	rs := runs()
	if len(rs) != 1 || !rs[0].Terminal() || !rs[0].NotifyPending {
		t.Fatalf("runs = %+v", rs)
	}
	if len(rs[0].Ops) != 0 {
		t.Fatalf("a finished run still carries its operations: %+v", rs[0])
	}

	ReconcileNotices(h.now)
	if len(calls) != 1 || calls[0] != rs[0].ID {
		t.Fatalf("calls after the reconcile = %v", calls)
	}
	if runs()[0].NotifyPending {
		t.Fatal("the mark was not cleared")
	}
	ReconcileNotices(h.now)
	if len(calls) != 1 {
		t.Fatalf("announced again: %v", calls)
	}
}

// The notification's outbox failing keeps the mark and retries; a run whose proposals were all
// decided meanwhile announces nothing.
func TestAFailedNotificationIsRetriedAndNothingIsAnnouncedWhenAllWasDecided(t *testing.T) {
	failing := true
	var calls int
	OnQueued = func(string, int, int) error {
		calls++
		if failing {
			return errors.New("outbox full")
		}
		return nil
	}
	t.Cleanup(func() { OnQueued = nil })

	h := newHarness(t)
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !runs()[0].NotifyPending {
		t.Fatalf("calls %d, mark %v", calls, runs()[0].NotifyPending)
	}
	ReconcileNotices(h.now)
	if calls != 2 || !runs()[0].NotifyPending {
		t.Fatalf("calls %d, mark %v", calls, runs()[0].NotifyPending)
	}
	failing = false
	ReconcileNotices(h.now)
	if calls != 3 || runs()[0].NotifyPending {
		t.Fatalf("calls %d, mark %v", calls, runs()[0].NotifyPending)
	}

	// Decided before the notification could be made: nothing to say, and the mark goes.
	h2 := newHarness(t)
	failing = true
	calls = 0
	if _, err := h2.reviewer().Review(context.Background(), h2.req(TriggerAuto, turns(4))); err != nil {
		t.Fatal(err)
	}
	for _, p := range Pending() {
		if err := Reject(p.ID, h2.now); err != nil {
			t.Fatal(err)
		}
	}
	before := calls
	ReconcileNotices(h2.now)
	if calls != before || runs()[0].NotifyPending {
		t.Fatalf("calls %d (was %d), mark %v", calls, before, runs()[0].NotifyPending)
	}
}

// A run whose notification is still owed is the only record of it, so pruning the old runs must not
// take it while one of its proposals waits - however many newer runs there are - and it goes back to
// the ordinary retention once the notification is made or its proposals are all decided.
func TestPruningKeepsARunWhoseNotificationIsOwed(t *testing.T) {
	failing := true
	var announced []string
	OnQueued = func(run string, _, _ int) error {
		if failing {
			return errors.New("outbox full")
		}
		announced = append(announced, run)
		return nil
	}
	t.Cleanup(func() { OnQueued = nil })

	h := newHarness(t)
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.Queued != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	flood := func() {
		for i := 0; i < keepTerminalRuns+20; i++ {
			r := Run{ID: fmt.Sprintf("newer%03d", i), Key: "other", State: StateDone, Last: 1}
			_ = saveRun(&r, h.now.Add(time.Hour+time.Duration(i)*time.Second))
		}
		_ = moveWatermark("other", 1, h.now)
		pruneRuns(h.now)
	}
	flood()
	if _, ok := runStore.Read(res.RunID); !ok {
		t.Fatal("the run that still owes its notification was pruned")
	}
	failing = false
	ReconcileNotices(h.now)
	if len(announced) != 1 || announced[0] != res.RunID {
		t.Fatalf("announced = %v", announced)
	}
	// Made: the run is ordinary history again and goes with the rest.
	flood()
	if _, ok := runStore.Read(res.RunID); ok {
		t.Fatal("a run with nothing owed was kept past the retention limit")
	}

	// Decided before it could be announced: the mark protects nothing.
	failing = true
	h2 := newHarness(t)
	res2, err := h2.reviewer().Review(context.Background(), h2.req(TriggerAuto, turns(4)))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Pending() {
		if err := Reject(p.ID, h2.now); err != nil {
			t.Fatal(err)
		}
	}
	h = h2
	flood()
	if _, ok := runStore.Read(res2.RunID); ok {
		t.Fatal("a run whose proposals were all decided was kept by its mark")
	}
}

// Another run's proposals waiting does not make this run's old notification news.
func TestAnotherRunsProposalsDoNotRenewAnOldNotification(t *testing.T) {
	var announced []string
	OnQueued = func(run string, _, _ int) error { announced = append(announced, run); return nil }
	t.Cleanup(func() { OnQueued = nil })
	queueEnv(t)
	now := time.Now()
	old := Run{ID: "oldrun", Key: "k1", State: StateDone, Queued: 1, NotifyPending: true}
	_ = saveRun(&old, now)
	if _, err := enqueue(prop("a", "n1", "text")); err != nil { // another run's ("run") proposal
		t.Fatal(err)
	}
	ReconcileNotices(now)
	if len(announced) != 0 {
		t.Fatalf("announced = %v", announced)
	}
	if r, _ := runStore.Read("oldrun"); r.NotifyPending {
		t.Fatal("the mark stayed")
	}
}
