package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The manual Tidy (ADR 0137 section 8): POST /api/memo-tidy/apply. Each test runs on SQLite and,
// with AF_TEST_DATABASE_URL, on Postgres.

func hashed(rows ...store.Memo) []map[string]any {
	out := []map[string]any{}
	for _, m := range rows {
		out = append(out, map[string]any{"id": m.ID, "contentHash": memoContentHash(m)})
	}
	return out
}

func opMerge(text string, ids ...string) map[string]any {
	return map[string]any{"op": "merge", "ids": ids, "text": text}
}
func opSplit(id string, texts ...string) map[string]any {
	return map[string]any{"op": "split", "id": id, "texts": texts}
}
func opMove(id, to string) map[string]any { return map[string]any{"op": "move", "id": id, "to": to} }

// manualApply posts the approved preview of member who.
func (e *inboxEnv) manualApply(who, repo, category string, rows []store.Memo, ops ...map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	if ops == nil {
		ops = []map[string]any{}
	}
	return e.do(who, "POST", "/api/memo-tidy/apply", map[string]any{"repo": repo, "category": category, "items": hashed(rows...), "operations": ops})
}

// snapshot is every memo of member a without the seen mark, which a run writes whatever it does.
func (e *inboxEnv) snapshot() map[string]store.Memo {
	out := e.allMemos()
	for id, m := range out {
		m.TidySeenHash = ""
		out[id] = m
	}
	return out
}

func (e *inboxEnv) runCount(cat string) int { return len(e.runs(cat).Runs) }

type tidyErr struct {
	Error struct {
		Code     string   `json:"code"`
		Stale    []string `json:"stale"`
		TidyCode string   `json:"tidyCode"`
	} `json:"error"`
}

// A merge, a split, three moves and a keep are applied as one manual run on a category whose tidy
// setting is off: the effects are the ones an automatic run has, the answer carries every message the
// run touched, and the run list's undo takes it all back.
func TestMemoTidyManualApplyAndUndo(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		listed, unlisted, plain := e.category("r", "listed"), e.category("r", "unlisted"), e.category("r", "plain")
		e.queue(listed)
		e.queue(unlisted)
		if rec := e.do("a", "PUT", "/api/memo-queues/"+q, map[string]any{"instruction": "x", "tidy": false, "tidyDestinations": []string{listed}}); rec.Code != 200 {
			t.Fatalf("queue: %d %s", rec.Code, rec.Body.String())
		}
		a := e.tidyRow("r", "q", "first", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "second", "member", "", "", store.MemoStatePending)
		c := e.tidyRow("r", "q", "one two", "member", "", "", store.MemoStatePending)
		d := e.tidyRow("r", "q", "to listed", "member", "", "", store.MemoStatePending)
		f := e.tidyRow("r", "q", "to unlisted", "member", "", "", store.MemoStatePending)
		g := e.tidyRow("r", "q", "to plain", "member", "", "", store.MemoStatePending)
		k := e.tidyRow("r", "q", "kept", "member", "", "", store.MemoStatePending)
		rows := []store.Memo{a, b, c, d, f, g, k}
		before := e.snapshot()

		rec := e.manualApply("a", "r", "q", rows, opMerge("first and second", a.ID, b.ID), opSplit(c.ID, "one", "two"),
			opMove(d.ID, listed), opMove(f.ID, unlisted), opMove(g.ID, plain), map[string]any{"op": "keep", "id": k.ID})
		if rec.Code != http.StatusOK {
			t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
		}
		got := decode[memoTidyApplyWire](t, rec)
		if got.Run.Trigger != store.TidyTriggerManual || got.Run.State != store.TidyRunApplied || got.Run.Merged != 1 || got.Run.Split != 1 || got.Run.Moved != 3 {
			t.Fatalf("run: %+v", got.Run)
		}
		// 3 sources superseded, 3 derived rows, 3 moved rows; the kept one is not touched.
		if len(got.Memos) != 9 {
			t.Fatalf("memos: %d", len(got.Memos))
		}
		after := e.allMemos()
		if after[d.ID].State != store.MemoStatePending || after[d.ID].Category != "listed" || after[d.ID].ReleasedBy != store.MemoReleasedByTidy {
			t.Fatalf("listed move: %+v", after[d.ID])
		}
		if after[f.ID].State != store.MemoStateHeld || after[f.ID].TidyFrom != q {
			t.Fatalf("unlisted move: %+v", after[f.ID])
		}
		if after[g.ID].State != "" || after[g.ID].Category != "plain" {
			t.Fatalf("plain move: %+v", after[g.ID])
		}
		if after[a.ID].State != store.MemoStateSuperseded || after[c.ID].State != store.MemoStateSuperseded {
			t.Fatal("sources are not superseded")
		}
		if after[k.ID].State != store.MemoStatePending || after[k.ID].Category != "q" {
			t.Fatalf("kept: %+v", after[k.ID])
		}
		for id := range before { // conservation: no row is deleted
			if _, ok := after[id]; !ok {
				t.Fatalf("row %s was deleted", id)
			}
		}
		if rec := e.undo(got.Run.ID); rec.Code != http.StatusOK {
			t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
		}
		again := e.snapshot()
		for id, m := range before {
			m.Position, again[id] = 0, withPosition(again[id], 0) // an undo puts a moved message at the end of its queue
			if !reflect.DeepEqual(m, again[id]) {
				t.Fatalf("row %s after undo:\n%+v\nbefore:\n%+v", id, again[id], m)
			}
		}
	})
}

// A manual run is never refused by the daily cap and does not use up the automatic runs' budget.
func TestMemoTidyManualIgnoresTheDailyCap(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		for i := 0; i < 10; i++ {
			if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
				return tx.InsertTidyRun(store.MemoTidyRun{ID: store.NewID(), QueueID: q, Trigger: "auto", State: store.TidyRunKept, CreatedAt: now, UpdatedAt: now})
			}); err != nil {
				t.Fatal(err)
			}
		}
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		if rec := e.manualApply("a", "r", "q", []store.Memo{a, b}, opMerge("ab", a.ID, b.ID)); rec.Code != 200 {
			t.Fatalf("apply at the cap: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.runs(q); got.UsedToday != 10 || len(got.Runs) != 11 {
			t.Fatalf("a manual run used the automatic budget: %+v", got.UsedToday)
		}
	})
}

// A held message can be tidied by hand: a derived message from sources that mix waiting and
// approval-pending ones waits for approval, and a held message stays held when moved.
func TestMemoTidyManualHeldSources(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		listed := e.category("r", "listed")
		e.queue(listed)
		e.tidyQueue(q, listed)
		p := e.tidyRow("r", "q", "waiting", "member", "", "", store.MemoStatePending)
		h := e.tidyRow("r", "q", "approval", "member", "", "", store.MemoStateHeld)
		h2 := e.tidyRow("r", "q", "approval two", "member", "", "", store.MemoStateHeld)
		rec := e.manualApply("a", "r", "q", []store.Memo{p, h, h2}, opMerge("mixed", p.ID, h.ID), opMove(h2.ID, listed))
		if rec.Code != 200 {
			t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
		}
		got := decode[memoTidyApplyWire](t, rec)
		after := e.allMemos()
		for _, id := range got.Run.ResultIDs {
			if after[id].State != store.MemoStateHeld {
				t.Fatalf("a derived message of mixed sources is %q", after[id].State)
			}
		}
		if after[h2.ID].State != store.MemoStateHeld || after[h2.ID].Category != "listed" {
			t.Fatalf("held move: %+v", after[h2.ID])
		}
	})
}

// A message that differs from the preview refuses the whole call and changes nothing: no run, no
// seen mark, no row.
func TestMemoTidyManualStaleChangesNothing(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		c := e.tidyRow("r", "q", "c", "member", "", "", store.MemoStatePending)
		rows := []store.Memo{a, b, c}
		// b is edited, c is claimed by a delivery, after the preview.
		if rec := e.do("a", "PATCH", "/api/memos/"+b.ID, map[string]any{"body": "b edited"}); rec.Code != 200 {
			t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
		}
		e.setState(c.ID, store.MemoStateClaimed)
		before := e.allMemos()
		rec := e.manualApply("a", "r", "q", rows, opMerge("abc", a.ID, b.ID, c.ID))
		if rec.Code != http.StatusConflict {
			t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
		}
		got := decode[tidyErr](t, rec)
		if got.Error.Code != errCodeMemoTidyStale || !reflect.DeepEqual(sortedIDs(got.Error.Stale), sortedIDs([]string{b.ID, c.ID})) {
			t.Fatalf("answer: %+v", got)
		}
		if !reflect.DeepEqual(before, e.allMemos()) || e.runCount(q) != 0 {
			t.Fatal("a stale call changed something")
		}
	})
}

// The messages are what the member saw, but a change between this route's read and apply's lock is
// caught by apply as well.
func TestMemoTidyManualStaleInsideApply(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		in := tidyItems(a, b)
		in[1].Hash = "not-the-hash"
		out, err := memoTidyApply(context.Background(), e.st, memoTidyApplyIn{MembershipID: e.members["a"], QueueID: q,
			Trigger: store.TidyTriggerManual, Input: in, Ops: nil})
		if err != nil || out.Run.State != store.TidyRunDropped || out.Run.Reason != memoTidyReasonStaleInput {
			t.Fatalf("apply: %+v %v", out.Run, err)
		}
	})
}

// A reply the validator refuses answers 400 with its code, and the rows are what they were except
// the seen marks; a reply that is not of the agreed shape never reaches a run.
func TestMemoTidyManualInvalidChangesNothing(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "from me", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "from a session", "workspace", "s1", "o", store.MemoStatePending)
		rows := []store.Memo{a, b}
		before := e.snapshot()

		rec := e.manualApply("a", "r", "q", rows, opMerge("both", a.ID, b.ID))
		got := decode[tidyErr](t, rec)
		if rec.Code != http.StatusBadRequest || got.Error.Code != errCodeMemoTidyInvalid || got.Error.TidyCode != "mixed_group" {
			t.Fatalf("mixed group: %d %s", rec.Code, rec.Body.String())
		}
		if !reflect.DeepEqual(before, e.snapshot()) {
			t.Fatal("a refused reply changed a row")
		}
		if runs := e.runs(q).Runs; len(runs) != 1 || runs[0].State != store.TidyRunDropped || runs[0].Trigger != store.TidyTriggerManual || runs[0].Reason != "mixed_group" {
			t.Fatalf("runs: %+v", runs)
		}

		// An unknown id, an operation of an unknown kind and a field that is not one.
		e.category("r", "other")
		stray := e.tidyRow("r", "other", "elsewhere", "member", "", "", store.MemoStatePending)
		before = e.snapshot()
		for name, ops := range map[string][]map[string]any{
			"unknown id":   {opMerge("x", a.ID, stray.ID)},
			"unknown kind": {{"op": "rewrite", "id": a.ID, "text": "x"}},
			"stray field":  {{"op": "keep", "id": a.ID, "extra": 1}},
			"move self":    {opMove(a.ID, q)},
		} {
			rec := e.manualApply("a", "r", "q", rows, ops...)
			if rec.Code != http.StatusBadRequest || decode[tidyErr](t, rec).Error.Code != errCodeMemoTidyInvalid {
				t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
			}
		}
		if rec := e.do("a", "POST", "/api/memo-tidy/apply", map[string]any{"repo": "r", "category": "q", "items": hashed(a), "operations": map[string]any{"op": "keep"}}); rec.Code != 400 || decode[tidyErr](t, rec).Error.TidyCode != "bad_shape" {
			t.Fatalf("operations that is not a list: %d %s", rec.Code, rec.Body.String())
		}
		if !reflect.DeepEqual(before, e.snapshot()) {
			t.Fatal("a refused reply changed a row")
		}
	})
}

// A category that is no auto-run category, another member's category or message, and a message of
// another category are 404 and change nothing.
func TestMemoTidyManualScope(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		e.category("r", "plain")
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		before := e.allMemos()
		if rec := e.manualApply("a", "r", "plain", []store.Memo{a, b}, opMerge("ab", a.ID, b.ID)); rec.Code != 404 {
			t.Fatalf("plain category: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.manualApply("a", "r", "nope", []store.Memo{a, b}); rec.Code != 404 {
			t.Fatalf("unknown category: %d", rec.Code)
		}
		e.foreignQueue()
		if rec := e.manualApply("b", "r", "q", []store.Memo{a, b}); rec.Code != 404 {
			t.Fatalf("another member's category: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.manualApply("b", "r", "theirs", []store.Memo{a, b}, opMerge("ab", a.ID, b.ID)); rec.Code != 404 {
			t.Fatalf("another member's messages: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.manualApply("a", "r", "q", []store.Memo{{ID: "ghost"}}); rec.Code != 404 {
			t.Fatalf("a message that does not exist: %d", rec.Code)
		}
		if !reflect.DeepEqual(before, e.allMemos()) || e.runCount(q) != 0 {
			t.Fatal("a refused call changed something")
		}
	})
}

// Bad bodies are 400 before anything is read, and an automatic run in flight is a 409 with no change.
func TestMemoTidyManualBodyAndBusy(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		if rec := e.manualApply("a", "r", "q", nil); rec.Code != 400 {
			t.Fatalf("no items: %d", rec.Code)
		}
		if rec := e.manualApply("a", "r", "q", []store.Memo{a, a}); rec.Code != 400 {
			t.Fatalf("duplicate item: %d", rec.Code)
		}
		many := make([]store.Memo, 41)
		for i := range many {
			many[i] = a
		}
		if rec := e.manualApply("a", "r", "q", many); rec.Code != 400 {
			t.Fatalf("too many items: %d", rec.Code)
		}
		now := store.NowTS()
		if err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
			return tx.InsertTidyRun(store.MemoTidyRun{ID: store.NewID(), QueueID: q, Trigger: "auto", State: store.TidyRunRunning, CreatedAt: now, UpdatedAt: now})
		}); err != nil {
			t.Fatal(err)
		}
		before := e.allMemos()
		rec := e.manualApply("a", "r", "q", []store.Memo{a, b}, opMerge("ab", a.ID, b.ID))
		if rec.Code != 409 || decode[tidyErr](t, rec).Error.Code != errCodeMemoTidyBusy {
			t.Fatalf("busy: %d %s", rec.Code, rec.Body.String())
		}
		if !reflect.DeepEqual(before, e.allMemos()) {
			t.Fatal("a busy call changed a row")
		}
	})
}

// The route is the Console's: the token face has none (the existing /mcp test names no tidy tool).
func TestMemoTidyManualApplyIsConsoleOnly(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		body := map[string]any{"repo": "r", "category": "q", "items": hashed(a, b), "operations": []map[string]any{opMerge("ab", a.ID, b.ID)}}
		if rec := e.memoTokenDo("a", "POST", "/internal/memo-tidy/apply", body); rec.Code != 404 && rec.Code != 405 {
			t.Fatalf("token face: %d", rec.Code)
		}
		if rec := e.memoTokenDo("a", "POST", "/api/memo-tidy/apply", body); rec.Code == 200 {
			t.Fatalf("the token face reached the Console route: %d", rec.Code)
		}
		if e.memoRow(a.ID).State != store.MemoStatePending {
			t.Fatal("applied through a face that may not")
		}
	})
}

func sortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func withPosition(m store.Memo, p int) store.Memo { m.Position = p; return m }

// The destinations a manual Tidy offers are the automatic run's: the listed queues first, then the
// categories of the queue's own repository, never the queue itself, at most MaxDestinations.
func TestMemoTidyDestinationsAreSharedRule(t *testing.T) {
	own := store.MemoCategory{ID: "own", Repo: "r", Name: "q"}
	cats := []store.MemoCategory{own, {ID: "a", Repo: "r", Name: "a"}, {ID: "far", Repo: "other", Name: "far"}, {ID: "bad", Repo: "r", Name: "x\ny"}, {ID: "unlisted", Repo: "other", Name: "u"}}
	queues := []store.MemoQueue{{CategoryID: "far"}, {CategoryID: "own"}}
	got := memoTidyDestinations(own, []string{"far", "own", "gone", "far"}, cats, queues)
	want := []memoTidyWireDest{{ID: "far", Name: "other/far", Queue: true}, {ID: "a", Name: "r/a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	many := []store.MemoCategory{own}
	for i := 0; i < 150; i++ {
		many = append(many, store.MemoCategory{ID: string(rune('A'+i%26)) + string(rune('a'+i/26)), Repo: "r", Name: "c"})
	}
	if n := len(memoTidyDestinations(own, nil, many, nil)); n != 100 {
		t.Fatalf("offered %d", n)
	}
}

// Like undo, the manual apply does not read the tenant's memo-queue switch: queue rows exist, and
// what is in them can be tidied whatever the switch says now.
func TestMemoTidyManualIgnoresTheTenantSwitch(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		e.memoQueuesOn(false)
		if rec := e.manualApply("a", "r", "q", []store.Memo{a, b}, opMerge("ab", a.ID, b.ID)); rec.Code != 200 {
			t.Fatalf("apply with the switch off: %d %s", rec.Code, rec.Body.String())
		}
	})
}

// The run in the apply answer carries the same undo verdict the run list gives right after: an applied
// run nothing blocks says undoable, a kept run does not.
func TestMemoTidyManualApplyAnswersUndoVerdict(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		if rec := e.do("a", "PUT", "/api/memo-queues/"+q, map[string]any{"instruction": "x", "tidy": false}); rec.Code != 200 {
			t.Fatalf("queue: %d %s", rec.Code, rec.Body.String())
		}
		a := e.tidyRow("r", "q", "first", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "second", "member", "", "", store.MemoStatePending)
		k := e.tidyRow("r", "q", "kept", "member", "", "", store.MemoStatePending)

		rec := e.manualApply("a", "r", "q", []store.Memo{a, b}, opMerge("first and second", a.ID, b.ID))
		if rec.Code != http.StatusOK {
			t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
		}
		applied := decode[memoTidyApplyWire](t, rec).Run
		if applied.State != store.TidyRunApplied || !applied.Undoable || len(applied.Blocked) != 0 {
			t.Fatalf("applied answer: %+v", applied)
		}

		rec = e.manualApply("a", "r", "q", []store.Memo{k}, map[string]any{"op": "keep", "id": k.ID})
		if rec.Code != http.StatusOK {
			t.Fatalf("keep: %d %s", rec.Code, rec.Body.String())
		}
		kept := decode[memoTidyApplyWire](t, rec).Run
		if kept.State == store.TidyRunApplied || kept.Undoable {
			t.Fatalf("kept answer: %+v", kept)
		}

		for _, r := range e.runs(q).Runs {
			want := map[string]memoTidyRunDTO{applied.ID: applied, kept.ID: kept}[r.ID]
			if r.Undoable != want.Undoable || len(r.Blocked) != len(want.Blocked) {
				t.Fatalf("run %s: list says undoable=%v blocked=%d, answer said undoable=%v blocked=%d",
					r.ID, r.Undoable, len(r.Blocked), want.Undoable, len(want.Blocked))
			}
		}
	})
}
