package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// A start time a queue message carries (ADR 0137, addendum 2026-11-22, #4777).

func TestMemoStartAfterParse(t *testing.T) {
	now := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, in, want string
		bad            bool
	}{
		{name: "empty", in: "  ", want: ""},
		{name: "with an offset", in: "2026-12-04T09:00:00+09:00", want: "2026-12-04T00:00:00.000000Z"},
		{name: "utc with a fraction", in: "2026-12-04T00:00:00.5Z", want: "2026-12-04T00:00:00.500000Z"},
		{name: "already past", in: "2026-11-22T15:59:00+09:00", want: ""},
		{name: "now itself", in: "2026-11-22T07:00:00Z", want: ""},
		{name: "no offset", in: "2026-12-04T09:00:00", bad: true},
		{name: "a date alone", in: "2026-12-04", bad: true},
		{name: "a minute of 60 in the offset", in: "2026-12-04T09:00:00+09:60", bad: true},
		{name: "an offset of 24 hours", in: "2026-12-04T09:00:00+24:00", bad: true},
		{name: "a negative offset", in: "2026-12-03T19:00:00-05:00", want: "2026-12-04T00:00:00.000000Z"},
		{name: "past the bound", in: now.Add(memoafter.StartAfterMaxAhead + time.Minute).Format(time.RFC3339), bad: true},
		{name: "on the bound", in: now.Add(memoafter.StartAfterMaxAhead).Format(time.RFC3339), want: store.DeliveryTS(now.Add(memoafter.StartAfterMaxAhead))},
	} {
		got, aerr := memoStartAfterParse(tc.in, now)
		if tc.bad {
			if aerr == nil || aerr.code != errCodeMemoStartAfterInvalid || aerr.status != http.StatusBadRequest {
				t.Errorf("%s: got %q %v, want a refusal", tc.name, got, aerr)
			}
			continue
		}
		if aerr != nil || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", tc.name, got, aerr, tc.want)
		}
	}
}

// A message without a start time hashes as it always did, so no release recorded before the
// column existed goes stale; one with a time hashes differently for each time.
func TestMemoStartAfterInTheContentHash(t *testing.T) {
	m := store.Memo{Kind: "text", Body: "check the keys", Repo: "app", Category: "impl"}
	plain := memoContentHash(m)
	m.StartAfter = "2026-12-04T00:00:00.000000Z"
	at := memoContentHash(m)
	m.StartAfter = "2026-12-05T00:00:00.000000Z"
	if plain == at || at == memoContentHash(m) {
		t.Fatal("the start time is not part of the content hash")
	}
	// The formula before the column: the same parts, nothing appended.
	b, _ := json.Marshal([]string{"memo-content/v1", "text", "check the keys", "", "", "app", "impl"})
	if sum := sha256.Sum256(b); plain != hex.EncodeToString(sum[:]) {
		t.Fatal("a message without a start time hashes differently than before the column")
	}
}

// A session's message with a start time waits for its release; the release keeps the time, and
// neither the dispatcher nor "fire now" claims it before the time. The queue says why, the SQL
// statistics and the Go side agree, and once the time has passed the message is claimed.
func TestMemoStartAfterSurvivesTheReleaseAndHoldsTheClaim(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"enabled": true})
		start := time.Now().Add(3 * time.Hour).Truncate(time.Second)
		body := textMemo("app", "impl", "check the keys are gone")
		body["startAfter"] = start.In(time.FixedZone("JST", 9*3600)).Format(time.RFC3339)
		rec := e.memoTokenDo("a", "POST", "/internal/memos", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		m := decode[memoDTO](t, rec)
		if m.State != store.MemoStateHeld || m.StartAfter != store.DeliveryTS(start) {
			t.Fatalf("created %+v", m)
		}
		if rec := e.do("a", "POST", "/api/memos/release", map[string]any{"items": []map[string]any{{"id": m.ID, "contentHash": m.ContentHash}}}); rec.Code != http.StatusOK {
			t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
		}
		row := e.memoRow(m.ID)
		if row.State != store.MemoStatePending || row.StartAfter != store.DeliveryTS(start) || row.NotBefore != "" {
			t.Fatalf("the release lost the start time: %+v", row)
		}
		if got := e.listed(m.ID); got.StartAfter != row.StartAfter {
			t.Fatalf("listed startAfter %q", got.StartAfter)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict {
			t.Fatalf("fire now claimed a message before its start time: %d %s", rec.Code, rec.Body.String())
		}
		if w := e.queueWaiting(q); w == nil || w.Reason != waitScheduled || w.Until != untilOf(start) || w.Detail == nil || w.Detail.Scheduled != 1 {
			t.Fatalf("waiting %+v", w)
		}
		if fb := e.queueFireBlock(q); fb == nil || fb.Reason != waitScheduled || fb.Until != untilOf(start) {
			t.Fatalf("fireBlock %+v", fb)
		}
		sql, goSide := e.statsOf(q)
		if sql.Eligible != 0 || sql.Scheduled != 1 || sql.Deferred != 0 || goSide.Scheduled != 1 || goSide.Eligible != 0 || sql.NextAt != goSide.NextAt || sql.NextAt != row.StartAfter {
			t.Fatalf("stats sql %+v go %+v", sql, goSide)
		}
		// The time passes: the message is claimed as any other. The row is moved back in time
		// with the hash of the release it would have had, or the claim sends it back to held.
		row.StartAfter = store.DeliveryTS(time.Now().Add(-time.Minute))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET start_after=?, release_start_after=?, release_hash=? WHERE id=?`,
			row.StartAfter, row.StartAfter, memoContentHash(row), m.ID); err != nil {
			t.Fatal(err)
		}
		if sql, _ := e.statsOf(q); sql.Eligible != 1 || sql.Scheduled != 0 {
			t.Fatalf("stats after the time %+v", sql)
		}
		if d := e.fired(q); !sameIDs(d.MessageIDs, m.ID) {
			t.Fatalf("fired %v", d.MessageIDs)
		}
	})
}

// A hand-back and a start time on the same message: it waits for the later of the two, and the
// queue reads it as handed back.
func TestMemoStartAfterWithAHandBack(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", map[string]any{"enabled": true})
		m := e.pending("app", "impl", "one")
		later := store.DeliveryTS(time.Now().Add(5 * time.Hour))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET start_after=?, release_start_after=?, not_before=? WHERE id=?`,
			later, later, store.DeliveryTS(time.Now().Add(time.Hour)), m.ID); err != nil {
			t.Fatal(err)
		}
		sql, goSide := e.statsOf(q)
		if sql.Deferred != 1 || sql.Scheduled != 0 || goSide.Deferred != 1 || goSide.Scheduled != 0 || sql.NextAt != later || goSide.NextAt != later {
			t.Fatalf("stats sql %+v go %+v", sql, goSide)
		}
		if w := e.queueWaiting(q); w == nil || w.Reason != waitDeferred {
			t.Fatalf("waiting %+v", w)
		}
		// The hand-back's time passes, the start time has not: still not claimable.
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET not_before=? WHERE id=?`,
			store.DeliveryTS(time.Now().Add(-time.Minute)), m.ID); err != nil {
			t.Fatal(err)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict {
			t.Fatalf("fired before the start time: %d %s", rec.Code, rec.Body.String())
		}
		if w := e.queueWaiting(q); w == nil || w.Reason != waitScheduled {
			t.Fatalf("waiting %+v", w)
		}
	})
}

// Changing the start time is an edit of the content: from a session it sends a released message
// back to held, from the Console it re-records the member's own release. "" clears it, and a
// refused time changes nothing.
func TestMemoStartAfterEdits(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		m := e.pending("app", "impl", "one")
		start := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
		rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"startAfter": start.Format(time.RFC3339)})
		if rec.Code != http.StatusOK {
			t.Fatalf("console patch: %d %s", rec.Code, rec.Body.String())
		}
		if got := decode[memoDTO](t, rec); got.State != store.MemoStatePending || got.StartAfter != store.DeliveryTS(start) || got.ContentHash == m.ContentHash {
			t.Fatalf("after the Console's edit %+v", got)
		}
		if rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"startAfter": "2026-12-04 09:00"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("a time without an offset: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(m.ID); row.StartAfter != store.DeliveryTS(start) || row.State != store.MemoStatePending {
			t.Fatalf("a refused edit changed the row %+v", row)
		}
		rec = e.memoTokenDo("a", "PATCH", "/internal/memos/"+m.ID, map[string]any{"startAfter": start.Add(time.Hour).Format(time.RFC3339)})
		if rec.Code != http.StatusOK {
			t.Fatalf("token patch: %d %s", rec.Code, rec.Body.String())
		}
		if got := decode[memoDTO](t, rec); got.State != store.MemoStateHeld || got.StartAfter != store.DeliveryTS(start.Add(time.Hour)) {
			t.Fatalf("a session's change of the time kept the release: %+v", got)
		}
		rec = e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"startAfter": ""})
		if got := decode[memoDTO](t, rec); rec.Code != http.StatusOK || got.StartAfter != "" {
			t.Fatalf("clear: %d %+v", rec.Code, got)
		}
		// Leaving the queue drops it: a plain note waits for nothing.
		e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"startAfter": start.Format(time.RFC3339)})
		rec = e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"category": "notes"})
		if got := decode[memoDTO](t, rec); rec.Code != http.StatusOK || got.State != "" || got.StartAfter != "" {
			t.Fatalf("moved out: %d %+v", rec.Code, got)
		}
	})
}

// A tidy run keeps a message with a start time as it is: a merge would drop or widen the time.
func TestMemoStartAfterIsLinkedForTidy(t *testing.T) {
	rows := []store.Memo{{ID: "a"}, {ID: "b", StartAfter: "2026-12-04T00:00:00.000000Z"}}
	if got := memoafter.Linked(rows); got["a"] || !got["b"] {
		t.Fatalf("linked %v", got)
	}
}

// A time given outside every queue is refused, on a create and on an edit, rather than dropped: the
// caller would take a note that waits for nothing for one that waits.
func TestMemoStartAfterRefusedOutsideAQueue(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		e.queueWith("app", "impl", nil)
		at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		body := textMemo("app", "notes", "plain")
		body["startAfter"] = at
		if rec := e.do("a", "POST", "/api/memos", body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errCodeMemoStartAfterInvalid) {
			t.Fatalf("create outside a queue: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.memoTokenDo("a", "POST", "/internal/memos", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("token create outside a queue: %d %s", rec.Code, rec.Body.String())
		}
		rec := e.do("a", "POST", "/api/memos", textMemo("app", "notes", "plain"))
		plain := decode[memoDTO](t, rec)
		if rec := e.do("a", "PATCH", "/api/memos/"+plain.ID, map[string]any{"startAfter": at}); rec.Code != http.StatusBadRequest {
			t.Fatalf("patch outside a queue: %d %s", rec.Code, rec.Body.String())
		}
		// Moving out with a new time at once is refused too; a clear goes through.
		m := e.pending("app", "impl", "queued")
		if rec := e.do("a", "PATCH", "/api/memos/"+m.ID, map[string]any{"category": "notes", "startAfter": at}); rec.Code != http.StatusBadRequest {
			t.Fatalf("move out with a time: %d %s", rec.Code, rec.Body.String())
		}
		if row := e.memoRow(m.ID); row.Category != "impl" {
			t.Fatalf("a refused move moved the row: %+v", row)
		}
		if rec := e.do("a", "PATCH", "/api/memos/"+plain.ID, map[string]any{"startAfter": ""}); rec.Code != http.StatusOK {
			t.Fatalf("clear outside a queue: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// A row in a hand send is no part of the timed counters, on the SQL side as on the Go side.
func TestMemoStartAfterStatsSkipAHandSend(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		q := e.queueWith("app", "impl", nil)
		m := e.pending("app", "impl", "one")
		at := store.DeliveryTS(time.Now().Add(time.Hour))
		row := e.memoRow(m.ID)
		row.StartAfter = at
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET start_after=?, release_start_after=?, release_hash=?, send_token='t', send_started_at=? WHERE id=?`,
			at, at, memoContentHash(row), store.NowTS(), m.ID); err != nil {
			t.Fatal(err)
		}
		// Before its time and after it, the row in flight is neither blocked, nor timed, nor
		// claimable, and "Run now" says nothing waits; it still keeps a drain going.
		for _, phase := range []string{"before", "after"} {
			if phase == "after" {
				row.StartAfter = store.DeliveryTS(time.Now().Add(-time.Minute))
				if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET start_after=?, release_start_after=?, release_hash=? WHERE id=?`,
					row.StartAfter, row.StartAfter, memoContentHash(row), m.ID); err != nil {
					t.Fatal(err)
				}
			}
			sql, goSide := e.statsOf(q)
			if sql.Held != 1 || goSide.Held != 1 || sql.Count != 0 || goSide.Count != 0 || sql.Eligible != 0 || goSide.Eligible != 0 ||
				sql.Scheduled != 0 || sql.NextAt != "" || goSide.Scheduled != 0 || goSide.NextAt != "" {
				t.Fatalf("%s: stats sql %+v go %+v", phase, sql, goSide)
			}
			if fb := e.queueFireBlock(q); fb == nil || fb.Reason != waitNothing {
				t.Fatalf("%s: fireBlock %+v", phase, fb)
			}
		}
	})
}

// A draining queue whose one pending message is in a hand send stays draining: the send may fail
// and put the row back, and a threshold queue would then wait for a threshold it already passed.
// It ends once nothing is pending at all.
func TestMemoDrainHeldByAHandSend(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 5}, 0)
		m := e.pending("app", "impl", "one")
		if err := e.st.SetMemoQueueDraining(context.Background(), q, true); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET send_token='t', send_started_at=? WHERE id=?`, store.NowTS(), m.ID); err != nil {
			t.Fatal(err)
		}
		e.autoTick(time.Now())
		if !e.queueRow(q).Draining {
			t.Fatal("draining ended while a hand send held the only pending message")
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `DELETE FROM memo WHERE id=?`, m.ID); err != nil {
			t.Fatal(err)
		}
		e.autoTick(time.Now())
		if e.queueRow(q).Draining {
			t.Fatal("still draining with nothing pending")
		}
	})
}
