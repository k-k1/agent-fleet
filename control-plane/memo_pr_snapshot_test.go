package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The PR snapshot of a prerequisite's delivery (ADR 0137, addendum 2026-11-19, part A, step 2).

// prChain makes an enabled queue with a prerequisite a, fired and closed clean at closedAt with
// first turn turn ("" for none), and a dependent b waiting for it. The delivery's session is "q-a".
func (e *deliveryEnv) prChain(turn string, closedAt time.Time) (*fakeQueueAgent, string, store.MemoDelivery, memoDTO) {
	e.t.Helper()
	agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 3}, 0)
	a := e.pending("app", "impl", "a")
	rec := e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "b waits for a", a.ID))
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("dependent: %d %s", rec.Code, rec.Body.String())
	}
	b := decode[memoDTO](e.t, rec)
	da := e.fired(q)
	e.closeDeliveryAs(da.ID, store.DeliveryClean, turn, 90_000, closedAt)
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET session='q-a' WHERE id=?`, da.ID); err != nil {
		e.t.Fatal(err)
	}
	return agent, q, e.delivery(da.ID), b
}

func prAnswer(states ...string) map[string]any {
	prs := []map[string]any{}
	for i, s := range states {
		n := 7 + i
		prs = append(prs, map[string]any{"provider": "github", "key": "o/r#" + itoaTest(n), "state": s,
			"url": "https://github.com/o/r/pull/" + itoaTest(n)})
	}
	return map[string]any{"prs": prs, "readAt": "2026-11-19T10:00:00Z"}
}

func itoaTest(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return digits[n : n+1]
	}
	return itoaTest(n/10) + digits[n%10:n%10+1]
}

// The first read names the session and the close; later reads ask by number, at most every 10
// minutes; a dependent waits while a PR is open and fires once every one is merged; a final
// snapshot is never asked again.
func TestMemoPRSnapshotReadsUntilMerged(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.prChain("clean", now.Add(-time.Minute))
		state := "open"
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer(state) })
		e.settleNow(now)
		calls := agent.prCallsSoFar()
		if len(calls) != 1 || calls[0].Session != "q-a" || calls[0].Since != da.ClosedAt || len(calls[0].PRs) != 0 {
			t.Fatalf("first read %+v, want the session and the close", calls)
		}
		got := e.delivery(da.ID)
		if got.PRState != store.PRStateWaiting || got.PRChangedAt == "" {
			t.Fatalf("after an open PR: %+v", got)
		}
		st := e.listed(b.ID).AfterStatus
		if len(st) != 1 || st[0].Reason != memoafter.WhyPR || len(st[0].PRs) != 1 || st[0].PRs[0].Key != "o/r#7" || st[0].PRs[0].State != "open" {
			t.Fatalf("after status %+v, want waiting for PR #7", st)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict {
			t.Fatalf("b fired while a's PR is open: %d %s", rec.Code, rec.Body.String())
		}
		e.settleNow(now.Add(5 * time.Minute))
		if n := len(agent.prCallsSoFar()); n != 1 {
			t.Fatalf("%d reads within 10 minutes", n)
		}
		state = "merged"
		e.settleNow(now.Add(11 * time.Minute))
		calls = agent.prCallsSoFar()
		if len(calls) != 2 || calls[1].Session != "" || len(calls[1].PRs) != 1 || calls[1].PRs[0].Key != "o/r#7" {
			t.Fatalf("second read %+v, want PR #7 by number and no session", calls)
		}
		if got := e.delivery(da.ID); got.PRState != store.PRStateMerged {
			t.Fatalf("after a merge: %+v", got)
		}
		if d := e.fired(q); !sameIDs(d.MessageIDs, b.ID) {
			t.Fatalf("claimed %v, want b", d.MessageIDs)
		}
		e.settleNow(now.Add(30 * time.Minute))
		if n := len(agent.prCallsSoFar()); n != 2 {
			t.Fatalf("a final snapshot was asked again (%d reads)", n)
		}
	})
}

// No answer is never "no PR": an Agent without the route is not asked, and an error, an answer of
// another handler or one this Control Plane cannot read keeps the dependent waiting. A session the
// Agent no longer has, before any snapshot, sends the dependent to held at once; a PR closed
// unmerged does too.
func TestMemoPRSnapshotNoAnswerKeepsWaiting(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.prChain("clean", now.Add(-time.Minute))
		at := now
		step := func(reply func(memoPRSnapshotRequest) (int, any)) {
			t.Helper()
			agent.setPRs(reply != nil, reply)
			at = at.Add(11 * time.Minute)
			e.settleNow(at)
			e.runner().checkAfter(context.Background(), e.queueRow(q), at)
		}
		waiting := func(what string) {
			t.Helper()
			if got := e.delivery(da.ID); got.PRState != "" || got.PRSnapshot != "" {
				t.Fatalf("%s: snapshot %q / %q", what, got.PRState, got.PRSnapshot)
			}
			if got := e.memoRow(b.ID); got.State != store.MemoStatePending {
				t.Fatalf("%s: b is %s", what, got.State)
			}
		}
		step(nil)
		if n := len(agent.prCallsSoFar()); n != 0 {
			t.Fatalf("an Agent without the marker was asked %d times", n)
		}
		waiting("older Agent")
		step(func(memoPRSnapshotRequest) (int, any) {
			return http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": "pr_snapshot_unanswered"}}
		})
		waiting("unanswered")
		step(func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, map[string]any{"readAt": "x"} })
		waiting("no list")
		step(func(memoPRSnapshotRequest) (int, any) {
			return http.StatusOK, map[string]any{"prs": []map[string]any{{"provider": "gitlab", "key": "o/r#1", "state": "merged"}}}
		})
		waiting("another provider")
		step(func(memoPRSnapshotRequest) (int, any) {
			return http.StatusNotFound, map[string]any{"error": map[string]any{"code": agentPRNoSession}}
		})
		if got := e.delivery(da.ID); got.PRState != store.PRStateGone {
			t.Fatalf("session gone: %+v", got)
		}
		got := e.memoRow(b.ID)
		if hr := memoafter.ParseHeldReason(got.HeldReason); got.State != store.MemoStateHeld || hr == nil || hr.Reason != memoafter.WhySessionGone {
			t.Fatalf("b after the session is gone: %+v", got)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.prChain("clean", now.Add(-time.Minute))
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer("merged", "closed") })
		e.settleNow(now)
		e.runner().checkAfter(context.Background(), e.queueRow(q), now)
		if got := e.delivery(da.ID); got.PRState != store.PRStateClosed {
			t.Fatalf("a closed PR: %+v", got)
		}
		got := e.memoRow(b.ID)
		if hr := memoafter.ParseHeldReason(got.HeldReason); got.State != store.MemoStateHeld || hr == nil || hr.Reason != memoafter.WhyPRClosed {
			t.Fatalf("b after a PR closed unmerged: %+v", got)
		}
	})
}

// A first turn that is never recorded: a merged PR satisfies without waiting out the 24 hours, and
// no PR sends the dependent to held once the window is over, never before.
func TestMemoPRSnapshotWithoutAFirstTurn(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.prChain("", now.Add(-time.Minute))
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer("merged") })
		e.settleNow(now)
		if got := e.delivery(da.ID); got.PRState != store.PRStateMerged || got.TurnEndedAt != "" {
			t.Fatalf("%+v", got)
		}
		if d := e.fired(q); !sameIDs(d.MessageIDs, b.ID) {
			t.Fatalf("claimed %v, want b before the window ends", d.MessageIDs)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.prChain("", now.Add(-time.Minute))
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
		e.settleNow(now)
		if got := e.delivery(da.ID); got.PRState != store.PRStateNone {
			t.Fatalf("%+v", got)
		}
		e.runner().checkAfter(context.Background(), e.queueRow(q), now.Add(time.Hour))
		if got := e.memoRow(b.ID); got.State != store.MemoStatePending {
			t.Fatalf("b is %s inside the window", got.State)
		}
		e.runner().checkAfter(context.Background(), e.queueRow(q), now.Add(25*time.Hour))
		got := e.memoRow(b.ID)
		if hr := memoafter.ParseHeldReason(got.HeldReason); got.State != store.MemoStateHeld || hr == nil || hr.Reason != memoafter.WhyNoPR {
			t.Fatalf("b after the window with no PR: %+v", got)
		}
	})
}

// A snapshot that once named PRs is never undone by a later "no PR", and one kept final is never
// overwritten.
func TestMemoPRSnapshotWritesAreGuarded(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, _, da, _ := e.prChain("clean", time.Now().Add(-time.Minute))
		ctx := context.Background()
		at := store.DeliveryTS(time.Now())
		if ok, err := e.st.RecordPRSnapshot(ctx, da.ID, `[{"provider":"github","key":"o/r#7","state":"open"}]`, store.PRStateWaiting, at); err != nil || !ok {
			t.Fatalf("first: %v %v", ok, err)
		}
		if ok, _ := e.st.RecordPRSnapshot(ctx, da.ID, `[]`, store.PRStateNone, at); ok {
			t.Fatal("a no-PR replaced a snapshot that named a PR")
		}
		if ok, _ := e.st.RecordPRGone(ctx, da.ID, at); ok {
			t.Fatal("a session gone replaced a snapshot that named a PR")
		}
		if ok, _ := e.st.RecordPRSnapshot(ctx, da.ID, `[{"provider":"github","key":"o/r#7","state":"merged"}]`, store.PRStateMerged, at); !ok {
			t.Fatal("the merge was not kept")
		}
		if ok, _ := e.st.RecordPRSnapshot(ctx, da.ID, `[{"provider":"github","key":"o/r#7","state":"open"}]`, store.PRStateWaiting, at); ok {
			t.Fatal("a final snapshot was overwritten")
		}
	})
}

// prWakes counts the member's wakes made only to read a snapshot.
func (e *deliveryEnv) prWakes(since time.Time) int {
	e.t.Helper()
	n := 0
	if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) (err error) {
		n, err = tx.CountPRWakesSince(store.DeliveryTS(since))
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// A stopped workspace whose queue waits on a snapshot alone is woken at most once an hour, the
// wake is not a delivery (the fires of the day and the settle wakes do not see it), and a snapshot
// unchanged for 24 hours wakes nothing more and is told once.
func TestMemoPRSnapshotWakes(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.prChain("clean", now.Add(-time.Minute))
		e.setRuntime(agent, "stopped")
		before := len(e.allDeliveries(q))
		_, wakes := e.autoTick(now)
		if len(wakes) != 1 || e.prWakes(now.Add(-time.Hour)) != 1 {
			t.Fatalf("%d wakes, %d recorded, want one", len(wakes), e.prWakes(now.Add(-time.Hour)))
		}
		if got := e.allDeliveries(q); len(got) != before {
			t.Fatalf("the wake wrote a delivery: %d rows, had %d", len(got), before)
		}
		_ = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
			fires, _ := tx.CountDeliveriesCreatedSince(q, store.DeliveryTS(now.Add(-time.Hour)))
			settle, _ := tx.CountMemberWakesSince(store.DeliveryTS(now.Add(-time.Hour)))
			if fires != 1 || settle != 0 {
				t.Fatalf("the wake was counted: %d fires, %d settle wakes", fires, settle)
			}
			return nil
		})
		if got := e.prWakeAudits(); got != 1 {
			t.Fatalf("%d audit entries", got)
		}
		// The workspace the wake started is read at once: nothing was stamped while it was stopped.
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer("open") })
		e.setRuntime(agent, "running")
		e.autoTick(now.Add(time.Minute))
		if n := len(agent.prCallsSoFar()); n != 1 {
			t.Fatalf("%d reads right after the wake, want 1", n)
		}
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now.Add(30 * time.Minute)); len(wakes) != 0 {
			t.Fatal("woken twice within the hour")
		}
		if _, wakes := e.autoTick(now.Add(61 * time.Minute)); len(wakes) != 1 {
			t.Fatal("not woken again after the hour")
		}
		// 24 hours without a change: no wake, and the member is told once.
		late := now.Add(time.Minute + 25*time.Hour)
		if _, wakes := e.autoTick(late); len(wakes) != 0 {
			t.Fatal("woken for a snapshot unchanged for a day")
		}
		e.autoTick(late.Add(2 * time.Hour))
		if n := e.notificationsOf(memoPRStaleKind); n != 1 {
			t.Fatalf("%d notices, want 1", n)
		}
		// A change starts the count again.
		at := store.DeliveryTS(late)
		if _, err := e.st.RecordPRSnapshot(context.Background(), da.ID, `[{"provider":"github","key":"o/r#7","state":"draft"}]`, store.PRStateWaiting, at); err != nil {
			t.Fatal(err)
		}
		if _, wakes := e.autoTick(late.Add(3 * time.Hour)); len(wakes) != 1 {
			t.Fatal("not woken after the snapshot changed")
		}
	})
}

// No wake for a message that waits on anything but a read: a prerequisite still running asks for
// none. Such a message does not keep a wake from one that waits on a read alone either: each
// message is judged on its own (issue #4876).
func TestMemoPRSnapshotWakeOnlyForSnapshots(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 3}, 0)
		x := e.pending("app", "impl", "x")
		e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "y waits for x", x.ID))
		e.fired(q) // x runs: y waits for a running delivery, not a snapshot
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 0 || e.prWakes(now.Add(-time.Hour)) != 0 {
			t.Fatalf("woken while the only message waits for a running prerequisite: %d", len(wakes))
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, _, _ := e.prChain("clean", now.Add(-time.Minute))
		e.setRuntime(agent, "running")
		x := e.pending("app", "impl", "x")
		e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "y waits for x", x.ID))
		e.fired(q) // x runs, a slot stays free: b still waits on a's snapshot alone
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 1 || e.prWakes(now.Add(-time.Hour)) != 1 {
			t.Fatalf("%d wakes, want one for b's read beside y's running prerequisite", len(wakes))
		}
	})
}

func (e *deliveryEnv) prWakeAudits() int {
	e.t.Helper()
	all, err := e.st.ListAuditByTenant(context.Background(), "", 100)
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, a := range all {
		if a.Action == "workspace.queue_pr_wake" {
			n++
		}
	}
	return n
}

// A message that began to wait a day after its prerequisite closed has not waited a day: its
// snapshot still wakes the workspace, and nothing is told.
func TestMemoPRSnapshotStaleCountsFromTheWait(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, _, _ := e.prChain("clean", now.Add(-25*time.Hour))
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 1 {
			t.Fatalf("%d wakes for a message that has just begun to wait", len(wakes))
		}
		if n := e.notificationsOf(memoPRStaleKind); n != 0 {
			t.Fatalf("%d stale notices", n)
		}
	})
}

// A queue whose Agent was found without the queue report is not woken: it could not fire the
// message once the PRs merged.
func TestMemoPRSnapshotNoWakeWithoutTheReport(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, _, _ := e.prChain("clean", now.Add(-time.Minute))
		if err := e.st.SetMemoQueueNoReport(context.Background(), q, store.DeliveryTS(now)); err != nil {
			t.Fatal(err)
		}
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 0 || e.prWakes(now.Add(-time.Hour)) != 0 {
			t.Fatalf("%d wakes for an Agent without the report", len(wakes))
		}
	})
}
