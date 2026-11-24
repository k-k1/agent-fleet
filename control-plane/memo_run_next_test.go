package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The "run next" mark of a waiting message (issue #5021).

// setRunNext sets the mark through the store, as the member's write will.
func (e *deliveryEnv) setRunNext(id string, on bool) bool {
	e.t.Helper()
	var won bool
	if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
		var err error
		won, err = tx.SetMemoRunNext(id, on)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return won
}

// closeOnly closes a delivery so that its slot frees.
func (e *deliveryEnv) closeOnly(d memoDeliveryDTO) {
	e.closeDeliveryAs(d.ID, "done", "none", 0, time.Now())
}

// With the slots full and messages waiting, the marked one is the next started when a slot frees,
// and the others follow oldest first.
func TestMemoRunNextIsClaimedWhenASlotFrees(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 1})
		var ms []memoDTO
		for _, s := range []string{"zero", "one", "two", "three"} {
			ms = append(ms, e.pending("app", "impl", s))
		}
		first := e.fired(q)
		if !sameIDs(first.MessageIDs, ms[0].ID) {
			t.Fatalf("first fired %v", first.MessageIDs)
		}
		if !e.setRunNext(ms[2].ID, true) {
			t.Fatal("the mark was not set on a pending message")
		}
		if row := e.memoRow(ms[2].ID); !row.RunNext || row.ReleaseHash != memoContentHash(row) {
			t.Fatalf("marked row %+v", row)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNoSlot {
			t.Fatalf("a full queue fired: %d %s", rec.Code, rec.Body.String())
		}
		e.closeOnly(first)
		next := e.fired(q)
		if !sameIDs(next.MessageIDs, ms[2].ID) {
			t.Fatalf("after the slot freed %v, want the marked %s", next.MessageIDs, ms[2].ID)
		}
		e.closeOnly(next)
		if d := e.fired(q); !sameIDs(d.MessageIDs, ms[1].ID) {
			t.Fatalf("then %v, want the oldest %s", d.MessageIDs, ms[1].ID)
		}
	})
}

// The dispatcher's own claim takes the marked message first too.
func TestMemoRunNextOrdersTheDispatch(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 1}, 0)
		older := e.pending("app", "impl", "older")
		newer := e.pending("app", "impl", "newer")
		e.setRunNext(newer.ID, true)
		sends, _ := e.runner().dispatch(context.Background(), time.Now(), 1)
		if len(sends) != 1 || !sameIDs(sends[0].MessageIDs, newer.ID) || sends[0].QueueID != q {
			t.Fatalf("dispatched %+v, want the marked %s before %s", sends, newer.ID, older.ID)
		}
	})
}

// A batch starts with the marked messages, then fills up oldest first.
func TestMemoRunNextLeadsTheBatch(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 3})
		a := e.pending("app", "impl", "a")
		b := e.pending("app", "impl", "b")
		c := e.pending("app", "impl", "c")
		d := e.pending("app", "impl", "d")
		e.setRunNext(d.ID, true)
		e.setRunNext(c.ID, true)
		if got := e.fired(q); !sameIDs(got.MessageIDs, c.ID, d.ID, a.ID) {
			t.Fatalf("batch %v, want %v", got.MessageIDs, []string{c.ID, d.ID, a.ID})
		}
		if row := e.memoRow(b.ID); row.State != store.MemoStatePending {
			t.Fatalf("b %+v", row)
		}
	})
}

// The mark only orders: a marked message whose prerequisites are not satisfied, or whose start
// time has not come, is still held back and does not stop the messages behind it.
func TestMemoRunNextKeepsEligibility(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 3})
		pre := e.pending("app", "impl", "prerequisite")
		rec := e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "dependent", pre.ID))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create dependent: %d %s", rec.Code, rec.Body.String())
		}
		dep := decode[memoDTO](t, rec)
		e.setRunNext(dep.ID, true)
		if got := e.fired(q); !sameIDs(got.MessageIDs, pre.ID) {
			t.Fatalf("fired %v, want the prerequisite %s", got.MessageIDs, pre.ID)
		}
		if row := e.memoRow(dep.ID); row.State != store.MemoStatePending || !row.RunNext {
			t.Fatalf("the dependent %+v", row)
		}

		later := e.pending("app", "impl", "scheduled")
		at := store.DeliveryTS(time.Now().Add(2 * time.Hour))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET start_after=?, release_start_after=? WHERE id=?`, at, at, later.ID); err != nil {
			t.Fatal(err)
		}
		row := e.memoRow(later.ID)
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET release_hash=? WHERE id=?`, memoContentHash(row), later.ID); err != nil {
			t.Fatal(err)
		}
		e.setRunNext(later.ID, true)
		plain := e.pending("app", "impl", "plain")
		if got := e.fired(q); !sameIDs(got.MessageIDs, plain.ID) {
			t.Fatalf("fired %v, want %s past the marked messages that cannot run", got.MessageIDs, plain.ID)
		}
	})
}

// The mark is set only on a waiting message of a queue, is not content (a release and an edit
// keep it, the release hash does not move), and a move out of every queue clears it.
func TestMemoRunNextStore(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		e.category("app", "notes")

		// A held message from a session: marked, then released, the mark stays.
		rec := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("app", "impl", "from a session"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		held := decode[memoDTO](t, rec)
		if held.State != store.MemoStateHeld {
			t.Fatalf("created %+v", held)
		}
		if !e.setRunNext(held.ID, true) {
			t.Fatal("the mark was not set on a held message")
		}
		if row := e.memoRow(held.ID); !row.RunNext || memoContentHash(row) != held.ContentHash {
			t.Fatalf("held row %+v, hash %q want %q", row, memoContentHash(row), held.ContentHash)
		}
		if rec := e.release(held.ID, held.ContentHash); rec.Code != http.StatusOK {
			t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(held.ID); row.State != store.MemoStatePending || !row.RunNext {
			t.Fatalf("the release lost the mark: %+v", row)
		}
		// An edit through the Console keeps it: no edit writes the column.
		if rec := e.do("a", "PATCH", "/api/memos/"+held.ID, map[string]any{"body": "from a session, edited"}); rec.Code != http.StatusOK {
			t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(held.ID); !row.RunNext {
			t.Fatalf("an edit cleared the mark: %+v", row)
		}
		// The setter clears it, and sets it again.
		if !e.setRunNext(held.ID, false) || e.memoRow(held.ID).RunNext {
			t.Fatal("the mark was not cleared")
		}
		e.setRunNext(held.ID, true)
		// A move to a category that is not a queue makes a plain note and clears it.
		if rec := e.do("a", "PATCH", "/api/memos/"+held.ID, map[string]any{"category": "notes"}); rec.Code != http.StatusOK {
			t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(held.ID); row.State != "" || row.RunNext {
			t.Fatalf("a plain note kept the mark: %+v", row)
		}
		// Nothing but a waiting message takes it: a plain note, a claimed message, a missing id.
		if e.setRunNext(held.ID, true) || e.memoRow(held.ID).RunNext {
			t.Fatal("a plain note took the mark")
		}
		m := e.pending("app", "impl", "claimed")
		e.fired(q)
		if e.setRunNext(m.ID, true) || e.memoRow(m.ID).RunNext {
			t.Fatal("a claimed message took the mark")
		}
		if e.setRunNext("0123456789abcdef0123456789abcdef", true) {
			t.Fatal("a missing message took the mark")
		}
		// Another member's message is out of reach.
		other := e.pending("app", "impl", "mine")
		var won bool
		if err := e.st.MemoTx(context.Background(), e.members["b"], func(tx store.MemoTxOps) error {
			var err error
			won, err = tx.SetMemoRunNext(other.ID, true)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if won || e.memoRow(other.ID).RunNext {
			t.Fatal("another member set the mark")
		}
	})
}
