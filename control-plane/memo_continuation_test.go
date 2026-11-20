package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A run of a queue may add a continuation of the message it was given, and the Control Plane
// admits it as pending only while the member's release of that message still covers it (ADR
// 0137, addendum 2026-11-20, #4606). Each wall below is a case that must stay held.

const contRef = "acme/app#5"

// contQueue makes category name a queue with continuationMax max (-1 leaves it at the default).
func (e *inboxEnv) contQueue(name string, max int, extra map[string]any) string {
	e.t.Helper()
	cat := e.category("r", name)
	body := map[string]any{"instruction": "do it"}
	if max >= 0 {
		body["continuationMax"] = max
	}
	for k, v := range extra {
		body[k] = v
	}
	if rec := e.do("a", "PUT", "/api/memo-queues/"+cat, body); rec.Code != http.StatusOK {
		e.t.Fatalf("put queue: %d %s", rec.Code, rec.Body.String())
	}
	return cat
}

func contMemo(category, body, key string) map[string]any {
	m := textMemo("r", category, body)
	m["reference"] = map[string]any{"provider": "github", "key": key, "url": "https://example.test/" + key}
	return m
}

// contRoot is a message the member wrote in the Console (pending, released by nobody else).
func (e *inboxEnv) contRoot(category, key string) store.Memo {
	e.t.Helper()
	rec := e.do("a", "POST", "/api/memos", contMemo(category, "part 1 of "+key, key))
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("root: %d %s", rec.Code, rec.Body.String())
	}
	return e.memoRow(decode[memoDTO](e.t, rec).ID)
}

// contRun is an open delivery of queue q that holds messages ids and whose session is session,
// with the messages delivered the way a fire leaves them.
func (e *inboxEnv) contRun(q, session string, state string, ids ...string) string {
	e.t.Helper()
	id := store.NewID()
	ts := store.DeliveryTS(time.Now())
	d := store.MemoDelivery{ID: id, MembershipID: e.members["a"], QueueID: q, State: state, Session: session,
		MessageIDs: ids, CreatedAt: ts, UpdatedAt: ts, SentAt: ts, OpenedAt: ts}
	if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error { return tx.InsertDelivery(d) }); err != nil {
		e.t.Fatal(err)
	}
	for _, mid := range ids {
		if _, err := e.st.DB().ExecContext(context.Background(),
			`UPDATE memo SET state='delivered', delivery_id=?, sent_at=?, release_hash='', released_at='' WHERE id=?`, id, ts, mid); err != nil {
			e.t.Fatal(err)
		}
	}
	return id
}

// contSend is the create the relay sends for a continuation claim.
func (e *inboxEnv) contSend(category, body, key, origin, producer, delivery string) store.Memo {
	e.t.Helper()
	m := contMemo(category, body, key)
	m["producer"], m["producerOrigin"], m["continuation"], m["queueDelivery"] = producer, origin, true, delivery
	rec := e.memoTokenDo("a", "POST", "/internal/memos", m)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	return e.memoRow(decode[memoDTO](e.t, rec).ID)
}

func (e *inboxEnv) chainRow(id string) (admitted, stopped int, found bool) {
	e.t.Helper()
	err := e.st.DB().QueryRowContext(context.Background(), `SELECT admitted, stopped FROM memo_chain WHERE id=?`, id).Scan(&admitted, &stopped)
	return admitted, stopped, err == nil
}

func contHeldReason(t *testing.T, m store.Memo) string {
	t.Helper()
	if m.State != store.MemoStateHeld {
		return "<" + m.State + ">"
	}
	if r := memoafter.ParseHeldReason(m.HeldReason); r != nil {
		return r.Reason
	}
	return ""
}

// The chain is bounded by M counted from the root: M continuations in, the next one held.
func TestMemoContinuationAdmitsUpToMThenHolds(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 2, nil)
		root := e.contRoot("impl", contRef)
		given := root
		for seq := 1; seq <= 2; seq++ {
			d := e.contRun(q, "run-1", store.DeliveryOpen, given.ID)
			c := e.contSend("impl", "next part", contRef, "queue", "run-1", d)
			if c.State != store.MemoStatePending || c.ReleasedBy != store.MemoReleasedByContinuation ||
				c.ChainID != root.ID || c.ChainSeq != seq || c.ChainDelivery != d || c.ReleaseHash != memoContentHash(c) {
				t.Fatalf("continuation %d not admitted as expected: %+v", seq, c)
			}
			given = c
		}
		d := e.contRun(q, "run-1", store.DeliveryOpen, given.ID)
		c := e.contSend("impl", "one too many", contRef, "queue", "run-1", d)
		if got := contHeldReason(t, c); got != memoContHeldLimit || c.ReleaseHash != "" || c.ChainID != "" {
			t.Fatalf("the third continuation: reason %q, %+v", got, c)
		}
		if n, _, ok := e.chainRow(root.ID); !ok || n != 2 {
			t.Fatalf("chain admitted %d (found %v), want 2", n, ok)
		}
		e.assertPendingReleaseMatches()

		// The count outlives the root: the sweep deletes a delivered message a week on.
		if _, err := e.st.DB().ExecContext(context.Background(), `DELETE FROM memo WHERE id=?`, root.ID); err != nil {
			t.Fatal(err)
		}
		if n, _, _ := e.chainRow(root.ID); n != 2 {
			t.Fatalf("the count did not survive the root: %d", n)
		}
	})
}

// Past M is held, and so is everything the release does not cover.
func TestMemoContinuationWalls(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		other := e.contQueue("other", 4, nil)
		cases := []struct {
			name   string
			setup  func(root store.Memo) (key, origin, producer, delivery, category string)
			reason string
		}{
			{"a different reference", func(r store.Memo) (string, string, string, string, string) {
				return "acme/app#6", "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, r.ID), "impl"
			}, memoContHeldReference},
			{"a different queue", func(r store.Memo) (string, string, string, string, string) {
				return "", "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, r.ID), "other"
			}, memoContHeldDelivery},
			{"a session a person started", func(r store.Memo) (string, string, string, string, string) {
				return "", "user", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, r.ID), "impl"
			}, memoContHeldOrigin},
			{"a scheduled session", func(r store.Memo) (string, string, string, string, string) {
				return "", "schedule", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, r.ID), "impl"
			}, memoContHeldOrigin},
			{"a session another session started", func(r store.Memo) (string, string, string, string, string) {
				return "", "session", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, r.ID), "impl"
			}, memoContHeldOrigin},
			{"no delivery", func(r store.Memo) (string, string, string, string, string) {
				e.contRun(q, "run-1", store.DeliveryClosed, r.ID)
				return "", "queue", "run-1", "", "impl"
			}, memoContHeldOrigin},
			{"a delivery that does not exist", func(r store.Memo) (string, string, string, string, string) {
				e.contRun(q, "run-1", store.DeliveryClosed, r.ID)
				return "", "queue", "run-1", "no-such-delivery", "impl"
			}, memoContHeldDelivery},
			{"a delivery that is closed", func(r store.Memo) (string, string, string, string, string) {
				return "", "queue", "run-1", e.contRun(q, "run-1", store.DeliveryClosed, r.ID), "impl"
			}, memoContHeldDelivery},
			{"a delivery of another queue", func(r store.Memo) (string, string, string, string, string) {
				return "", "queue", "run-1", e.contRun(other, "run-1", store.DeliveryOpen, r.ID), "impl"
			}, memoContHeldDelivery},
			{"another session's delivery", func(r store.Memo) (string, string, string, string, string) {
				return "", "queue", "run-2", e.contRun(q, "run-1", store.DeliveryOpen, r.ID), "impl"
			}, memoContHeldDelivery},
			{"a batch run", func(r store.Memo) (string, string, string, string, string) {
				second := e.contRoot("impl", "acme/app#99")
				return "", "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, r.ID, second.ID), "impl"
			}, memoContHeldBatch},
			{"a root a tidy released", func(r store.Memo) (string, string, string, string, string) {
				if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET released_by='tidy' WHERE id=?`, r.ID); err != nil {
					t.Fatal(err)
				}
				return "", "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, r.ID), "impl"
			}, memoContHeldRoot},
		}
		for i, c := range cases {
			root := e.contRoot("impl", fmt.Sprintf("acme/app#%d", 100+i))
			key, origin, producer, delivery, category := c.setup(root)
			if key == "" {
				key = root.RefKey
			}
			got := e.contSend(category, "next "+string(rune('a'+i)), key, origin, producer, delivery)
			if r := contHeldReason(t, got); r != c.reason || got.ChainID != "" || got.ReleaseHash != "" {
				t.Errorf("%s: reason %q (want %q), %+v", c.name, r, c.reason, got)
			}
			if _, _, found := e.chainRow(root.ID); found {
				t.Errorf("%s: a chain row was written", c.name)
			}
		}

		// A run admits one continuation, and a stopped chain admits none.
		root := e.contRoot("impl", contRef)
		d := e.contRun(q, "run-1", store.DeliveryOpen, root.ID)
		first := e.contSend("impl", "first", contRef, "queue", "run-1", d)
		if first.State != store.MemoStatePending {
			t.Fatalf("first: %+v", first)
		}
		// The open message already holding the reference is answered, so the run adds nothing more.
		rec := e.memoTokenDo("a", "POST", "/internal/memos", func() map[string]any {
			m := contMemo("impl", "second", contRef)
			m["producer"], m["producerOrigin"], m["continuation"], m["queueDelivery"] = "run-1", "queue", true, d
			return m
		}())
		if got := decode[memoDTO](t, rec); rec.Code != http.StatusOK || got.ID != first.ID {
			t.Errorf("a second continuation from one run: %d %+v", rec.Code, got)
		}
		// Once the first has moved on, the run's own delivery still admits no second one.
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='delivered', release_hash='', released_at='' WHERE id=?`, first.ID); err != nil {
			t.Fatal(err)
		}
		if second := e.contSend("impl", "second", contRef, "queue", "run-1", d); contHeldReason(t, second) != memoContHeldUsed {
			t.Errorf("a second continuation from one run: %+v", second)
		}
		root2 := e.contRoot("impl", "acme/app#7")
		if rec := e.do("a", "POST", "/api/memos/"+root2.ID+"/stop-chain", nil); rec.Code != http.StatusOK {
			t.Fatalf("stop: %d %s", rec.Code, rec.Body.String())
		}
		d2 := e.contRun(q, "run-1", store.DeliveryOpen, root2.ID)
		if c := e.contSend("impl", "after stop", "acme/app#7", "queue", "run-1", d2); contHeldReason(t, c) != memoContHeldStopped {
			t.Errorf("a stopped chain admitted: %+v", c)
		}
		// Only the member reaches it, and only for their own messages.
		if rec := e.memoTokenDo("a", "POST", "/internal/memos/"+root2.ID+"/stop-chain", nil); rec.Code == http.StatusOK {
			t.Errorf("the token face stopped a chain")
		}
		if rec := e.do("c", "POST", "/api/memos/"+root2.ID+"/stop-chain", nil); rec.Code != http.StatusNotFound {
			t.Errorf("another member's stop: %d", rec.Code)
		}
	})
}

// A queue whose continuation_max is 0 never admits one.
func TestMemoContinuationOffAndTheDefaultAndTheCeiling(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		off := e.contQueue("off", 0, nil)
		root := e.contRoot("off", contRef)
		d := e.contRun(off, "run-1", store.DeliveryOpen, root.ID)
		if c := e.contSend("off", "next", contRef, "queue", "run-1", d); contHeldReason(t, c) != memoContHeldOff {
			t.Errorf("max 0 admitted: %+v", c)
		}

		def := e.contQueue("def", -1, nil)
		got := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues
		for _, q := range got {
			if q.CategoryID == def && q.ContinuationMax != memoContinuationDefault {
				t.Errorf("default continuationMax = %d, want %d", q.ContinuationMax, memoContinuationDefault)
			}
		}
		if rec := e.do("a", "PUT", "/api/memo-queues/"+def, map[string]any{"instruction": "x", "continuationMax": memoContinuationCeiling + 1}); rec.Code != http.StatusBadRequest {
			t.Errorf("above the ceiling: %d", rec.Code)
		}
		if rec := e.do("a", "PUT", "/api/memo-queues/"+def, map[string]any{"instruction": "x", "continuationMax": -1}); rec.Code != http.StatusBadRequest {
			t.Errorf("negative: %d", rec.Code)
		}
		// A row that predates the column reads as 4: the migration's default applies to every queue.
		cat := e.category("r", "old")
		if _, err := e.st.DB().ExecContext(context.Background(),
			`INSERT INTO memo_queue(category_id, membership_id, created_at, updated_at) VALUES(?,?,?,?)`,
			cat, e.members["a"], "2026-11-20T00:00:00Z", "2026-11-20T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := e.st.DB().QueryRowContext(context.Background(), `SELECT continuation_max FROM memo_queue WHERE category_id=?`, cat).Scan(&n); err != nil || n != memoContinuationDefault {
			t.Errorf("column default = %d, %v", n, err)
		}
		// The ceiling holds against a row that says more.
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_queue SET continuation_max=99 WHERE category_id=?`, def); err != nil {
			t.Fatal(err)
		}
		if memoContinuationMax(store.MemoQueue{ContinuationMax: 99}) != memoContinuationCeiling {
			t.Errorf("a row above the ceiling is not clamped")
		}
	})
}

// A claim from a session a person started follows the switch only: no chain is written, and a
// message the switch accepted is the root of a chain only because the switch is on.
func TestMemoContinuationForgedUserClaimFollowsTheSwitchOnly(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		for _, on := range []bool{false, true} {
			name := "sw-off"
			if on {
				name = "sw-on"
			}
			q := e.contQueue(name, 4, map[string]any{"acceptOwnSessions": on})
			root := e.contRoot(name, contRef)
			d := e.contRun(q, "run-1", store.DeliveryOpen, root.ID)
			c := e.contSend(name, "forged", contRef, "user", "run-1", d)
			if on != (c.State == store.MemoStatePending) {
				t.Errorf("switch %v: state %q", on, c.State)
			}
			if on && (c.ReleasedBy != store.MemoReleasedBySetting || c.ChainID != "") {
				t.Errorf("a user session's message is the switch's: %+v", c)
			}
			if !on && c.ChainID != "" {
				t.Errorf("held message carries a chain: %+v", c)
			}
			if _, _, found := e.chainRow(root.ID); found {
				t.Errorf("switch %v: the forged claim wrote a chain", on)
			}

			// A message the switch accepted roots a chain when the switch is on, and with it off
			// the same session's message is held and so never reaches a run.
			code, sm := e.fromSession(name, "own session note", "user", map[string]any{"reference": map[string]any{"provider": "github", "key": "acme/app#11", "url": "https://example.test/acme/app#11"}})
			if code != http.StatusCreated {
				t.Fatalf("create: %d %+v", code, sm)
			}
			row := e.memoRow(sm.ID)
			if !on {
				if row.State != store.MemoStateHeld {
					t.Errorf("switch off: own-session note %q", row.State)
				}
				continue
			}
			if row.State != store.MemoStatePending || row.ReleasedBy != store.MemoReleasedBySetting {
				t.Fatalf("switch on: own-session note %+v", row)
			}
			d2 := e.contRun(q, "run-1", store.DeliveryOpen, row.ID)
			cont := e.contSend(name, "continued", "acme/app#11", "queue", "run-1", d2)
			if cont.State != store.MemoStatePending || cont.ChainID != row.ID || cont.ChainSeq != 1 {
				t.Errorf("a switch-accepted message did not root a chain: %+v", cont)
			}
		}
	})
}

// The Console face and /mcp never read the claim: a Console note stays the member's own, with no chain.
func TestMemoContinuationClaimIsTokenFaceOnly(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		root := e.contRoot("impl", contRef)
		d := e.contRun(q, "run-1", store.DeliveryOpen, root.ID)
		m := contMemo("impl", "console", contRef)
		m["continuation"], m["queueDelivery"] = true, d
		c := e.memoRow(decode[memoDTO](t, e.do("a", "POST", "/api/memos", m)).ID)
		if c.ChainID != "" || c.ReleasedBy != "" {
			t.Errorf("the Console claim was read: %+v", c)
		}
		if _, _, found := e.chainRow(root.ID); found {
			t.Errorf("a chain row was written")
		}
	})
}

// What happens to a continuation: a Console edit keeps its chain, a token-face edit holds it and
// leaves the count spent, a person's release starts a fresh chain, and a move ends it.
func TestMemoContinuationLifecycle(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 1, nil)
		e.category("r", "elsewhere")
		root := e.contRoot("impl", contRef)
		c1 := e.contSend("impl", "p2", contRef, "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, root.ID))
		if c1.State != store.MemoStatePending {
			t.Fatalf("c1: %+v", c1)
		}

		// A Console edit keeps the state and the chain.
		if rec := e.do("a", "PATCH", "/api/memos/"+c1.ID, map[string]any{"body": "p2 edited"}); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		if row := e.memoRow(c1.ID); row.State != store.MemoStatePending || row.ChainID != root.ID {
			t.Errorf("console edit: %+v", row)
		}
		// A token-face edit holds it, the chain stays, the count stays spent.
		if rec := e.memoTokenDo("a", "PATCH", "/internal/memos/"+c1.ID, map[string]any{"body": "p2 from a session"}); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		row := e.memoRow(c1.ID)
		if row.State != store.MemoStateHeld || row.ChainID != root.ID {
			t.Errorf("token edit: %+v", row)
		}
		if n, _, _ := e.chainRow(root.ID); n != 1 {
			t.Errorf("count after the edit = %d", n)
		}
		// A person's release clears the chain: it is a fresh release, so a new chain starts at it.
		hash := memoContentHash(row)
		if rec := e.do("a", "POST", "/api/memos/release", map[string]any{"items": []map[string]any{{"id": c1.ID, "contentHash": hash}}}); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		row = e.memoRow(c1.ID)
		if row.State != store.MemoStatePending || row.ChainID != "" || row.ReleasedBy != "" {
			t.Errorf("after release: %+v", row)
		}
		c2 := e.contSend("impl", "p3", contRef, "queue", "run-2", e.contRun(q, "run-2", store.DeliveryOpen, row.ID))
		if c2.State != store.MemoStatePending || c2.ChainID != c1.ID || c2.ChainSeq != 1 {
			t.Errorf("a released message did not root a fresh chain: %+v", c2)
		}
		// A move to another category ends the chain.
		if rec := e.do("a", "PATCH", "/api/memos/"+c2.ID, map[string]any{"category": "elsewhere"}); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		if row := e.memoRow(c2.ID); row.ChainID != "" || row.ChainSeq != 0 || row.ChainDelivery != "" {
			t.Errorf("after the move: %+v", row)
		}
		e.assertPendingReleaseMatches()
	})
}

// A tidy only keeps a continuation.
func TestMemoContinuationIsKeptByATidy(t *testing.T) {
	rows := []store.Memo{{ID: "a", ChainID: "root"}, {ID: "b"}}
	if l := memoafter.Linked(rows); !l["a"] || l["b"] {
		t.Errorf("Linked = %v", l)
	}
}
