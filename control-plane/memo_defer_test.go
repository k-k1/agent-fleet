package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A run that hands its message back (ADR 0137, addendum 2026-11-19, part B).

// openDeferrable is openReported behind an Agent that reports the turn starts and answers the PR
// snapshot with no PR, with the session's last turn started an hour ago. It answers the message.
func (e *deliveryEnv) openDeferrable(settings map[string]any) (*fakeQueueAgent, store.MemoDelivery, string) {
	e.t.Helper()
	agent, d := e.openReported(settings)
	agent.mu.Lock()
	agent.defers = true
	agent.mu.Unlock()
	agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
	agent.setRow(d.Session, map[string]any{"lastTurnStartAt": time.Now().Add(-time.Hour).Format(time.RFC3339Nano)})
	if len(d.MessageIDs) != 1 {
		e.t.Fatalf("messages %v", d.MessageIDs)
	}
	return agent, d, d.MessageIDs[0]
}

// deferCall is defer_queue_item as the Agent relays it, on the token face of member a.
func (e *deliveryEnv) deferCall(id string, body map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	if _, ok := body["at"]; !ok {
		body["at"] = time.Now().Format(time.RFC3339Nano)
	}
	return e.memoTokenDo("a", "POST", "/internal/memo-deliveries/"+id+"/defer", body)
}

// retireRequested says the close requested the retire of the session, as a clean close does: it
// is pending, or the retire pass of the same tick already finished it.
func retireRequested(d store.MemoDelivery) bool { return d.RetirePending || d.RetireResult != "" }

// settleTwice is the two clear reads a close needs.
func (e *deliveryEnv) settleTwice() {
	e.settleNow(time.Now())
	e.settleNow(time.Now())
}

// The request only records; the clean close that follows applies it: the delivery closes as
// deferred with its retire requested, and the message is pending again with the time it waits
// for, no delivery and no sent stamp. It is not claimed before that time, and the queue says why.
func TestMemoDeferAppliedByTheCleanClose(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d, mid := e.openDeferrable(map[string]any{"enabled": true})
		notBefore := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
		rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "waiting for #12 to merge",
			"notBefore": notBefore.Format(time.RFC3339)})
		if rec.Code != http.StatusOK {
			t.Fatalf("defer: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.DeferReason != "waiting for #12 to merge" || got.DeferAt == "" {
			t.Fatalf("after the call: %+v", got)
		}
		if m := e.memoRow(mid); m.State != store.MemoStateDelivered {
			t.Fatalf("the call alone moved the message: %s", m.State)
		}
		e.settleTwice()
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryDeferred || !retireRequested(got) || got.DeferResult != store.DeferApplied {
			t.Fatalf("after the close: %+v", got)
		}
		calls := agent.prCallsSoFar()
		if len(calls) == 0 || calls[0].Session != d.Session || calls[0].Since != got.DeferAt {
			t.Fatalf("PR reads %+v, want the session since the call", calls)
		}
		m := e.memoRow(mid)
		if m.State != store.MemoStatePending || m.DeliveryID != "" || m.SentAt != "" || m.DeferCount != 1 ||
			m.NotBefore != store.DeliveryTS(notBefore) || m.ReleaseHash == "" {
			t.Fatalf("message after the close: %+v", m)
		}
		if rs := store.ParseDeferReasons(m.DeferReasons); len(rs) != 1 || rs[0].Reason != "waiting for #12 to merge" || rs[0].Delivery != d.ID {
			t.Fatalf("reasons %+v", rs)
		}
		if rec := e.fire(got.QueueID); rec.Code != http.StatusConflict {
			t.Fatalf("a message waiting for its time was fired: %d %s", rec.Code, rec.Body.String())
		}
		if w := e.queueWaiting(got.QueueID); w == nil || w.Reason != waitDeferred || w.Until == "" {
			t.Fatalf("waiting %+v", w)
		}
		sql, goSide := e.statsOf(got.QueueID)
		if sql.Eligible != 0 || sql.Deferred != 1 || goSide.Deferred != 1 || sql.NextAt != goSide.NextAt {
			t.Fatalf("stats sql %+v go %+v", sql, goSide)
		}
		// The reason is the Console's alone: a session's list never carries it.
		if strings.Contains(e.memoTokenDo("a", "GET", "/internal/memos", nil).Body.String(), "waiting for #12") {
			t.Fatal("the token face lists the hand-back's reason")
		}
		if !strings.Contains(e.do("a", "GET", "/api/memos", nil).Body.String(), "waiting for #12") {
			t.Fatal("the Console's list lacks the hand-back's reason")
		}
		// Once its time has passed it is eligible again, in its old place.
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET not_before=? WHERE id=?`,
			store.DeliveryTS(time.Now().Add(-time.Minute)), mid); err != nil {
			t.Fatal(err)
		}
		if d2 := e.fired(got.QueueID); !sameIDs(d2.MessageIDs, mid) {
			t.Fatalf("re-fire claimed %v", d2.MessageIDs)
		}
	})
}

// A run that went on to work is not handed back: a turn of the tree that started after the call,
// a PR of the session that is open or merged, or no PR answer at all drops the request, and the
// close is clean with the message delivered.
func TestMemoDeferDroppedWhenTheRunWentOn(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(a *fakeQueueAgent, session string)
		result string
	}{
		{"a later turn of the root", func(a *fakeQueueAgent, s string) {
			a.setRow(s, map[string]any{"lastTurnStartAt": time.Now().Add(time.Minute).Format(time.RFC3339Nano)})
		}, store.DeferDroppedTurn},
		{"a later turn of a child", func(a *fakeQueueAgent, s string) {
			a.child("kid", s, map[string]any{"lastTurnStartAt": time.Now().Add(time.Minute).Format(time.RFC3339Nano)})
		}, store.DeferDroppedTurn},
		{"an open PR", func(a *fakeQueueAgent, _ string) {
			a.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer("closed", "open") })
		}, store.DeferDroppedPR},
		{"a merged PR", func(a *fakeQueueAgent, _ string) {
			a.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer("merged") })
		}, store.DeferDroppedPR},
		{"no PR answer", func(a *fakeQueueAgent, _ string) { a.setPRs(true, nil) }, store.DeferDroppedUnread},
		{"an Agent without the turn starts", func(a *fakeQueueAgent, _ string) {
			a.mu.Lock()
			a.defers = false
			a.mu.Unlock()
		}, store.DeferDroppedUnread},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
				agent, d, mid := e.openDeferrable(nil)
				if rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "later"}); rec.Code != http.StatusOK {
					t.Fatalf("defer: %d %s", rec.Code, rec.Body.String())
				}
				c.setup(agent, d.Session)
				e.settleTwice()
				got := e.delivery(d.ID)
				if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryClean || got.DeferResult != c.result || !retireRequested(got) {
					t.Fatalf("close %+v, want clean with %s", got, c.result)
				}
				if m := e.memoRow(mid); m.State != store.MemoStateDelivered || m.DeliveryID != d.ID || m.NotBefore != "" {
					t.Fatalf("message %+v", m)
				}
			})
		})
	}
}

// The close from the kept report while the workspace is stopped never applies a hand-back: the
// delivery stays open until a running Agent reports, and then it is applied.
func TestMemoDeferNotAppliedFromTheStoppedWorkspace(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d, mid := e.openDeferrable(nil)
		e.settleNow(time.Now()) // one clear read is kept
		if rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "later"}); rec.Code != http.StatusOK {
			t.Fatalf("defer: %d %s", rec.Code, rec.Body.String())
		}
		e.stopWorkspace(agent, d.Session, "")
		for i := 0; i < 4; i++ {
			e.settleNow(time.Now())
		}
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen {
			t.Fatalf("closed from the mirror: %+v", got)
		}
		if !settleNeedsAgent(e.delivery(d.ID)) {
			t.Fatal("a stopped workspace is not woken for the hand-back")
		}
		e.setRuntime(agent, "")
		e.settleTwice()
		if got := e.delivery(d.ID); got.Outcome != store.DeliveryDeferred {
			t.Fatalf("after the Agent reported: %+v", got)
		}
		if m := e.memoRow(mid); m.State != store.MemoStatePending {
			t.Fatalf("message %s", m.State)
		}
	})
}

// A later call replaces the request and cancel withdraws it; an aborted close ignores it.
func TestMemoDeferReplacedWithdrawnAndIgnored(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d, mid := e.openDeferrable(nil)
		e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "first"})
		e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "second"})
		if got := e.delivery(d.ID); got.DeferReason != "second" {
			t.Fatalf("not replaced: %q", got.DeferReason)
		}
		if rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "cancel": true}); rec.Code != http.StatusOK {
			t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.delivery(d.ID); got.DeferAt != "" || got.DeferReason != "" {
			t.Fatalf("not withdrawn: %+v", got)
		}
		e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "again"})
		agent.setRow(d.Session, map[string]any{"archived": true})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.Outcome != store.DeliveryAborted {
			t.Fatalf("archived: %+v", got)
		}
		if m := e.memoRow(mid); m.State != store.MemoStateDelivered {
			t.Fatalf("an aborted close returned the message: %s", m.State)
		}
	})
}

// The route accepts a call only for an open run that names the session, on a queue of batch 1,
// with a reason, a time 10 minutes to 7 days ahead and a stamp near this clock.
func TestMemoDeferRouteRefusals(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d, _ := e.openDeferrable(nil)
		now := time.Now()
		for _, c := range []struct {
			name string
			body map[string]any
			code int
		}{
			{"another session", map[string]any{"session": "someone", "reason": "x"}, http.StatusConflict},
			{"no reason", map[string]any{"session": d.Session}, http.StatusBadRequest},
			{"a reason over 1 KiB", map[string]any{"session": d.Session, "reason": strings.Repeat("x", 1025)}, http.StatusBadRequest},
			{"too soon", map[string]any{"session": d.Session, "reason": "x", "notBefore": now.Add(time.Minute).Format(time.RFC3339)}, http.StatusBadRequest},
			{"too late", map[string]any{"session": d.Session, "reason": "x", "notBefore": now.Add(8 * 24 * time.Hour).Format(time.RFC3339)}, http.StatusBadRequest},
			{"a stamp far off", map[string]any{"session": d.Session, "reason": "x", "at": now.Add(time.Hour).Format(time.RFC3339Nano)}, http.StatusBadRequest},
		} {
			if rec := e.deferCall(d.ID, c.body); rec.Code != c.code {
				t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body.String())
			}
		}
		if got := e.delivery(d.ID); got.DeferAt != "" {
			t.Fatalf("a refused call was recorded: %+v", got)
		}
		// The default is an hour, and a time a few seconds short of the bound is moved onto it.
		rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "x", "notBefore": now.Add(10*time.Minute - 5*time.Second).Format(time.RFC3339Nano)})
		if rec.Code != http.StatusOK || e.delivery(d.ID).DeferNotBefore < store.DeliveryTS(now.Add(10*time.Minute)) {
			t.Fatalf("near the bound: %d %s", rec.Code, rec.Body.String())
		}
		e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "x"})
		if nb := e.delivery(d.ID).DeferNotBefore; nb < store.DeliveryTS(now.Add(59*time.Minute)) || nb > store.DeliveryTS(now.Add(61*time.Minute)) {
			t.Fatalf("default not_before %s", nb)
		}
		if rec := e.deferCall("nope", map[string]any{"session": d.Session, "reason": "x"}); rec.Code != http.StatusNotFound {
			t.Fatalf("no run: %d", rec.Code)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d := e.openReported(map[string]any{"batch": 2})
		if rec := e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "x"}); rec.Code != http.StatusConflict ||
			deliveryErrCode(t, rec) != errCodeMemoDeferBatch {
			t.Fatalf("batch 2: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// The third hand-back of one message sends it to held with the reasons and no release; the
// person's release clears the time and the count.
func TestMemoDeferThirdGoesToHeld(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d, mid := e.openDeferrable(nil)
		prior, _ := json.Marshal([]store.MemoDeferEntry{{Reason: "one", At: "x"}, {Reason: "two", At: "y"}})
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET defer_count=2, defer_reasons=? WHERE id=?`, string(prior), mid); err != nil {
			t.Fatal(err)
		}
		e.deferCall(d.ID, map[string]any{"session": d.Session, "reason": "three"})
		e.settleTwice()
		m := e.memoRow(mid)
		if m.State != store.MemoStateHeld || m.ReleaseHash != "" || m.NotBefore != "" || m.DeferCount != 3 || m.DeliveryID != "" {
			t.Fatalf("third hand-back: %+v", m)
		}
		if hr := memoafter.ParseHeldReason(m.HeldReason); hr == nil || hr.Reason != store.MemoHeldDeferred {
			t.Fatalf("held reason %q", m.HeldReason)
		}
		if rs := store.ParseDeferReasons(m.DeferReasons); len(rs) != 3 || rs[2].Reason != "three" {
			t.Fatalf("reasons %+v", rs)
		}
		listed := e.listed(mid)
		rec := e.do("a", "POST", "/api/memos/release", map[string]any{"items": []map[string]any{{"id": mid, "contentHash": listed.ContentHash}}})
		if rec.Code != http.StatusOK {
			t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
		}
		if m := e.memoRow(mid); m.State != store.MemoStatePending || m.DeferCount != 0 || m.DeferReasons != "" || m.NotBefore != "" {
			t.Fatalf("after the release: %+v", m)
		}
	})
}

// A message whose reference another message of the queue took meanwhile stays where it is, the
// run history names that message, and the close still happens.
func TestMemoDeferBlockedByTheOpenReference(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		rec := e.do("a", "POST", "/api/memos", refMemo("app", "impl", "", ghRef("12")))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		mid := decode[memoDTO](t, rec).ID
		agent := e.useQueueAgent("")
		agent.mu.Lock()
		agent.report, agent.refs, agent.defers = true, true, true
		agent.mu.Unlock()
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
		d := e.fired(q)
		e.tick(e.runner(), time.Now())
		open := e.delivery(d.ID)
		agent.setRow(open.Session, map[string]any{"alive": true, "state": "idle"})
		// The same item is queued again while the first is delivered.
		rec = e.do("a", "POST", "/api/memos", refMemo("app", "impl", "", ghRef("12")))
		if rec.Code != http.StatusCreated {
			t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
		}
		second := decode[memoDTO](t, rec).ID
		e.deferCall(d.ID, map[string]any{"session": open.Session, "reason": "later"})
		e.settleTwice()
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.Outcome != store.DeliveryDeferred {
			t.Fatalf("close %+v", got)
		}
		if b := store.ParseReturnBlocked(got.ReturnBlocked); len(b) != 1 || b[0].ID != mid || b[0].By != second {
			t.Fatalf("blocked %+v", b)
		}
		if m := e.memoRow(mid); m.State != store.MemoStateDelivered || m.DeliveryID != d.ID {
			t.Fatalf("message %+v", m)
		}
		// The person's return is refused with that message named.
		rec = e.do("a", "POST", "/api/memo-queues/"+q+"/deliveries/"+d.ID+"/return", nil)
		if rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoReturnRefHeld || !strings.Contains(rec.Body.String(), second) {
			t.Fatalf("return: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// The person's "return to queue" of a closed run: the messages still of that run go back to
// pending with no time to wait and no hand-backs counted. A second press finds nothing, and an
// open run is refused. Console only: the token face has no such route.
func TestMemoReturnToQueue(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, d, mid := e.openDeferrable(nil)
		path := "/api/memo-queues/" + d.QueueID + "/deliveries/" + d.ID + "/return"
		if rec := e.do("a", "POST", path, nil); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoReturnNotDone {
			t.Fatalf("open run: %d %s", rec.Code, rec.Body.String())
		}
		e.settleTwice()
		if got := e.delivery(d.ID); got.Outcome != store.DeliveryClean {
			t.Fatalf("close %+v", got)
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET defer_count=2 WHERE id=?`, mid); err != nil {
			t.Fatal(err)
		}
		if rec := e.memoTokenDo("a", "POST", path, nil); rec.Code == http.StatusOK {
			t.Fatal("the token face returned a run")
		}
		rec := e.do("a", "POST", path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("return: %d %s", rec.Code, rec.Body.String())
		}
		if dto := decode[memoDeliveryDTO](t, rec); dto.ReturnedAt == "" {
			t.Fatalf("returned at not stamped: %+v", dto)
		}
		m := e.memoRow(mid)
		if m.State != store.MemoStatePending || m.DeliveryID != "" || m.SentAt != "" || m.DeferCount != 0 || m.NotBefore != "" {
			t.Fatalf("message %+v", m)
		}
		if rec := e.do("a", "POST", path, nil); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoReturnNothing {
			t.Fatalf("second press: %d %s", rec.Code, rec.Body.String())
		}
		if d2 := e.fired(d.QueueID); !sameIDs(d2.MessageIDs, mid) {
			t.Fatalf("re-fire claimed %v", d2.MessageIDs)
		}
	})
}

// A deferred delivery is skipped by the breaker: two bad first turns around a hand-back still
// pause the queue, so a hand-back (forged or not) cannot reset it.
func TestMemoDeferSkippedByTheBreaker(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", nil, 0)
		base := time.Now().Add(-time.Hour)
		err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
			for i, outcome := range []string{store.DeliveryClean, store.DeliveryClean, store.DeliveryDeferred} {
				at := store.DeliveryTS(base.Add(time.Duration(i) * time.Minute))
				if err := tx.InsertDelivery(store.MemoDelivery{ID: "brk" + itoaTest(i), QueueID: q, State: store.DeliveryClosed,
					Outcome: outcome, CreatedAt: at, UpdatedAt: at, ClosedAt: at}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for i, turn := range []string{"turn-failed", "turn-failed", "clean"} {
			if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET turn_outcome=?, turn_ms=120000, turn_ended_at=? WHERE id=?`,
				turn, store.DeliveryTS(base), "brk"+itoaTest(i)); err != nil {
				t.Fatal(err)
			}
		}
		e.runner().checkBreaker(context.Background(), e.queueRow(q), time.Now())
		if !e.queueRow(q).Paused {
			t.Fatal("a deferred delivery ended the run of bad ones")
		}
	})
}
