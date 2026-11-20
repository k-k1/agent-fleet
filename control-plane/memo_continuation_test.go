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
		// The record is on the delivery: with the first child gone (the member deleted it, or it moved
		// out of the bucket) the run's delivery still admits no second one.
		if _, err := e.st.DB().ExecContext(context.Background(), `DELETE FROM memo WHERE id=?`, first.ID); err != nil {
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

// The Console face never reads the claim, even a complete one: a Console note stays the member's own, with no chain.
func TestMemoContinuationClaimIsTokenFaceOnly(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		root := e.contRoot("impl", contRef)
		d := e.contRun(q, "run-1", store.DeliveryOpen, root.ID)
		m := contMemo("impl", "console", contRef)
		m["continuation"], m["queueDelivery"] = true, d
		m["producer"], m["producerOrigin"] = "run-1", "queue"
		rec := e.do("a", "POST", "/api/memos", m)
		if rec.Code != http.StatusCreated {
			t.Fatalf("console create: %d %s", rec.Code, rec.Body.String())
		}
		c := e.memoRow(decode[memoDTO](t, rec).ID)
		if c.State != store.MemoStatePending || c.Provenance != store.MemoProvenanceMember {
			t.Errorf("a Console create is the member's own note: %+v", c)
		}
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

// The URL of an admitted continuation is the given message's, whatever the claim sent: the
// work item is the provider and key, and the next session and the Console link follow the URL.
func TestMemoContinuationTakesTheGivenURL(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		root := e.contRoot("impl", contRef)
		d := e.contRun(q, "run-1", store.DeliveryOpen, root.ID)
		m := contMemo("impl", "next", contRef)
		m["reference"] = map[string]any{"provider": "github", "key": contRef, "url": "https://evil.example/elsewhere"}
		m["producer"], m["producerOrigin"], m["continuation"], m["queueDelivery"] = "run-1", "queue", true, d
		rec := e.memoTokenDo("a", "POST", "/internal/memos", m)
		if rec.Code != http.StatusCreated {
			t.Fatal(rec.Body.String())
		}
		c := e.memoRow(decode[memoDTO](t, rec).ID)
		if c.State != store.MemoStatePending || c.RefURL != root.RefURL || c.ReleaseHash != memoContentHash(c) {
			t.Errorf("continuation URL %q, want %q: %+v", c.RefURL, root.RefURL, c)
		}
	})
}

// A delivery that is overdue, waiting to be retired or past the queue's bound admits nothing.
func TestMemoContinuationNeedsALiveDelivery(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		for i, set := range []string{"overdue_at='2026-11-20T00:00:00Z'", "retire_pending=1", "opened_at='2020-01-01T00:00:00Z'"} {
			root := e.contRoot("impl", "acme/app#"+string(rune('1'+i))+"0")
			d := e.contRun(q, "run-1", store.DeliveryOpen, root.ID)
			if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET `+set+` WHERE id=?`, d); err != nil {
				t.Fatal(err)
			}
			c := e.contSend("impl", "next", root.RefKey, "queue", "run-1", d)
			if contHeldReason(t, c) != memoContHeldDelivery {
				t.Errorf("%s: %+v", set, c)
			}
		}
	})
}

// A rename of the queue keeps the chain and what admitted the child, and a move to another
// queue ends the chain without making the child a root there.
func TestMemoContinuationRenameAndMove(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 1, nil)
		other := e.contQueue("other", 4, nil)
		root := e.contRoot("impl", contRef)
		c1 := e.contSend("impl", "p2", contRef, "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, root.ID))
		if rec := e.do("a", "PATCH", "/api/memo-categories/"+q, map[string]any{"name": "impl2"}); rec.Code != http.StatusOK {
			t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
		}
		row := e.memoRow(c1.ID)
		if row.State != store.MemoStatePending || row.ReleasedBy != store.MemoReleasedByContinuation || row.ChainID != root.ID {
			t.Errorf("after the rename: %+v", row)
		}
		// The chain is at its limit: the renamed child's own continuation is held.
		c2 := e.contSend("impl2", "p3", contRef, "queue", "run-2", e.contRun(q, "run-2", store.DeliveryOpen, row.ID))
		if contHeldReason(t, c2) != memoContHeldLimit {
			t.Errorf("past the limit after a rename: %+v", c2)
		}
		// A move to another queue: pending as the member's own move, chain ended, no root there.
		root2 := e.contRoot("impl2", "acme/app#31")
		d1 := e.contSend("impl2", "q2", "acme/app#31", "queue", "run-4", e.contRun(q, "run-4", store.DeliveryOpen, root2.ID))
		if rec := e.do("a", "PATCH", "/api/memos/"+d1.ID, map[string]any{"category": "other"}); rec.Code != http.StatusOK {
			t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
		}
		moved := e.memoRow(d1.ID)
		if moved.ChainID != "" || moved.ReleasedBy != store.MemoReleasedByContinuation {
			t.Errorf("after the move: %+v", moved)
		}
		c3 := e.contSend("other", "p4", "acme/app#31", "queue", "run-3", e.contRun(other, "run-3", store.DeliveryOpen, moved.ID))
		if contHeldReason(t, c3) != memoContHeldRoot {
			t.Errorf("a moved continuation rooted a chain: %+v", c3)
		}
	})
}

// A chain row belongs to one work item: a person editing a child's reference ends its use.
func TestMemoContinuationReferenceEditEndsTheChain(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		root := e.contRoot("impl", contRef)
		c1 := e.contSend("impl", "p2", contRef, "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, root.ID))
		if rec := e.do("a", "PATCH", "/api/memos/"+c1.ID, map[string]any{"reference": map[string]any{"provider": "github", "key": "acme/app#77", "url": "https://example.test/acme/app#77"}}); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		edited := e.memoRow(c1.ID)
		c2 := e.contSend("impl", "p3", "acme/app#77", "queue", "run-2", e.contRun(q, "run-2", store.DeliveryOpen, edited.ID))
		if contHeldReason(t, c2) != memoContHeldReference {
			t.Errorf("a chain was reused for another work item: %+v", c2)
		}
	})
}

// A switch-accepted message starts a chain only while the switch is on. A chain already
// started goes on within its M after the switch is turned off.
func TestMemoContinuationSwitchRootNeedsTheSwitchNow(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("sw", 4, map[string]any{"acceptOwnSessions": true})
		ref := map[string]any{"reference": map[string]any{"provider": "github", "key": "acme/app#21", "url": "https://example.test/acme/app#21"}}
		_, a := e.fromSession("sw", "note a", "user", ref)
		ref2 := map[string]any{"reference": map[string]any{"provider": "github", "key": "acme/app#22", "url": "https://example.test/acme/app#22"}}
		_, b := e.fromSession("sw", "note b", "user", ref2)
		// b starts its chain while the switch is on.
		first := e.contSend("sw", "b2", "acme/app#22", "queue", "run-b", e.contRun(q, "run-b", store.DeliveryOpen, b.ID))
		if first.State != store.MemoStatePending {
			t.Fatalf("with the switch on: %+v", first)
		}
		if rec := e.do("a", "PUT", "/api/memo-queues/"+q, map[string]any{"instruction": "do it", "acceptOwnSessions": false}); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		// a never started one: held now.
		c := e.contSend("sw", "a2", "acme/app#21", "queue", "run-a", e.contRun(q, "run-a", store.DeliveryOpen, a.ID))
		if contHeldReason(t, c) != memoContHeldRoot {
			t.Errorf("a root started after the switch went off: %+v", c)
		}
		// b's chain, started already, goes on.
		next := e.contSend("sw", "b3", "acme/app#22", "queue", "run-b2", e.contRun(q, "run-b2", store.DeliveryOpen, first.ID))
		if next.State != store.MemoStatePending || next.ChainSeq != 2 {
			t.Errorf("a started chain stopped with the switch: %+v", next)
		}
	})
}

// A PUT that leaves continuationMax out keeps the stored value: an older Console tab must not
// turn a member's 0 back into the default.
func TestMemoContinuationMaxSurvivesAnOmittingPut(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 0, nil)
		if rec := e.do("a", "PUT", "/api/memo-queues/"+q, map[string]any{"instruction": "changed"}); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		for _, qq := range decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues {
			if qq.CategoryID == q && qq.ContinuationMax != 0 {
				t.Errorf("continuationMax = %d after an omitting PUT, want 0", qq.ContinuationMax)
			}
		}
	})
}

// A root stopped before its first continuation exists is kept whole by a tidy, and the next
// continuation is held. The count's place is taken once per delivery, even at M-1.
func TestMemoContinuationStopRootAndTidyAndLastPlace(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 1, nil)
		root := e.contRoot("impl", contRef)
		if rec := e.do("a", "POST", "/api/memos/"+root.ID+"/stop-chain", nil); rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
			rows, err := tx.ListMemosIn("r", "impl")
			if err != nil {
				return err
			}
			l, err := memoLinked(tx, rows)
			if !l[root.ID] {
				t.Errorf("a root with a chain row is not keep-only: %v", l)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if c := e.contSend("impl", "x", contRef, "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, root.ID)); contHeldReason(t, c) != memoContHeldStopped {
			t.Errorf("a stopped root admitted: %+v", c)
		}
		// Two admissions at M-1 (M=1 here, the chain empty): one place only.
		var won []bool
		if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
			for i := 0; i < 2; i++ {
				_, _, ok, err := tx.AdmitChain(store.MemoChain{ID: "free-root", CategoryID: q, CreatedAt: "2026-11-21T00:00:00Z"}, 1)
				if err != nil {
					return err
				}
				won = append(won, ok)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(won) != 2 || !won[0] || won[1] {
			t.Errorf("admissions at the last place = %v, want [true false]", won)
		}
	})
}

// The member's removal takes the chain rows with the rest of their memo tables.
func TestMemoContinuationChainRowsGoWithTheMember(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.contQueue("impl", 4, nil)
		root := e.contRoot("impl", contRef)
		e.contSend("impl", "p2", contRef, "queue", "run-1", e.contRun(q, "run-1", store.DeliveryOpen, root.ID))
		if _, _, found := e.chainRow(root.ID); !found {
			t.Fatal("no chain row")
		}
		if err := e.st.DeleteMembership(context.Background(), e.members["a"]); err != nil {
			t.Fatal(err)
		}
		if _, _, found := e.chainRow(root.ID); found {
			t.Errorf("the chain row outlived the member")
		}
	})
}
