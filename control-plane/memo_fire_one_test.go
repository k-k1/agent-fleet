package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func (e *deliveryEnv) fireMemo(queueID, memoID string) *httptest.ResponseRecorder {
	return e.do("a", "POST", "/api/memo-queues/"+queueID+"/fire/"+memoID, nil)
}

func (e *deliveryEnv) notReady(queueID, memoID, why string) {
	e.t.Helper()
	rec := e.fireMemo(queueID, memoID)
	if rec.Code != http.StatusConflict || deliveryErrCode(e.t, rec) != errCodeMemoQueueMemoNotReady || !strings.Contains(rec.Body.String(), why) {
		e.t.Fatalf("press for %s: %d %s, want %s with %q", memoID, rec.Code, rec.Body.String(), errCodeMemoQueueMemoNotReady, why)
	}
}

// A press that names a message (POST .../fire/{memoId}) claims that message alone, whatever the batch and however many
// older messages are waiting; without a body the press is the oldest-first claim as before.
func TestMemoFireOneClaimsOnlyTheNamedMessage(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 3, "maxActive": 3})
		m1 := e.pending("app", "impl", "first")
		m2 := e.pending("app", "impl", "second")
		m3 := e.pending("app", "impl", "third")
		m4 := e.pending("app", "impl", "fourth")

		rec := e.fireMemo(q, m3.ID)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("press for the third: %d %s", rec.Code, rec.Body.String())
		}
		d := decode[memoDeliveryDTO](t, rec)
		if !sameIDs(d.MessageIDs, m3.ID) {
			t.Fatalf("claimed %v, want only the third", d.MessageIDs)
		}
		if got := e.memoRow(m3.ID); got.State != store.MemoStateClaimed || got.DeliveryID != d.ID {
			t.Fatalf("third %+v", got)
		}
		for _, id := range []string{m1.ID, m2.ID, m4.ID} {
			if got := e.memoRow(id); got.State != store.MemoStatePending || got.DeliveryID != "" {
				t.Fatalf("%s was touched: %+v", id, got)
			}
		}
		// The same message again is part of a run, and is refused, not replaced by an older one.
		e.notReady(q, m3.ID, memoNotReadyClaimed)
		if got := e.memoRow(m1.ID); got.State != store.MemoStatePending {
			t.Fatalf("the oldest was claimed by a refused press: %+v", got)
		}
		// The plain route is what it was: oldest first, whatever body an older Console sends with it.
		rec = e.do("a", "POST", "/api/memo-queues/"+q+"/fire", map[string]any{})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("plain press: %d %s", rec.Code, rec.Body.String())
		}
		if d := decode[memoDeliveryDTO](t, rec); !sameIDs(d.MessageIDs, m1.ID, m2.ID, m4.ID) {
			t.Fatalf("a plain press claimed %v", d.MessageIDs)
		}
	})
}

// Each reason a message cannot run now is refused with its own text, and the message is never
// swapped for another one.
func TestMemoFireOneRefusals(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 3, "maxActive": 3})
		other := e.pending("app", "impl", "older and ready")

		// Held for approval: written by a session, not yet released.
		held := memoFaceClient{"token", e.inboxEnv}
		if code, _ := held.create(textMemo("app", "impl", "from a session")); code != http.StatusCreated {
			t.Fatalf("token create: %d", code)
		}
		var heldID string
		for _, m := range decode[[]memoDTO](t, e.do("a", "GET", "/api/memos", nil)) {
			if m.State == store.MemoStateHeld {
				heldID = m.ID
			}
		}
		if heldID == "" {
			t.Fatal("no held message")
		}
		e.notReady(q, heldID, memoNotReadyHeld)

		// A prerequisite that has not finished.
		pre := e.pending("app", "impl", "prerequisite")
		dep := decode[memoDTO](t, e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "dependent", pre.ID)))
		e.notReady(q, dep.ID, memoNotReadyPrerequisites)

		// A start time and a hand-back that have not come.
		sched := e.pending("app", "impl", "scheduled")
		later := store.DeliveryTS(time.Now().Add(3 * time.Hour))
		row := e.memoRow(sched.ID)
		row.StartAfter = later
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET start_after=?, release_start_after=?, release_hash=? WHERE id=?`,
			later, later, memoContentHash(row), sched.ID); err != nil {
			t.Fatal(err)
		}
		e.notReady(q, sched.ID, memoNotReadyScheduled)
		back := e.pending("app", "impl", "handed back")
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET not_before=? WHERE id=?`, later, back.ID); err != nil {
			t.Fatal(err)
		}
		e.notReady(q, back.ID, memoNotReadyDeferred)

		// A release that no longer matches the content goes back to held, and that sticks.
		stale := e.pending("app", "impl", "released once")
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET release_hash='not-this-content' WHERE id=?`, stale.ID); err != nil {
			t.Fatal(err)
		}
		e.notReady(q, stale.ID, memoNotReadyHeld)
		if got := e.memoRow(stale.ID); got.State != store.MemoStateHeld || got.ReleaseHash != "" {
			t.Fatalf("stale release after the press: %+v", got)
		}

		// A message that is not in this queue: unknown, or in another category of the member.
		e.queueWith("app", "elsewhere", nil)
		elsewhere := e.pending("app", "elsewhere", "in another queue")
		for _, id := range []string{"no-such-id", elsewhere.ID} {
			if rec := e.fireMemo(q, id); rec.Code != http.StatusNotFound {
				t.Fatalf("press for %q: %d %s", id, rec.Code, rec.Body.String())
			}
		}
		// Another member cannot reach the queue, so not its messages either.
		if rec := e.do("c", "POST", "/api/memo-queues/"+q+"/fire/"+other.ID, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("another member's press: %d %s", rec.Code, rec.Body.String())
		}
		for _, id := range []string{other.ID, pre.ID} {
			if got := e.memoRow(id); got.State != store.MemoStatePending || got.DeliveryID != "" {
				t.Fatalf("%s was claimed by a refused press: %+v", id, got)
			}
		}
		if ds, err := e.st.ListMemoDeliveries(context.Background(), e.members["a"], q, 10); err != nil || len(ds) != 0 {
			t.Fatalf("deliveries after refusals: %v, %v", ds, err)
		}
	})
}

// The slot and the session quota hold for a named message exactly as for the oldest-first press.
func TestMemoFireOneNeedsASlotAndQuota(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent := e.useQueueAgent("")
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 1})
		e.pending("app", "impl", "first")
		second := e.pending("app", "impl", "second")
		third := e.pending("app", "impl", "third")
		if err := e.st.PutUserLimit(context.Background(), e.members["a"], store.UserQuota{MaxSessions: 1}); err != nil {
			t.Fatal(err)
		}
		agent.rows = append(agent.rows, map[string]any{"name": "busy", "kind": "claude", "alive": true, "state": "working"})
		if rec := e.fireMemo(q, third.ID); rec.Code != http.StatusTooManyRequests || deliveryErrCode(t, rec) != errCodeQuotaSessions {
			t.Fatalf("quota: %d %s", rec.Code, rec.Body.String())
		}
		agent.rows = nil
		if rec := e.fireMemo(q, third.ID); rec.Code != http.StatusAccepted {
			t.Fatalf("press: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.fireMemo(q, second.ID); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNoSlot {
			t.Fatalf("no slot: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// A named memo whose recipe the queue's agent cannot run is held for a person and the refusal
// commits that hold, as the oldest-first claim does; an older memo that could run stays waiting.
func TestMemoFireOneHoldsAnInvalidRecipe(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", map[string]any{})
		other := e.queueWith("app", "other", map[string]any{"agentKind": "codex", "maxActive": 3})
		older := e.pending("app", "other", "older and runnable")
		m := decode[memoDTO](t, e.recipeMemo("app", "impl", "claude only", map[string]any{"route": "anthropic-subscription"}))
		if rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"category": "other"}); rec.Code != http.StatusOK {
			t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
		}
		e.notReady(other, m.ID, memoNotReadyHeld)
		row := e.memoRow(m.ID)
		if row.State != store.MemoStateHeld || !strings.Contains(row.HeldReason, "recipe_invalid") {
			t.Fatalf("row %+v", row)
		}
		if got := e.memoRow(older.ID); got.State != store.MemoStatePending || got.DeliveryID != "" {
			t.Fatalf("the older memo was touched: %+v", got)
		}
		if ds, err := e.st.ListMemoDeliveries(context.Background(), e.members["a"], other, 10); err != nil || len(ds) != 0 {
			t.Fatalf("deliveries: %v, %v", ds, err)
		}
	})
}

// A memo that is not in the queue is a 404 before the queue's own limits are asked, so a stale id
// never reads as "every slot is taken".
func TestMemoFireOneChecksTheTargetBeforeTheSlots(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.useQueueAgent("")
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 1})
		e.queueWith("app", "elsewhere", nil)
		moved := e.pending("app", "elsewhere", "now in another queue")
		e.pending("app", "impl", "takes the only slot")
		e.fired(q)
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNoSlot {
			t.Fatalf("the slot is not full: %d %s", rec.Code, rec.Body.String())
		}
		for _, id := range []string{moved.ID, "no-such-id"} {
			if rec := e.fireMemo(q, id); rec.Code != http.StatusNotFound {
				t.Fatalf("press for %q with every slot taken: %d %s", id, rec.Code, rec.Body.String())
			}
		}
	})
}
