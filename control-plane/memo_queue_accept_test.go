package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The per-queue switch "accept from sessions I started" (ADR 0137 section 3): which messages
// skip the release press, which never do, and what stays bounded. Every test runs on SQLite and,
// with AF_TEST_DATABASE_URL, on Postgres.

// acceptQueue makes a category a queue with the switch set and answers its id.
func (e *inboxEnv) acceptQueue(name string, on bool) string {
	e.t.Helper()
	cat := e.category("r", name)
	rec := e.do("a", "PUT", "/api/memo-queues/"+cat, map[string]any{"instruction": "do it", "acceptOwnSessions": on})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("put queue: %d %s", rec.Code, rec.Body.String())
	}
	return cat
}

// fromSession is a create on the token face the way the Agent's relay sends it.
func (e *inboxEnv) fromSession(category, body, origin string, extra map[string]any) (int, memoDTO) {
	e.t.Helper()
	m := textMemo("r", category, body)
	m["producer"], m["producerOrigin"] = "sess-1", origin
	for k, v := range extra {
		m[k] = v
	}
	rec := e.memoTokenDo("a", "POST", "/internal/memos", m)
	return rec.Code, decode[memoDTO](e.t, rec)
}

func TestMemoQueueAcceptOwnSessionsSettings(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		cat := e.category("r", "q")
		put := func(body map[string]any) memoQueueDTO {
			rec := e.do("a", "PUT", "/api/memo-queues/"+cat, body)
			if rec.Code != 200 {
				t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
			}
			return decode[memoQueueDTO](t, rec)
		}
		if put(map[string]any{"instruction": "x"}).AcceptOwnSessions {
			t.Fatal("the switch is on by default")
		}
		if !put(map[string]any{"instruction": "x", "acceptOwnSessions": true}).AcceptOwnSessions {
			t.Fatal("the switch did not turn on")
		}
		if l := decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues; len(l) != 1 || !l[0].AcceptOwnSessions {
			t.Fatalf("list does not carry the switch: %+v", l)
		}
		// A PUT that omits the field means off, like the other defaults.
		if put(map[string]any{"instruction": "x"}).AcceptOwnSessions {
			t.Fatal("an omitted switch kept the old value")
		}
		// Nothing outside the Console writes it.
		put(map[string]any{"instruction": "x", "acceptOwnSessions": true})
		e.memoTokenDo("a", "PUT", "/internal/memo-queues/"+cat, map[string]any{"instruction": "x", "acceptOwnSessions": false})
		if !decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues[0].AcceptOwnSessions {
			t.Fatal("the token face changed the switch")
		}
	})
}

// A row that predates the column reads as off.
func TestMemoQueueAcceptOwnSessionsDefaultsOffInTheDatabase(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		cat := e.category("r", "q")
		rec := e.do("a", "PUT", "/api/memo-queues/"+cat, map[string]any{"instruction": "x"})
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
		var n int
		if err := e.st.DB().QueryRowContext(context.Background(), `SELECT accept_own_sessions FROM memo_queue WHERE category_id=?`, cat).Scan(&n); err != nil || n != 0 {
			t.Fatalf("accept_own_sessions = %d, %v", n, err)
		}
	})
}

// The matrix of the rule on create: the switch x the producer origin x the face.
func TestMemoQueueAcceptOwnSessionsMatrix(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		on, off := e.acceptQueue("on", true), e.acceptQueue("off", false)
		_, _ = on, off
		origins := []string{"user", "queue", "schedule", "session", "operator", "handoff", "unknown", "", "User", " user "}
		n := 0
		for _, c := range []struct {
			cat string
			sw  bool
		}{{"on", true}, {"off", false}} {
			for _, origin := range origins {
				n++
				code, d := e.fromSession(c.cat, fmt.Sprintf("m%d", n), origin, nil)
				if code != http.StatusCreated {
					t.Fatalf("create: %d", code)
				}
				row := e.memoRow(d.ID)
				want := store.MemoStateHeld
				// The Control Plane trims what the relay sends, so " user " is the origin user.
				if c.sw && (origin == "user" || origin == " user ") {
					want = store.MemoStatePending
				}
				if row.State != want || row.Provenance != store.MemoProvenanceWorkspace {
					t.Errorf("switch %v origin %q: state %q provenance %q, want %q", c.sw, origin, row.State, row.Provenance, want)
				}
				if want == store.MemoStatePending {
					if row.ReleaseHash != memoContentHash(row) || row.ReleasedAt == "" || row.ReleasedBy != store.MemoReleasedBySetting {
						t.Errorf("accepted row does not carry its release: %+v", row)
					}
				} else if row.ReleaseHash != "" || row.ReleasedAt != "" || row.ReleasedBy != "" {
					t.Errorf("held row carries a release: %+v", row)
				}
			}
		}
		// /mcp never skips the press, the switch notwithstanding.
		if code, d := (memoFaceClient{"mcp", e}).create(textMemo("r", "on", "from mcp")); code != http.StatusCreated || e.memoRow(d.ID).State != store.MemoStateHeld {
			t.Fatalf("mcp create into a switched queue: %d %+v", code, e.memoRow(d.ID))
		}
		// The member's own note is pending as before, and a forged origin changes nothing about
		// who is accepted by the setting: the Console is not the setting.
		m := textMemo("r", "on", "member note")
		m["producerOrigin"] = "user"
		d := decode[memoDTO](t, e.do("a", "POST", "/api/memos", m))
		if row := e.memoRow(d.ID); row.State != store.MemoStatePending || row.ReleasedBy != "" {
			t.Fatalf("console note: %+v", row)
		}
		// A category that is not a queue has no states.
		e.category("r", "plain")
		_, d = e.fromSession("plain", "plain note", "user", nil)
		if row := e.memoRow(d.ID); row.State != "" || row.ReleasedBy != "" {
			t.Fatalf("plain note: %+v", row)
		}
		e.assertPendingReleaseMatches()
	})
}

// A pending row accepted by the switch keeps every invariant of a pending row, with and without
// a reference: the release_* copy, the hash, and the one-open-message-per-reference rule.
func TestMemoQueueAcceptOwnSessionsPendingInvariants(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.acceptQueue("on", true)
		for _, ref := range []map[string]any{nil, ghRef("1")} {
			extra := map[string]any{}
			if ref != nil {
				extra["reference"] = ref
			}
			code, d := e.fromSession("on", fmt.Sprintf("ref %v", ref != nil), "user", extra)
			if code != http.StatusCreated {
				t.Fatalf("create: %d", code)
			}
			row := e.memoRow(d.ID)
			if row.State != store.MemoStatePending || row.ReleaseHash != memoContentHash(row) {
				t.Fatalf("not pending with its hash: %+v", row)
			}
			if (row.RefKey != "") != (ref != nil) {
				t.Fatalf("reference: %+v", row)
			}
			var rel, relKey string
			if err := e.st.DB().QueryRowContext(context.Background(), `SELECT release_body, release_ref_key FROM memo WHERE id=?`, d.ID).Scan(&rel, &relKey); err != nil || rel != row.Body || relKey != row.RefKey {
				t.Fatalf("release copy: %q %q %v", rel, relKey, err)
			}
		}
		// The same reference again answers the message already there.
		code, d := e.fromSession("on", "other text", "user", map[string]any{"reference": ghRef("1")})
		if code != http.StatusOK || !d.Existing {
			t.Fatalf("duplicate reference: %d %+v", code, d)
		}
		// A press on an accepted row (Console) re-records it as the member's.
		e.assertPendingReleaseMatches()
	})
}

// Point 3: a change through the token face to a pending message.
func TestMemoQueueAcceptOwnSessionsChange(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.acceptQueue("on", true)
		e.acceptQueue("off", false)
		tok := memoFaceClient{"token", e}
		mcp := memoFaceClient{"mcp", e}
		console := memoFaceClient{"console", e}

		_, own := e.fromSession("on", "own", "user", nil)
		before := e.memoRow(own.ID)
		code, _ := tok.update(own.ID, map[string]any{"body": "own, changed"})
		after := e.memoRow(own.ID)
		if code != 200 || after.State != store.MemoStatePending || after.ReleaseHash != memoContentHash(after) || after.ReleaseHash == before.ReleaseHash || after.ReleasedBy != store.MemoReleasedBySetting {
			t.Fatalf("token change of an accepted message: %d %+v", code, after)
		}
		// /mcp: back to held.
		if code, _ := mcp.update(own.ID, map[string]any{"body": "own, via mcp"}); code != 200 {
			t.Fatalf("mcp update: %d", code)
		}
		if got := e.memoRow(own.ID); got.State != store.MemoStateHeld || got.ReleaseHash != "" || got.ReleasedBy != "" {
			t.Fatalf("mcp change: %+v", got)
		}
		// A message of another origin: pending by a press, then changed from the token face.
		_, other := e.fromSession("on", "other origin", "schedule", nil)
		if row := e.memoRow(other.ID); row.State != store.MemoStateHeld {
			t.Fatalf("other origin entered %q", row.State)
		}
		if r := e.release(other.ID, other.ContentHash); r.Code != 200 {
			t.Fatalf("release: %d", r.Code)
		}
		if row := e.memoRow(other.ID); row.State != store.MemoStatePending || row.ReleasedBy != "" {
			t.Fatalf("pressed: %+v", row)
		}
		tok.update(other.ID, map[string]any{"body": "other origin, changed"})
		if row := e.memoRow(other.ID); row.State != store.MemoStateHeld {
			t.Fatalf("token change of another origin's message: %q", row.State)
		}
		// The member's own note changed from the token face goes back to held as before.
		member := decode[memoDTO](t, e.do("a", "POST", "/api/memos", textMemo("r", "on", "member")))
		tok.update(member.ID, map[string]any{"body": "member, changed"})
		if row := e.memoRow(member.ID); row.State != store.MemoStateHeld {
			t.Fatalf("member note changed by the token face: %q", row.State)
		}
		// A change by the Console is the member's own: pending, not marked.
		_, own2 := e.fromSession("on", "own2", "user", nil)
		console.update(own2.ID, map[string]any{"body": "own2 by hand"})
		if row := e.memoRow(own2.ID); row.State != store.MemoStatePending || row.ReleasedBy != "" || row.ReleaseHash != memoContentHash(row) {
			t.Fatalf("console change: %+v", row)
		}
		e.assertPendingReleaseMatches()
	})
}

// A move between a category with the switch and one without follows the destination's switch.
func TestMemoQueueAcceptOwnSessionsMove(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.acceptQueue("on", true)
		e.acceptQueue("off", false)
		tok := memoFaceClient{"token", e}
		_, a := e.fromSession("on", "a", "user", nil)
		tok.update(a.ID, map[string]any{"category": "off"})
		if row := e.memoRow(a.ID); row.State != store.MemoStateHeld || row.ReleasedBy != "" || row.ReleaseHash != "" {
			t.Fatalf("move to a category without the switch: %+v", row)
		}
		_, b := e.fromSession("off", "b", "user", nil)
		if row := e.memoRow(b.ID); row.State != store.MemoStateHeld {
			t.Fatalf("entered the category without the switch as %q", row.State)
		}
		tok.update(b.ID, map[string]any{"category": "on"})
		// A held message does not become pending by a move: only an entering or a changed
		// pending message is decided by the switch.
		if row := e.memoRow(b.ID); row.State != store.MemoStateHeld {
			t.Fatalf("held message moved into the switch: %q", row.State)
		}
		// A pending message accepted in "on" moved to "on2" (switch on) stays pending.
		e.acceptQueue("on2", true)
		_, c := e.fromSession("on", "c", "user", nil)
		tok.update(c.ID, map[string]any{"category": "on2"})
		if row := e.memoRow(c.ID); row.State != store.MemoStatePending || row.ReleaseHash != memoContentHash(row) || row.ReleasedBy != store.MemoReleasedBySetting {
			t.Fatalf("move between switched categories: %+v", row)
		}
		// A plain note of a session moves into the switched category and enters pending.
		e.category("r", "plain")
		_, p := e.fromSession("plain", "p", "user", nil)
		tok.update(p.ID, map[string]any{"category": "on"})
		if row := e.memoRow(p.ID); row.State != store.MemoStatePending || row.ReleasedBy != store.MemoReleasedBySetting {
			t.Fatalf("plain note moved into the switch: %+v", row)
		}
		e.assertPendingReleaseMatches()
	})
}

// The marker is in the list answer, and a press or an edit by the member removes it.
func TestMemoQueueAcceptOwnSessionsMarkerInList(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.acceptQueue("on", true)
		_, d := e.fromSession("on", "marked", "user", nil)
		_, h := e.fromSession("on", "unmarked", "schedule", nil)
		list := decode[[]memoDTO](t, e.do("a", "GET", "/api/memos", nil))
		got := map[string]string{}
		for _, m := range list {
			got[m.ID] = m.ReleasedBy
		}
		if got[d.ID] != "setting" || got[h.ID] != "" {
			t.Fatalf("releasedBy in the list: %v", got)
		}
		if r := e.release(h.ID, h.ContentHash); r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		if e.memoRow(h.ID).ReleasedBy != "" {
			t.Fatal("a press carries the marker")
		}
	})
}

// The bounds on a create from the workspace stay in force with the switch on: a burst from one
// session is cut at the rate limit, an identical message is dropped, the body cap and the
// envelope refusal hold, and the queue's daily cap is a fire-side bound the switch does not touch.
func TestMemoQueueAcceptOwnSessionsBurstIsBounded(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.acceptQueue("on", true)
		for i := 0; i < memoQueueRatePerMinute; i++ {
			if code, _ := e.fromSession("on", fmt.Sprintf("burst %d", i), "user", nil); code != http.StatusCreated {
				t.Fatalf("create %d: %d", i, code)
			}
		}
		code, _ := e.fromSession("on", "one too many", "user", nil)
		if code != http.StatusTooManyRequests {
			t.Fatalf("burst past the rate limit: %d", code)
		}
		if got := e.count("memo"); got != memoQueueRatePerMinute {
			t.Fatalf("%d rows after the burst", got)
		}
		if code, d := e.fromSession("on", "burst 0", "user", nil); code != http.StatusOK || !d.Existing {
			t.Fatalf("identical message: %d %+v", code, d)
		}
		if code, _ := e.fromSession("on", "[agent-fleet:x] hi", "user", nil); code != http.StatusBadRequest {
			t.Fatalf("envelope: %d", code)
		}
	})
}

// Turning the switch on or off later does not touch the rows already there: a message the
// switch accepted stays pending when it goes off, and a held one stays held when it goes on.
func TestMemoQueueAcceptOwnSessionsToggleKeepsRows(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		cat := e.acceptQueue("q", true)
		put := func(on bool) {
			if r := e.do("a", "PUT", "/api/memo-queues/"+cat, map[string]any{"instruction": "do it", "acceptOwnSessions": on}); r.Code != 200 {
				t.Fatal(r.Body.String())
			}
		}
		_, acc := e.fromSession("q", "accepted", "user", nil)
		put(false)
		if row := e.memoRow(acc.ID); row.State != store.MemoStatePending || row.ReleasedBy != store.MemoReleasedBySetting {
			t.Fatalf("switch off recalled an accepted message: %+v", row)
		}
		_, held := e.fromSession("q", "held while off", "user", nil)
		if row := e.memoRow(held.ID); row.State != store.MemoStateHeld {
			t.Fatalf("entered %q with the switch off", row.State)
		}
		put(true)
		if row := e.memoRow(held.ID); row.State != store.MemoStateHeld {
			t.Fatalf("switch on released a held message: %q", row.State)
		}
		e.assertPendingReleaseMatches()
	})
}
