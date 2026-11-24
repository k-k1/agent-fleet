package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A marked message the Agent would refuse does not stop an older message that would be claimed
// without the mark: it is refused at its own place in the oldest-first order, as without the mark.
func TestMemoRunNextUnsendableDoesNotBlock(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"maxActive": 2})
		good := e.pending("app", "impl", "good")
		big := e.pending("app", "impl", strings.Repeat("x", memoDeliveryItemBodyMax+1))
		e.setRunNext(big.ID, true)
		if d := e.fired(q); !sameIDs(d.MessageIDs, good.ID) {
			t.Fatalf("fired %v, want %s past the unsendable marked message", d.MessageIDs, good.ID)
		}
		// Alone, it is refused with the reason, as it is without the mark.
		rec := e.fire(q)
		if rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueItemInvalid || !strings.Contains(rec.Body.String(), big.ID) {
			t.Fatalf("fire of the unsendable message: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(big.ID); row.State != store.MemoStatePending || row.DeliveryID != "" || !row.RunNext {
			t.Fatalf("the refused message %+v", row)
		}
		// The dispatcher takes the good message past it too.
		_, q2 := e.autoQueue("app", "auto", map[string]any{"maxActive": 1}, 0)
		good2 := e.pending("app", "auto", "good")
		big2 := e.pending("app", "auto", strings.Repeat("y", memoDeliveryItemBodyMax+1))
		e.setRunNext(big2.ID, true)
		sends, _ := e.runner().dispatch(context.Background(), time.Now(), 4)
		var got []string
		for _, d := range sends {
			if d.QueueID == q2 {
				got = append(got, d.MessageIDs...)
			}
		}
		if !sameIDs(got, good2.ID) {
			t.Fatalf("dispatched %v, want %s", got, good2.ID)
		}
	})
}

// A marked message fixes the batch's recipe: the batch holds the marked message and the oldest
// messages of its recipe, and an older message of another recipe waits.
func TestMemoRunNextFixesTheBatchRecipe(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"batch": 5})
		a := e.pending("app", "impl", "a, the category's recipe")
		b1 := decode[memoDTO](t, e.recipeMemo("app", "impl", "b1", map[string]any{"agentKind": "codex"}))
		b2 := decode[memoDTO](t, e.recipeMemo("app", "impl", "b2", map[string]any{"agentKind": "codex"}))
		e.setRunNext(b2.ID, true)
		if d := e.fired(q); !sameIDs(d.MessageIDs, b2.ID, b1.ID) {
			t.Fatalf("batch %v, want %v", d.MessageIDs, []string{b2.ID, b1.ID})
		}
		if row := e.memoRow(a.ID); row.State != store.MemoStatePending || row.DeliveryID != "" {
			t.Fatalf("a %+v", row)
		}
	})
}

// Only InsertMemo and SetMemoRunNext write the column: an edit writes back neither value it was
// handed, a send in flight refuses the setter, and a session's edit cannot set it.
func TestMemoRunNextWriteBoundary(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		tx := func(f func(store.MemoTxOps) error) {
			t.Helper()
			if err := e.st.MemoTx(context.Background(), e.members["a"], f); err != nil {
				t.Fatal(err)
			}
		}
		marked := e.pending("app", "impl", "inserted marked")
		row := e.memoRow(marked.ID)
		row.ID, row.RunNext, row.CreatedAt = store.NewID(), true, store.NowTS()
		tx(func(tx store.MemoTxOps) error { return tx.InsertMemo(row) })
		if got := e.memoRow(row.ID); !got.RunNext {
			t.Fatalf("InsertMemo dropped the mark: %+v", got)
		}
		// An edit handed the opposite value writes neither.
		up := e.memoRow(row.ID)
		up.RunNext, up.Body = false, "edited"
		tx(func(tx store.MemoTxOps) error { return tx.WriteMemo(up) })
		if got := e.memoRow(row.ID); !got.RunNext || got.Body != "edited" {
			t.Fatalf("an edit cleared the mark: %+v", got)
		}
		plain := e.memoRow(marked.ID)
		plain.RunNext = true
		tx(func(tx store.MemoTxOps) error { return tx.WriteMemo(plain) })
		if got := e.memoRow(marked.ID); got.RunNext {
			t.Fatalf("an edit set the mark: %+v", got)
		}
		// A pending message in a send in flight is not waiting.
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET send_token='t', send_started_at=? WHERE id=?`, store.NowTS(), marked.ID); err != nil {
			t.Fatal(err)
		}
		if e.setRunNext(marked.ID, true) || e.memoRow(marked.ID).RunNext {
			t.Fatal("a message being sent took the mark")
		}
		// A session's edit has no field for it (a create may mark the message it adds:
		// TestMemoRunNextOnCreate).
		rec := e.memoTokenDo("a", "POST", "/internal/memos", textMemo("app", "impl", "from a session"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("token create: %d %s", rec.Code, rec.Body.String())
		}
		held := decode[memoDTO](t, rec)
		if rec := e.memoTokenDo("a", "PATCH", "/internal/memos/"+held.ID, map[string]any{"body": "edited by a session", "runNext": true}); rec.Code != http.StatusOK {
			t.Fatalf("token patch: %d %s", rec.Code, rec.Body.String())
		}
		if e.memoRow(held.ID).RunNext {
			t.Fatal("a session's edit set the mark")
		}
	})
}

// A batch a marked message started is not refused by a message the Agent would refuse that only
// its recipe brings into reach: the oldest-first claim would skip that message for its other recipe
// and claim the rest. The unsendable message is refused once the oldest-first order reaches it.
func TestMemoRunNextBatchOfAnotherRecipeSkipsTheUnsendable(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		for _, markBig := range []bool{false, true} {
			cat := map[bool]string{false: "plain", true: "both"}[markBig]
			q := e.queueWith("app", cat, map[string]any{"batch": 2, "maxActive": 3})
			a1 := e.pending("app", cat, "a1")
			b1 := decode[memoDTO](t, e.recipeMemo("app", cat, strings.Repeat("x", memoDeliveryItemBodyMax+1), map[string]any{"agentKind": "codex"}))
			a2 := e.pending("app", cat, "a2")
			b2 := decode[memoDTO](t, e.recipeMemo("app", cat, "b2", map[string]any{"agentKind": "codex"}))
			e.setRunNext(b2.ID, true)
			if markBig {
				e.setRunNext(b1.ID, true)
			}
			if d := e.fired(q); !sameIDs(d.MessageIDs, b2.ID) {
				t.Fatalf("%s: fired %v, want the marked %s", cat, d.MessageIDs, b2.ID)
			}
			if d := e.fired(q); !sameIDs(d.MessageIDs, a1.ID, a2.ID) {
				t.Fatalf("%s: then %v, want %v", cat, d.MessageIDs, []string{a1.ID, a2.ID})
			}
			rec := e.fire(q)
			if rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueItemInvalid || !strings.Contains(rec.Body.String(), b1.ID) {
				t.Fatalf("%s: fire of the unsendable message: %d %s", cat, rec.Code, rec.Body.String())
			}
			// Free the member's slots for the next case.
			if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET state='closed'`); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// makeOldest backdates a message, so the oldest-first order does not hang on two creates landing
// in the same second (created_at has second resolution; the tie falls to position and id).
func (e *deliveryEnv) makeOldest(id string) {
	e.t.Helper()
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET created_at='2000-01-01T00:00:00Z' WHERE id=?`, id); err != nil {
		e.t.Fatal(err)
	}
}

// runNextCall is the member's Run next / Clear run next on one message.
func (e *deliveryEnv) runNextCall(member, id string, body any) *httptest.ResponseRecorder {
	return e.do(member, "POST", "/api/memos/"+id+"/run-next", body)
}

// The member's route sets and clears the mark on a waiting message, the answer and the list carry
// it, and the claim then takes the marked message first.
func TestMemoRunNextRoute(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		older := e.pending("app", "impl", "older")
		newer := e.pending("app", "impl", "newer")
		rec := e.runNextCall("a", newer.ID, map[string]any{"runNext": true})
		if rec.Code != http.StatusOK {
			t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
		}
		if got := decode[memoDTO](t, rec); !got.RunNext || got.ID != newer.ID {
			t.Fatalf("answer %+v", got)
		}
		if !e.memoRow(newer.ID).RunNext || !e.listed(newer.ID).RunNext || e.listed(older.ID).RunNext {
			t.Fatal("the list does not show the mark on the marked message alone")
		}
		// Clear, then set again: each answer says where it stands.
		if rec := e.runNextCall("a", newer.ID, map[string]any{"runNext": false}); rec.Code != http.StatusOK || decode[memoDTO](t, rec).RunNext {
			t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
		}
		if e.memoRow(newer.ID).RunNext || e.listed(newer.ID).RunNext {
			t.Fatal("the mark was not cleared")
		}
		if rec := e.runNextCall("a", newer.ID, map[string]any{"runNext": true}); rec.Code != http.StatusOK {
			t.Fatalf("set again: %d %s", rec.Code, rec.Body.String())
		}
		d := e.fired(q)
		if !sameIDs(d.MessageIDs, newer.ID) {
			t.Fatalf("fired %v, want the marked %s before %s", d.MessageIDs, newer.ID, older.ID)
		}
		// A claimed message is no longer waiting: the route refuses it, and the list stops showing
		// the mark the row still holds.
		if row := e.memoRow(newer.ID); !row.RunNext {
			t.Fatalf("claimed row %+v", row)
		}
		if e.listed(newer.ID).RunNext {
			t.Fatal("a claimed message shows the mark")
		}
		rec = e.runNextCall("a", newer.ID, map[string]any{"runNext": false})
		if rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoLocked {
			t.Fatalf("clear on a claimed message: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// The route refuses what is not a waiting message of the member's own queue, and a request that
// does not say which way.
func TestMemoRunNextRouteRefusals(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		e.category("app", "notes")
		m := e.pending("app", "impl", "waiting")
		for _, body := range []any{nil, map[string]any{}, map[string]any{"runNext": "yes"}} {
			if rec := e.runNextCall("a", m.ID, body); rec.Code != http.StatusBadRequest {
				t.Fatalf("body %v: %d %s", body, rec.Code, rec.Body.String())
			}
		}
		if e.memoRow(m.ID).RunNext {
			t.Fatal("a refused request set the mark")
		}
		if rec := e.runNextCall("a", "0123456789abcdef0123456789abcdef", map[string]any{"runNext": true}); rec.Code != http.StatusNotFound {
			t.Fatalf("missing: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.runNextCall("b", m.ID, map[string]any{"runNext": true}); rec.Code != http.StatusNotFound || e.memoRow(m.ID).RunNext {
			t.Fatalf("another member: %d %s", rec.Code, rec.Body.String())
		}
		rec := e.do("a", "POST", "/api/memos", textMemo("app", "notes", "a plain note"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("plain create: %d %s", rec.Code, rec.Body.String())
		}
		plain := decode[memoDTO](t, rec)
		rec = e.runNextCall("a", plain.ID, map[string]any{"runNext": true})
		if rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoInQueue || e.memoRow(plain.ID).RunNext {
			t.Fatalf("plain note: %d %s", rec.Code, rec.Body.String())
		}
		// The workspace's token face has no such route.
		if rec := e.memoTokenDo("a", "POST", "/internal/memos/"+m.ID+"/run-next", map[string]any{"runNext": true}); rec.Code == http.StatusOK || e.memoRow(m.ID).RunNext {
			t.Fatalf("token face: %d %s", rec.Code, rec.Body.String())
		}
		// Nor does a Console edit read the field.
		if rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"body": "edited", "runNext": true}); rec.Code != http.StatusOK || e.memoRow(m.ID).RunNext {
			t.Fatalf("a Console edit set the mark: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// The mark is a place in one queue's order: a move to another category leaves it, whoever moves
// the message, and a session's edit of the content leaves it too, so a run cannot carry the
// member's mark into a queue whose switch accepts its messages. The member's edit in place keeps it.
func TestMemoRunNextLeavesWithAMove(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("r", "a-src", nil)
		dst := e.queueWith("r", "a-dst", map[string]any{"acceptOwnSessions": true, "maxActive": 1})
		older := e.pending("r", "a-dst", "older, unmarked")
		e.makeOldest(older.ID)
		_, held := e.fromSession("a-src", "by a session the member started", "user", nil)
		if rec := e.release(held.ID, held.ContentHash); rec.Code != http.StatusOK {
			t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.runNextCall("a", held.ID, map[string]any{"runNext": true}); rec.Code != http.StatusOK {
			t.Fatalf("mark: %d %s", rec.Code, rec.Body.String())
		}
		rec := e.memoTokenDo("a", "PATCH", "/internal/memos/"+held.ID, map[string]any{"category": "a-dst"})
		if rec.Code != http.StatusOK || decode[memoDTO](t, rec).RunNext {
			t.Fatalf("token move: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(held.ID); row.State != store.MemoStatePending || row.RunNext {
			t.Fatalf("after the token move: %+v, want pending without the mark", row)
		}
		if d := e.fired(dst); !sameIDs(d.MessageIDs, older.ID) {
			t.Fatalf("fired %v, want the older %s first", d.MessageIDs, older.ID)
		}

		// A session's edit of the content in place, the switch keeping it pending.
		_, again := e.fromSession("a-dst", "another", "user", nil)
		if again.State != store.MemoStatePending {
			t.Fatalf("created %+v", again)
		}
		e.runNextCall("a", again.ID, map[string]any{"runNext": true})
		if rec := e.memoTokenDo("a", "PATCH", "/internal/memos/"+again.ID, map[string]any{"body": "replaced by the session"}); rec.Code != http.StatusOK {
			t.Fatalf("token edit: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(again.ID); row.State != store.MemoStatePending || row.RunNext {
			t.Fatalf("after the session's edit: %+v, want pending without the mark", row)
		}
		// A session's write that changes no content (its position) keeps it.
		e.runNextCall("a", again.ID, map[string]any{"runNext": true})
		if rec := e.memoTokenDo("a", "PATCH", "/internal/memos/"+again.ID, map[string]any{"position": 7}); rec.Code != http.StatusOK || !e.memoRow(again.ID).RunNext {
			t.Fatalf("a reorder by a session dropped the mark: %d %s", rec.Code, rec.Body.String())
		}

		// The member's move to another queue leaves it too; the member's edit in place keeps it.
		mine := e.pending("r", "a-src", "the member's")
		e.runNextCall("a", mine.ID, map[string]any{"runNext": true})
		if rec := e.do("a", "PATCH", "/api/memos/"+mine.ID, map[string]any{"body": "the member's, edited"}); rec.Code != http.StatusOK || !e.memoRow(mine.ID).RunNext {
			t.Fatalf("the member's edit dropped the mark: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.do("a", "PATCH", "/api/memos/"+mine.ID, map[string]any{"category": "a-dst"}); rec.Code != http.StatusOK || e.memoRow(mine.ID).RunNext {
			t.Fatalf("the member's move kept the mark: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// A message in a send in flight is not waiting: the list does not show its mark.
func TestMemoRunNextHiddenWhileSending(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		m := e.pending("app", "impl", "waiting")
		e.runNextCall("a", m.ID, map[string]any{"runNext": true})
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET send_token='t', send_started_at=? WHERE id=?`, store.NowTS(), m.ID); err != nil {
			t.Fatal(err)
		}
		if e.listed(m.ID).RunNext {
			t.Fatal("a message being sent shows the mark")
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET send_token='', send_started_at='' WHERE id=?`, m.ID); err != nil {
			t.Fatal(err)
		}
		if !e.listed(m.ID).RunNext {
			t.Fatal("the mark is not shown once the send is given up")
		}
	})
}

// A tidy run's move to another queue leaves the mark as well, even where it lands pending.
func TestMemoRunNextLeavesWithATidyMove(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		listed := e.queueWith("other", "inbox", nil)
		agent, q, ids := e.tidyAuto(map[string]any{"tidyDestinations": []string{listed}}, 2)
		if !e.setRunNext(ids[0], true) || !e.setRunNext(ids[1], true) {
			t.Fatal("the marks were not set")
		}
		agent.tidyReply = tidyOpsReply(func(ids []string, _ map[string]any) []any {
			return []any{map[string]any{"op": "move", "id": ids[0], "to": listed}}
		})
		e.tidyTick(e.runner(), time.Now())
		if run := e.onlyRun(q); run.State != store.TidyRunApplied {
			t.Fatalf("run: %+v", run)
		}
		if m := e.memoRow(ids[0]); m.State != store.MemoStatePending || m.Repo != "other" || m.RunNext {
			t.Fatalf("the moved message %+v, want pending without the mark", m)
		}
		if m := e.memoRow(ids[1]); !m.RunNext {
			t.Fatalf("the message that stayed lost its mark: %+v", m)
		}
	})
}

// A merge of a queue into another leaves the mark; a rename of the same queue keeps it.
func TestMemoRunNextCategoryRenameAndMerge(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		src := e.queueWith("app", "src", nil)
		dst := e.queueWith("app", "dst", map[string]any{"maxActive": 1})
		older := e.pending("app", "dst", "older, unmarked")
		e.makeOldest(older.ID)
		m := e.pending("app", "src", "marked in src")
		e.runNextCall("a", m.ID, map[string]any{"runNext": true})
		if rec := e.do("a", "PATCH", "/api/memo-categories/"+src, map[string]any{"name": "src2"}); rec.Code != http.StatusOK {
			t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(m.ID); row.Category != "src2" || !row.RunNext {
			t.Fatalf("a rename dropped the mark: %+v", row)
		}
		if rec := e.do("a", "PATCH", "/api/memo-categories/"+src, map[string]any{"name": "dst"}); rec.Code != http.StatusOK {
			t.Fatalf("merge: %d %s", rec.Code, rec.Body.String())
		}
		row := e.memoRow(m.ID)
		if row.Category != "dst" || row.State != store.MemoStatePending || row.RunNext {
			t.Fatalf("after the merge: %+v, want pending in dst without the mark", row)
		}
		if d := e.fired(dst); !sameIDs(d.MessageIDs, older.ID) {
			t.Fatalf("fired %v, want the older %s", d.MessageIDs, older.ID)
		}
	})
}

// Undoing a tidy move does not take a mark set where the run put the message back home with it.
func TestMemoRunNextTidyUndoLeavesTheDestinationsMark(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		listed := e.queueWith("other", "inbox", nil)
		agent, q, ids := e.tidyAuto(map[string]any{"tidyDestinations": []string{listed}}, 2)
		agent.tidyReply = tidyOpsReply(func(ids []string, _ map[string]any) []any {
			return []any{map[string]any{"op": "move", "id": ids[0], "to": listed}}
		})
		e.tidyTick(e.runner(), time.Now())
		run := e.onlyRun(q)
		if m := e.memoRow(ids[0]); m.State != store.MemoStatePending || m.Repo != "other" {
			t.Fatalf("the moved message %+v", m)
		}
		if rec := e.runNextCall("a", ids[0], map[string]any{"runNext": true}); rec.Code != http.StatusOK {
			t.Fatalf("mark: %d %s", rec.Code, rec.Body.String())
		}
		rec := e.undo(run.ID)
		if rec.Code != http.StatusOK {
			t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
		}
		answered := false
		for _, m := range decode[memoTidyUndoWire](t, rec).Restored {
			if m.ID == ids[0] {
				answered = true
				if m.RunNext {
					t.Fatalf("the undo answered the mark the row no longer has: %+v", m)
				}
			}
		}
		if !answered {
			t.Fatal("the undo did not answer the message it moved home")
		}
		if m := e.memoRow(ids[0]); m.Repo == "other" || m.State != store.MemoStatePending || m.RunNext {
			t.Fatalf("after the undo: %+v, want pending at home without the mark", m)
		}
	})
}

// A create into a queue may mark the message it adds, on every face: the Console, the token face
// and /mcp. The mark releases nothing, and it marks nothing else: a create answered with a message
// already waiting (same item, or the same text from inside the workspace) leaves that one as it is,
// a create outside every queue is refused rather than dropped, and no face's edit sets the mark.
func TestMemoRunNextOnCreate(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		e.category("r", "notes")
		for i, c := range faces(e) {
			marked := textMemo("r", "q", c.name+" marked")
			marked["runNext"] = true
			code, d := c.create(marked)
			if code != http.StatusCreated || !d.RunNext || !e.memoRow(d.ID).RunNext {
				t.Fatalf("%s: create %d %+v", c.name, code, d)
			}
			// The mark is no release: a session's marked message still waits for the member's.
			wantState := store.MemoStateHeld
			if c.name == "console" {
				wantState = store.MemoStatePending
			}
			if d.State != wantState || e.memoRow(d.ID).State != wantState {
				t.Fatalf("%s: a marked create entered as %q, want %q", c.name, d.State, wantState)
			}
			if c.name != "console" {
				// The same text again from inside the workspace answers the marked message, mark and all.
				again := textMemo("r", "q", c.name+" marked")
				code, dup := c.create(again)
				if code != http.StatusOK || !dup.Existing || dup.ID != d.ID || !dup.RunNext || !e.memoRow(d.ID).RunNext {
					t.Fatalf("%s: same-text create %d %+v", c.name, code, dup)
				}
			}
			if code, d := c.create(textMemo("r", "q", c.name+" plain")); code != http.StatusCreated || d.RunNext || e.memoRow(d.ID).RunNext {
				t.Fatalf("%s: a create without the field marked it: %d %+v", c.name, code, d)
			}

			// The same item already waiting: that message comes back unmarked, nothing is added.
			n := fmt.Sprint(100 + i)
			code, first := c.create(refMemo("r", "q", "", ghRef(n)))
			if code != http.StatusCreated {
				t.Fatalf("%s: reference create %d", c.name, code)
			}
			again := refMemo("r", "q", "", ghRef(n))
			again["runNext"] = true
			code, dup := c.create(again)
			if code != http.StatusOK || !dup.Existing || dup.ID != first.ID || dup.RunNext || e.memoRow(first.ID).RunNext {
				t.Fatalf("%s: duplicate create %d %+v", c.name, code, dup)
			}

			// A plain note has no queue order to put it in.
			outside := textMemo("r", "notes", c.name+" outside")
			outside["runNext"] = true
			if code, _ := c.create(outside); code < 400 {
				t.Fatalf("%s: a mark outside every queue answered %d", c.name, code)
			}
			if c.name != "mcp" {
				rec := c.http("POST", "/memos", outside)
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errCodeMemoRunNextInvalid) {
					t.Fatalf("%s: outside %d %s", c.name, rec.Code, rec.Body.String())
				}
			}

			// An edit with the field leaves the mark where it was, either way.
			if code, _ := c.update(first.ID, map[string]any{"position": 7, "runNext": true}); code != http.StatusOK || e.memoRow(first.ID).RunNext {
				t.Fatalf("%s: an edit set the mark: %d", c.name, code)
			}
			if code, _ := c.update(d.ID, map[string]any{"position": 8, "runNext": false}); code != http.StatusOK || !e.memoRow(d.ID).RunNext {
				t.Fatalf("%s: an edit cleared the mark: %d", c.name, code)
			}
		}
	})
}
