package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memotidy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Tidying before a fire, slice S1 (ADR 0137 section 8): apply, undo, the run list and the rules a
// superseded or derived row keeps on every write path. Each test runs on SQLite and, with
// AF_TEST_DATABASE_URL, on Postgres.

// tidyRow inserts a message of member a straight into a queue with the given provenance and
// producer, in state pending or held, the way a face would have left it.
func (e *inboxEnv) tidyRow(repo, category, body, prov, producer, origin, state string) store.Memo {
	e.t.Helper()
	m := store.Memo{ID: store.NewID(), MembershipID: e.members["a"], Repo: repo, Category: category, Kind: "text", Body: body,
		CreatedAt: store.NowTS(), State: state, Provenance: prov, Producer: producer, ProducerOrigin: origin}
	if state == store.MemoStatePending {
		m.ReleaseHash, m.ReleasedAt = memoContentHash(m), store.NowTS()
	}
	if err := e.st.CreateMemo(context.Background(), m); err != nil {
		e.t.Fatal(err)
	}
	return e.memoRow(m.ID)
}

// tidyQueue turns category cat into a queue with tidy on and the listed destinations.
func (e *inboxEnv) tidyQueue(cat string, dests ...string) memoQueueDTO {
	e.t.Helper()
	rec := e.do("a", "PUT", "/api/memo-queues/"+cat, map[string]any{"instruction": "do it", "tidy": true, "tidyDestinations": dests})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("put tidy queue: %d %s", rec.Code, rec.Body.String())
	}
	return decode[memoQueueDTO](e.t, rec)
}

func tidyItems(rows ...store.Memo) []memoTidyItem {
	out := make([]memoTidyItem, 0, len(rows))
	for _, m := range rows {
		out = append(out, memoTidyItem{ID: m.ID, Hash: memoContentHash(m), State: m.State})
	}
	return out
}

func (e *inboxEnv) tidyApply(queue, trigger string, rows []store.Memo, dests []string, ops ...memotidy.Op) memoTidyApplyOut {
	e.t.Helper()
	out, err := memoTidyApply(context.Background(), e.st, memoTidyApplyIn{MembershipID: e.members["a"], QueueID: queue, Trigger: trigger,
		Input: tidyItems(rows...), Destinations: dests, Ops: ops})
	if err != nil {
		e.t.Fatalf("apply: %v", err)
	}
	return out
}

func (e *inboxEnv) allMemos() map[string]store.Memo {
	e.t.Helper()
	rows, err := e.st.ListMemos(context.Background(), e.members["a"], "")
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]store.Memo{}
	for _, m := range rows {
		out[m.ID] = m
	}
	return out
}

func (e *inboxEnv) queueRow(cat string) store.MemoQueue {
	e.t.Helper()
	qs, err := e.st.ListMemoQueues(context.Background(), e.members["a"])
	if err != nil {
		e.t.Fatal(err)
	}
	for _, q := range qs {
		if q.CategoryID == cat {
			return q
		}
	}
	e.t.Fatalf("no queue %s", cat)
	return store.MemoQueue{}
}

func (e *inboxEnv) undo(runID string) *httptest.ResponseRecorder {
	return e.do("a", "POST", "/api/memo-tidy-runs/"+runID+"/undo", nil)
}

type tidyBlocked struct {
	Error struct {
		Code    string          `json:"code"`
		Blocked []memoTidyBlock `json:"blocked"`
	} `json:"error"`
}

func (e *inboxEnv) runs(cat string) memoTidyRunsWire {
	e.t.Helper()
	rec := e.do("a", "GET", "/api/memo-queues/"+cat+"/tidy-runs", nil)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("run list: %d %s", rec.Code, rec.Body.String())
	}
	return decode[memoTidyRunsWire](e.t, rec)
}

func merge(text string, ids ...string) memotidy.Op {
	return memotidy.Op{Op: memotidy.OpMerge, IDs: ids, Text: text}
}
func move(id, to string) memotidy.Op { return memotidy.Op{Op: memotidy.OpMove, ID: id, To: to} }

// A merge, a split, three moves and a keep apply together, every pending row's release still matches
// its content, no row is deleted, the queue's settings are untouched, and the undo puts all of it back.
func TestMemoTidyApplyAndUndoRoundTrip(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		listed, unlisted, plain := e.category("r", "listed"), e.category("r", "unlisted"), e.category("r", "plain")
		e.queue(listed)
		e.queue(unlisted)
		e.tidyQueue(q, listed)
		a := e.tidyRow("r", "q", "first", "member", "", "", store.MemoStatePending)
		time.Sleep(1100 * time.Millisecond)
		b := e.tidyRow("r", "q", "second", "member", "", "", store.MemoStatePending)
		c := e.tidyRow("r", "q", "one two", "member", "", "", store.MemoStatePending)
		d := e.tidyRow("r", "q", "to listed", "member", "", "", store.MemoStatePending)
		f := e.tidyRow("r", "q", "to unlisted", "member", "", "", store.MemoStatePending)
		g := e.tidyRow("r", "q", "to plain", "member", "", "", store.MemoStatePending)
		k := e.tidyRow("r", "q", "kept", "member", "", "", store.MemoStatePending)
		rows := []store.Memo{a, b, c, d, f, g, k}
		before := e.allMemos()
		queueBefore := e.queueRow(q)

		out := e.tidyApply(q, store.TidyTriggerAuto, rows, []string{listed, unlisted, plain},
			merge("first and second", a.ID, b.ID),
			memotidy.Op{Op: memotidy.OpSplit, ID: c.ID, Texts: []string{"one", "two"}},
			move(d.ID, listed), move(f.ID, unlisted), move(g.ID, plain),
			memotidy.Op{Op: memotidy.OpKeep, ID: k.ID})
		if out.Run.State != store.TidyRunApplied || out.Run.Reason != "" {
			t.Fatalf("run: %+v", out.Run)
		}
		after := e.allMemos()
		if len(after) != len(before)+3 {
			t.Fatalf("rows %d -> %d, want three derived rows more and none deleted", len(before), len(after))
		}
		e.assertPendingReleaseMatches()
		if !reflect.DeepEqual(queueBefore, e.queueRow(q)) {
			t.Fatalf("a run changed the queue's settings:\n%+v\n%+v", queueBefore, e.queueRow(q))
		}

		// Sources: superseded, sent_at set so the retention sweep collects them, the run recorded.
		for _, s := range []store.Memo{a, b, c} {
			m := after[s.ID]
			if m.State != store.MemoStateSuperseded || m.SentAt == "" || m.TidyRunID != out.Run.ID || m.ReleaseHash != s.ReleaseHash {
				t.Fatalf("source %q: %+v", s.Body, m)
			}
		}
		// Results: derived, curator, the sources' provenance and state, oldest created_at, released by tidy.
		if len(out.Results) != 3 {
			t.Fatalf("results %v", out.Results)
		}
		merged := after[out.Results[0]]
		if merged.Body != "first and second" || merged.Provenance != store.MemoProvenanceCurator || merged.SourceProvenance != "member" ||
			merged.State != store.MemoStatePending || merged.ReleasedBy != store.MemoReleasedByTidy || merged.CreatedAt != a.CreatedAt ||
			merged.TidySeenHash != memoContentHash(merged) || merged.ReleaseHash != memoContentHash(merged) || merged.TidyRunID != out.Run.ID ||
			merged.RefKey != "" || merged.RefPath != "" || merged.Attachments != "" {
			t.Fatalf("merged: %+v", merged)
		}
		if got := decodeIDs(merged.DerivedFrom); !reflect.DeepEqual(got, []string{a.ID, b.ID}) {
			t.Fatalf("derivedFrom %v", got)
		}
		if p1, p2 := after[out.Results[1]], after[out.Results[2]]; p1.Body != "one" || p2.Body != "two" || decodeIDs(p1.DerivedFrom)[0] != c.ID {
			t.Fatalf("split parts: %+v %+v", p1, p2)
		}
		// Moves are decided by the stored settings: listed -> pending, unlisted -> held, plain -> note.
		if m := after[d.ID]; m.Category != "listed" || m.State != store.MemoStatePending || m.TidyFrom != q || m.ReleasedBy != store.MemoReleasedByTidy {
			t.Fatalf("moved to a listed queue: %+v", m)
		}
		if m := after[f.ID]; m.Category != "unlisted" || m.State != store.MemoStateHeld || m.TidyFrom != q || m.ReleaseHash != "" {
			t.Fatalf("moved to an unlisted queue: %+v", m)
		}
		if m := after[g.ID]; m.Category != "plain" || m.State != "" || m.TidyFrom != q {
			t.Fatalf("moved to a plain category: %+v", m)
		}
		// A kept message is as it was, and seen.
		if m := after[k.ID]; m.State != store.MemoStatePending || m.Body != "kept" || m.TidySeenHash != memoContentHash(k) {
			t.Fatalf("kept: %+v", m)
		}

		// What the Console reads.
		list := decode[[]memoDTO](t, e.do("a", "GET", "/api/memos", nil))
		var dto, movedDTO memoDTO
		for _, m := range list {
			switch m.ID {
			case merged.ID:
				dto = m
			case f.ID:
				movedDTO = m
			}
		}
		if dto.Provenance != "curator" || dto.SourceProvenance != "member" || dto.ReleasedBy != "tidy" || dto.TidyRunID != out.Run.ID || !reflect.DeepEqual(dto.DerivedFrom, []string{a.ID, b.ID}) {
			t.Fatalf("derived dto: %+v", dto)
		}
		if movedDTO.TidyFrom == nil || movedDTO.TidyFrom.ID != q || movedDTO.TidyFrom.Name != "q" || movedDTO.TidyFrom.Repo != "r" {
			t.Fatalf("moved dto: %+v", movedDTO.TidyFrom)
		}
		runs := e.runs(q)
		if runs.UsedToday != 1 || runs.DailyCap != memotidy.DailyCap || len(runs.Runs) != 1 {
			t.Fatalf("run list: %+v", runs)
		}
		r := runs.Runs[0]
		if r.State != "applied" || r.Trigger != "auto" || r.Merged != 1 || r.Split != 1 || r.Moved != 3 || r.Held != 1 || !r.Undoable || len(r.Blocked) != 0 ||
			len(r.SourceIDs) != 3 || len(r.ResultIDs) != 3 || len(r.MovedIDs) != 3 {
			t.Fatalf("run dto: %+v", r)
		}

		// Undo.
		rec := e.undo(out.Run.ID)
		if rec.Code != 200 {
			t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
		}
		undone := decode[memoTidyUndoWire](t, rec)
		if undone.Run.State != "undone" || len(undone.Restored) != 6 || len(undone.Removed) != 3 {
			t.Fatalf("undo answer: %+v", undone)
		}
		restored := e.allMemos()
		if len(restored) != len(before) {
			t.Fatalf("rows after undo %d, want %d", len(restored), len(before))
		}
		for id, was := range before {
			m := restored[id]
			if m.State != store.MemoStatePending || m.SentAt != "" || m.Category != was.Category || m.Body != was.Body ||
				m.ReleaseHash != memoContentHash(m) || m.TidyRunID != "" || m.TidyFrom != "" || m.TidySeenHash != memoContentHash(m) {
				t.Fatalf("after undo %q: %+v", was.Body, m)
			}
		}
		e.assertPendingReleaseMatches()
		if rec := e.undo(out.Run.ID); rec.Code != 409 || decode[tidyBlocked](t, rec).Error.Code != "memo_tidy_not_undoable" {
			t.Fatalf("a second undo: %d %s", rec.Code, rec.Body.String())
		}
		if r := e.runs(q).Runs[0]; r.State != "undone" || r.Undoable || r.UndoneAt == "" {
			t.Fatalf("run after undo: %+v", r)
		}
	})
}

// The listing is read when the reply is applied, not when it was asked for.
func TestMemoTidyMoveIsDecidedAtApplyTime(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q, dest := e.category("r", "q"), e.category("r", "dest")
		e.queue(dest)
		e.tidyQueue(q, dest)
		m1 := e.tidyRow("r", "q", "one", "member", "", "", store.MemoStatePending)
		// Listed when the run read it, unlisted when it applies: held.
		e.tidyQueue(q)
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{m1}, []string{dest}, move(m1.ID, dest))
		if m := e.memoRow(m1.ID); out.Run.State != "applied" || m.State != store.MemoStateHeld || m.TidyFrom != q || m.ReleaseHash != "" {
			t.Fatalf("a destination unlisted since: %+v", m)
		}
		// Unlisted when asked, listed at apply: pending, with a release that matches.
		m2 := e.tidyRow("r", "q", "two", "member", "", "", store.MemoStatePending)
		e.tidyQueue(q, dest)
		e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{m2}, []string{dest}, move(m2.ID, dest))
		if m := e.memoRow(m2.ID); m.State != store.MemoStatePending || m.ReleaseHash != memoContentHash(m) || m.ReleasedBy != "tidy" {
			t.Fatalf("a destination listed since: %+v", m)
		}
		// A destination that stopped being a queue holds a plain note.
		m3 := e.tidyRow("r", "q", "three", "member", "", "", store.MemoStatePending)
		if r := e.do("a", "DELETE", "/api/memo-queues/"+dest, nil); r.Code != 204 {
			t.Fatalf("delete settings: %d", r.Code)
		}
		e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{m3}, []string{dest}, move(m3.ID, dest))
		if m := e.memoRow(m3.ID); m.State != "" || m.Category != "dest" {
			t.Fatalf("a destination that is no queue: %+v", m)
		}
		e.assertPendingReleaseMatches()
	})
}

// The manual Tidy takes held messages too, and a result takes its sources' state: a move never
// releases a held message and a derived message of any held source is held.
func TestMemoTidyManualResultsTakeTheSourcesState(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q, dest := e.category("r", "q"), e.category("r", "dest")
		e.queue(dest)
		e.tidyQueue(q, dest)
		h1 := e.tidyRow("r", "q", "held one", "workspace", "s1", "agent", store.MemoStateHeld)
		h2 := e.tidyRow("r", "q", "held two", "workspace", "s1", "agent", store.MemoStateHeld)
		p := e.tidyRow("r", "q", "pending", "workspace", "s1", "agent", store.MemoStatePending)
		h3 := e.tidyRow("r", "q", "held three", "member", "", "", store.MemoStateHeld)

		// The automatic run only takes pending messages.
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{h1}, nil, memotidy.Op{Op: memotidy.OpKeep, ID: h1.ID})
		if out.Run.State != "dropped" || out.Run.Reason != "stale_input" {
			t.Fatalf("an automatic run with a held message: %+v", out.Run)
		}
		out = e.tidyApply(q, store.TidyTriggerManual, []store.Memo{h1, h2, p, h3}, []string{dest},
			merge("both held", h1.ID, h2.ID), move(h3.ID, dest), memotidy.Op{Op: memotidy.OpKeep, ID: p.ID})
		if out.Run.State != "applied" || out.Run.Trigger != "manual" {
			t.Fatalf("manual run: %+v", out.Run)
		}
		if m := e.memoRow(out.Results[0]); m.State != store.MemoStateHeld || m.ReleaseHash != "" || m.SourceProvenance != "workspace" || m.Producer != "s1" || m.ProducerOrigin != "agent" {
			t.Fatalf("derived of held sources: %+v", m)
		}
		if m := e.memoRow(h3.ID); m.State != store.MemoStateHeld || m.Category != "dest" {
			t.Fatalf("a held message moved to a listed queue was released: %+v", m)
		}
		// A merge of a pending and a held message is held.
		p2 := e.tidyRow("r", "q", "pending two", "workspace", "s1", "agent", store.MemoStatePending)
		h4 := e.tidyRow("r", "q", "held four", "workspace", "s1", "agent", store.MemoStateHeld)
		out = e.tidyApply(q, store.TidyTriggerManual, []store.Memo{p2, h4}, nil, merge("mixed", p2.ID, h4.ID))
		if m := e.memoRow(out.Results[0]); m.State != store.MemoStateHeld {
			t.Fatalf("derived of a pending and a held source: %+v", m)
		}
		e.assertPendingReleaseMatches()
	})
}

// No merge across provenance, producer or producer origin, over the full matrix, and a derived
// message is grouped by its sources' provenance and producer.
func TestMemoTidyNoMergeAcrossGroups(t *testing.T) {
	var rows []store.Memo
	for _, prov := range []string{"member", "workspace", "mcp"} {
		for _, producer := range []string{"", "s1"} {
			for _, origin := range []string{"", "user"} {
				rows = append(rows, store.Memo{Kind: "text", Body: "x", Provenance: prov, Producer: producer, ProducerOrigin: origin})
				rows = append(rows, store.Memo{Kind: "text", Body: "x", Provenance: store.MemoProvenanceCurator, SourceProvenance: prov, Producer: producer, ProducerOrigin: origin})
			}
		}
	}
	for i := range rows {
		rows[i].ID = string(rune('A' + i))
	}
	groupOf := func(m store.Memo) [3]string {
		prov := m.Provenance
		if prov == store.MemoProvenanceCurator {
			prov = m.SourceProvenance
		}
		return [3]string{prov, m.Producer, m.ProducerOrigin}
	}
	pairs := 0
	for i := range rows {
		for j := i + 1; j < len(rows); j++ {
			pairs++
			r := memotidy.Validate(memotidy.Request{Self: "self", Items: []memotidy.Item{memoTidyItemOf(rows[i]), memoTidyItemOf(rows[j])},
				Ops: []memotidy.Op{merge("x", rows[i].ID, rows[j].ID)}})
			same := groupOf(rows[i]) == groupOf(rows[j])
			if same && r != nil || !same && (r == nil || r.Code != memotidy.CodeMixedGroup) {
				t.Fatalf("merge of %+v and %+v: %v (same group %v)", groupOf(rows[i]), groupOf(rows[j]), r, same)
			}
		}
	}
	if pairs != 24*23/2 {
		t.Fatalf("matrix of %d pairs", pairs)
	}

	// End to end: a refused merge changes nothing, and a derived message of a session's notes does
	// not merge with a member's.
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		mem := e.tidyRow("r", "q", "mine", "member", "", "", store.MemoStatePending)
		ws1 := e.tidyRow("r", "q", "theirs", "workspace", "s1", "user", store.MemoStatePending)
		ws2 := e.tidyRow("r", "q", "theirs too", "workspace", "s1", "user", store.MemoStatePending)
		before := e.allMemos()
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{mem, ws1}, nil, merge("x", mem.ID, ws1.ID))
		if out.Run.State != "dropped" || out.Run.Reason != memotidy.CodeMixedGroup {
			t.Fatalf("mixed merge: %+v", out.Run)
		}
		if got := e.allMemos(); len(got) != len(before) || got[mem.ID].State != store.MemoStatePending {
			t.Fatalf("a refused merge changed rows: %d", len(got))
		}
		out = e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{ws1, ws2}, nil, merge("theirs, both", ws1.ID, ws2.ID))
		derived := e.memoRow(out.Results[0])
		if out.Run.State != "applied" || derived.SourceProvenance != "workspace" || derived.Producer != "s1" || derived.ProducerOrigin != "user" {
			t.Fatalf("same group merge: %+v / %+v", out.Run, derived)
		}
		// The derived message is still the sessions', so it does not merge with the member's.
		out = e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{derived, mem}, nil, merge("x", derived.ID, mem.ID))
		if out.Run.State != "dropped" || out.Run.Reason != memotidy.CodeMixedGroup {
			t.Fatalf("derived with a member's note: %+v", out.Run)
		}
	})
}

// An invalid reply, and an input that is no longer what the run read, leave every row as it was
// except the seen mark, and the queue's row identical.
func TestMemoTidyRefusedReplyLeavesTheQueueAsItWas(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q, dest := e.category("r", "q"), e.category("r", "dest")
		e.queue(dest)
		e.tidyQueue(q, dest)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		ws := e.tidyRow("r", "q", "w", "workspace", "s1", "user", store.MemoStatePending)
		before, queueBefore := e.allMemos(), e.queueRow(q)
		same := func(what string) {
			t.Helper()
			after := e.allMemos()
			for id, was := range before {
				got := after[id]
				got.TidySeenHash = was.TidySeenHash
				if !reflect.DeepEqual(got, was) {
					t.Fatalf("%s changed a row:\n%+v\n%+v", what, was, got)
				}
			}
			if len(after) != len(before) || !reflect.DeepEqual(queueBefore, e.queueRow(q)) {
				t.Fatalf("%s changed the rows or the queue", what)
			}
		}
		rows := []store.Memo{a, b, ws}
		for name, ops := range map[string][]memotidy.Op{
			"an unknown id":      {{Op: memotidy.OpKeep, ID: "nope"}},
			"a mixed merge":      {merge("x", a.ID, ws.ID)},
			"a move elsewhere":   {move(a.ID, "not-offered")},
			"a good op then bad": {merge("ab", a.ID, b.ID), move(ws.ID, q)},
			"an envelope":        {merge("[agent-fleet:peer] ab", a.ID, b.ID)},
		} {
			out := e.tidyApply(q, store.TidyTriggerAuto, rows, []string{dest}, ops...)
			if out.Run.State != "dropped" || out.Run.Reason == "" {
				t.Fatalf("%s: %+v", name, out.Run)
			}
			same(name)
		}
		for _, r := range e.runs(q).Runs {
			if r.State != "dropped" || r.Undoable || len(r.SourceIDs) != 0 {
				t.Fatalf("run list: %+v", r)
			}
		}
		// The run marked its input seen: the seen hash is the hash it read.
		if m := e.memoRow(a.ID); m.TidySeenHash != memoContentHash(a) {
			t.Fatalf("seen mark: %+v", m)
		}
		// A person's edit afterwards makes it unseen again with no other write.
		if code, _ := (memoFaceClient{"console", e}).update(a.ID, map[string]any{"body": "edited"}); code != 200 {
			t.Fatal("edit")
		}
		if m := e.memoRow(a.ID); m.TidySeenHash == memoContentHash(m) {
			t.Fatalf("an edited message is still seen: %+v", m)
		}

		// Stale input: edited, claimed, in a flush, or sent since the run read it.
		for name, change := range map[string]string{
			"edited":   `UPDATE memo SET body='edited since', release_body='edited since' WHERE id=?`,
			"claimed":  `UPDATE memo SET state='claimed', delivery_id='d1' WHERE id=?`,
			"sending":  `UPDATE memo SET send_token='t', send_started_at='` + store.NowTS() + `' WHERE id=?`,
			"sent":     `UPDATE memo SET sent_at='` + store.NowTS() + `' WHERE id=?`,
			"moved":    `UPDATE memo SET category='dest', state='', release_hash='' WHERE id=?`,
			"released": `UPDATE memo SET state='held', release_hash='' WHERE id=?`,
		} {
			row := e.tidyRow("r", "q", "stale "+name, "member", "", "", store.MemoStatePending)
			snap := e.allMemos()
			if _, err := e.st.DB().ExecContext(context.Background(), change, row.ID); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			changed := e.memoRow(row.ID)
			out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{row, b}, nil, merge("x", row.ID, b.ID))
			if out.Run.State != "dropped" || out.Run.Reason != "stale_input" {
				t.Fatalf("%s: %+v", name, out.Run)
			}
			after := e.allMemos()
			got := after[row.ID]
			got.TidySeenHash = changed.TidySeenHash
			if len(after) != len(snap) || !reflect.DeepEqual(got, changed) {
				t.Fatalf("%s: stale input changed rows", name)
			}
		}
		if out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{b}, make([]string, memotidy.MaxDestinations+1)); out.Run.State != "dropped" || out.Run.Reason != memotidy.CodeBadShape {
			t.Fatalf("too many destinations: %+v", out.Run)
		}
		if out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{{ID: "gone", State: store.MemoStatePending}}, nil); out.Run.Reason != "stale_input" {
			t.Fatalf("a row that is gone: %+v", out.Run)
		}
	})
}

// A run in which nothing changes is kept, and one that cannot find its queue is dropped.
func TestMemoTidyKeptAndNoQueue(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		before := e.allMemos()
		if out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a}, nil); out.Run.State != "kept" {
			t.Fatalf("no operations: %+v", out.Run)
		}
		if out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a}, nil, memotidy.Op{Op: memotidy.OpKeep, ID: a.ID}); out.Run.State != "kept" {
			t.Fatalf("keep: %+v", out.Run)
		}
		if got := e.allMemos(); len(got) != len(before) || got[a.ID].State != store.MemoStatePending {
			t.Fatal("a kept run changed rows")
		}
		if out := e.tidyApply("no-such-queue", store.TidyTriggerAuto, nil, nil); out.Run.State != "dropped" || out.Run.Reason != "no_queue" {
			t.Fatalf("no queue: %+v", out.Run)
		}
		if r := e.runs(q); r.UsedToday != 2 {
			t.Fatalf("usedToday counts every run: %+v", r)
		}
	})
}

// A move of messages that cannot become two open messages of one reference in a queue.
func TestMemoTidyMoveRespectsTheReferenceRule(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q, dest := e.category("r", "q"), e.category("r", "dest")
		e.queue(dest)
		e.tidyQueue(q, dest)
		if code, _ := (memoFaceClient{"console", e}).create(refMemo("r", "dest", "already there", ghRef("5"))); code != http.StatusCreated {
			t.Fatalf("setup: %d", code)
		}
		mine := e.tidyRow("r", "q", "same item", "member", "", "", store.MemoStatePending)
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET ref_provider='github', ref_key='acme/app#5', ref_url='https://github.com/acme/app/issues/5', release_ref_provider='github', release_ref_key='acme/app#5', release_ref_url='https://github.com/acme/app/issues/5' WHERE id=?`, mine.ID); err != nil {
			t.Fatal(err)
		}
		mine = e.memoRow(mine.ID)
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{mine}, []string{dest}, move(mine.ID, dest))
		if out.Run.State != "dropped" || out.Run.Reason != memotidy.CodeDuplicateRef {
			t.Fatalf("a duplicate reference: %+v", out.Run)
		}
		// A note with a reference is never merged or split, only moved or kept.
		other := e.tidyRow("r", "q", "plain", "member", "", "", store.MemoStatePending)
		if out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{mine, other}, nil, merge("x", mine.ID, other.ID)); out.Run.Reason != memotidy.CodeNotMergeable {
			t.Fatalf("a merge with a reference: %+v", out.Run)
		}
	})
}

// Undo is refused whole, naming each message, and nothing changes, whatever stops it.
func TestMemoTidyUndoIsRefusedWholeAndNamesTheMessages(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q, dest := e.category("r", "q"), e.category("r", "dest")
		e.queue(dest)
		e.tidyQueue(q, dest)
		setup := func(tag string) (memoTidyApplyOut, store.Memo, store.Memo, store.Memo) {
			a := e.tidyRow("r", "q", "a "+tag, "member", "", "", store.MemoStatePending)
			b := e.tidyRow("r", "q", "b "+tag, "member", "", "", store.MemoStatePending)
			m := e.tidyRow("r", "q", "m "+tag, "member", "", "", store.MemoStatePending)
			out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a, b, m}, []string{dest}, merge("ab "+tag, a.ID, b.ID), move(m.ID, dest))
			if out.Run.State != "applied" {
				t.Fatalf("%s: %+v", tag, out.Run)
			}
			return out, a, b, m
		}
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := e.st.DB().ExecContext(context.Background(), sql, args...); err != nil {
				t.Fatal(err)
			}
		}
		refused := func(tag string, out memoTidyApplyOut, want ...memoTidyBlock) {
			t.Helper()
			before := e.allMemos()
			rec := e.undo(out.Run.ID)
			if rec.Code != 409 {
				t.Fatalf("%s: undo %d %s", tag, rec.Code, rec.Body.String())
			}
			got := decode[tidyBlocked](t, rec).Error
			if got.Code != "memo_tidy_not_undoable" || !reflect.DeepEqual(got.Blocked, want) {
				t.Fatalf("%s: blocked %+v, want %+v", tag, got, want)
			}
			if after := e.allMemos(); !reflect.DeepEqual(before, after) {
				t.Fatalf("%s: a refused undo changed rows", tag)
			}
			if r, _, _ := e.getRun(out.Run.ID); r.State != "applied" {
				t.Fatalf("%s: run %s", tag, r.State)
			}
		}

		out, _, _, m := setup("claimed")
		exec(`UPDATE memo SET state='claimed', delivery_id='d1' WHERE id=?`, out.Results[0])
		exec(`UPDATE memo SET state='claimed', delivery_id='d1' WHERE id=?`, m.ID)
		refused("claimed", out, memoTidyBlock{out.Results[0], "claimed"}, memoTidyBlock{m.ID, "claimed"})
		if r := e.runs(q).Runs[len(e.runs(q).Runs)-1]; r.Undoable || len(r.Blocked) != 2 {
			t.Fatalf("the list shows what the undo refuses: %+v", r)
		}

		out, _, _, _ = setup("flush")
		exec(`UPDATE memo SET send_token='t', send_started_at=? WHERE id=?`, store.NowTS(), out.Results[0])
		refused("flush", out, memoTidyBlock{out.Results[0], "claimed"})

		out, _, _, _ = setup("sent")
		exec(`UPDATE memo SET state='delivered', sent_at=?, release_hash='' WHERE id=?`, store.NowTS(), out.Results[0])
		refused("sent", out, memoTidyBlock{out.Results[0], "sent"})

		out, _, _, _ = setup("changed")
		if code, _ := (memoFaceClient{"console", e}).update(out.Results[0], map[string]any{"body": "edited by hand"}); code != 200 {
			t.Fatal("edit derived")
		}
		refused("changed", out, memoTidyBlock{out.Results[0], "changed"})

		out, a, _, _ := setup("gone")
		exec(`DELETE FROM memo WHERE id=?`, out.Results[0])
		exec(`DELETE FROM memo WHERE id=?`, a.ID)
		refused("gone", out, memoTidyBlock{out.Results[0], "gone"}, memoTidyBlock{a.ID, "gone"})

		out, a, _, _ = setup("source")
		exec(`UPDATE memo SET state='delivered' WHERE id=?`, a.ID)
		refused("a source that is no longer superseded", out, memoTidyBlock{a.ID, "changed"})

		// A later run used a result: the result was merged again.
		out, _, _, _ = setup("later")
		first := e.memoRow(out.Results[0])
		other := e.tidyRow("r", "q", "other", "member", "", "", store.MemoStatePending)
		later := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{first, other}, nil, merge("again", first.ID, other.ID))
		if later.Run.State != "applied" {
			t.Fatalf("later run: %+v", later.Run)
		}
		refused("later_run", out, memoTidyBlock{first.ID, "later_run"})
		// The later run itself can be undone, and then the first.
		if rec := e.undo(later.Run.ID); rec.Code != 200 {
			t.Fatalf("undo the later run: %d %s", rec.Code, rec.Body.String())
		}
		if rec := e.undo(out.Run.ID); rec.Code != 200 {
			t.Fatalf("undo the first after the later one: %d %s", rec.Code, rec.Body.String())
		}

		// A moved message with a reference cannot go back beside one that has taken its place.
		ref := e.tidyRow("r", "q", "with reference", "member", "", "", store.MemoStatePending)
		exec(`UPDATE memo SET ref_provider='github', ref_key='acme/app#9', ref_url='https://github.com/acme/app/issues/9', release_ref_provider='github', release_ref_key='acme/app#9', release_ref_url='https://github.com/acme/app/issues/9' WHERE id=?`, ref.ID)
		ref = e.memoRow(ref.ID)
		out = e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{ref}, []string{dest}, move(ref.ID, dest))
		if out.Run.State != "applied" {
			t.Fatalf("move a reference: %+v", out.Run)
		}
		if code, d := (memoFaceClient{"console", e}).create(refMemo("r", "q", "a new one", ghRef("9"))); code != http.StatusCreated {
			t.Fatalf("a new message with the reference: %d %+v", code, d)
		}
		refused("duplicate_ref", out, memoTidyBlock{ref.ID, "duplicate_ref"})
		// An unknown run, and one that is not applied.
		if rec := e.undo("nope"); rec.Code != 404 {
			t.Fatalf("unknown run: %d", rec.Code)
		}
		dropped := e.tidyApply(q, store.TidyTriggerAuto, nil, nil, memotidy.Op{Op: memotidy.OpKeep, ID: "x"})
		if rec := e.undo(dropped.Run.ID); rec.Code != 409 || len(decode[tidyBlocked](t, rec).Error.Blocked) != 0 {
			t.Fatalf("undo of a dropped run: %d %s", rec.Code, rec.Body.String())
		}
	})
}

func (e *inboxEnv) getRun(id string) (store.MemoTidyRun, bool, error) {
	var r store.MemoTidyRun
	var ok bool
	err := e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) (err error) {
		r, ok, err = tx.GetTidyRun(id)
		return err
	})
	return r, ok, err
}

// An undo puts a message back as pending only when its release still matches, and as held or a
// plain note otherwise.
func TestMemoTidyUndoRestoresUnderTheSameRule(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q, dest := e.category("r", "q"), e.category("r", "dest")
		e.queue(dest)
		e.tidyQueue(q, dest)
		// The queue is renamed after the run: the message goes back held, its release being of
		// the old name.
		m := e.tidyRow("r", "q", "moved", "member", "", "", store.MemoStatePending)
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{m}, []string{dest}, move(m.ID, dest))
		if r := e.do("a", "PATCH", "/api/memo-categories/"+q, map[string]any{"name": "q-renamed"}); r.Code != 200 {
			t.Fatalf("rename: %d", r.Code)
		}
		if rec := e.undo(out.Run.ID); rec.Code != 200 {
			t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(m.ID); got.State != store.MemoStateHeld || got.Category != "q-renamed" || got.ReleaseHash != "" {
			t.Fatalf("after a rename: %+v", got)
		}
		// The queue's settings are gone after the run: a plain note.
		m2 := e.tidyRow("r", "q-renamed", "moved two", "member", "", "", store.MemoStatePending)
		out = e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{m2}, []string{dest}, move(m2.ID, dest))
		if r := e.do("a", "DELETE", "/api/memo-queues/"+q, nil); r.Code != 204 {
			t.Fatalf("delete settings: %d", r.Code)
		}
		if rec := e.undo(out.Run.ID); rec.Code != 200 {
			t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(m2.ID); got.State != "" || got.Category != "q-renamed" || got.SentAt != "" {
			t.Fatalf("after the settings were deleted: %+v", got)
		}
		// A held source goes back held, never pending.
		e.tidyQueue(q)
		h1 := e.tidyRow("r", "q-renamed", "h1", "workspace", "s", "", store.MemoStateHeld)
		h2 := e.tidyRow("r", "q-renamed", "h2", "workspace", "s", "", store.MemoStateHeld)
		out = e.tidyApply(q, store.TidyTriggerManual, []store.Memo{h1, h2}, nil, merge("h", h1.ID, h2.ID))
		if rec := e.undo(out.Run.ID); rec.Code != 200 {
			t.Fatalf("undo held: %d %s", rec.Code, rec.Body.String())
		}
		for _, id := range []string{h1.ID, h2.ID} {
			if got := e.memoRow(id); got.State != store.MemoStateHeld || got.SentAt != "" || got.TidyRunID != "" {
				t.Fatalf("a held source after undo: %+v", got)
			}
		}
		e.assertPendingReleaseMatches()
	})
}

// memoTidyWritePathChecks is run at the end of TestMemoQueueInvariantOnEveryWritePath. A superseded
// row keeps its place, stays locked and never becomes held or pending, whatever writes to it. A derived row is a message like any other. Both are collected by the retention sweep.
func memoTidyWritePathChecks(t *testing.T, e *inboxEnv) {
	console, tok, mcp := memoFaceClient{"console", e}, memoFaceClient{"token", e}, memoFaceClient{"mcp", e}
	q := e.category("r", "tq")
	e.tidyQueue(q)
	a := e.tidyRow("r", "tq", "a", "member", "", "", store.MemoStatePending)
	b := e.tidyRow("r", "tq", "b", "member", "", "", store.MemoStatePending)
	out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a, b}, nil, merge("ab", a.ID, b.ID))
	derived := out.Results[0]
	neverRevived := func(what string) {
		t.Helper()
		for _, id := range []string{a.ID, b.ID} {
			if m, ok := e.allMemos()[id]; ok && (m.State == store.MemoStatePending || m.State == store.MemoStateHeld) {
				t.Fatalf("%s: a superseded message is %s", what, m.State)
			}
		}
		e.assertPendingReleaseMatches()
	}
	for _, f := range []memoFaceClient{console, tok, mcp} {
		if code, _ := f.update(a.ID, map[string]any{"body": "edit"}); code != 409 {
			t.Fatalf("%s edit of a superseded message: %d", f.name, code)
		}
		if code := f.flush(a.ID); code != 409 {
			t.Fatalf("%s flush of a superseded message: %d", f.name, code)
		}
	}
	for _, f := range []memoFaceClient{tok, mcp} {
		if code := f.del(a.ID); code != 409 {
			t.Fatalf("%s delete of a superseded message: %d", f.name, code)
		}
	}
	if r := e.release(a.ID, memoContentHash(e.memoRow(a.ID))); r.Code != 409 {
		t.Fatalf("release of a superseded message: %d", r.Code)
	}
	neverRevived("the faces")

	// The store refuses it even when a caller does not check.
	row := e.memoRow(a.ID)
	row.State, row.ReleaseHash, row.ReleasedAt = store.MemoStatePending, memoContentHash(row), store.NowTS()
	if err := e.st.UpdateMemo(context.Background(), row); !errors.Is(err, store.ErrMemoSuperseded) {
		t.Fatalf("UpdateMemo of a superseded row as pending: %v", err)
	}
	row.State = store.MemoStateHeld
	if err := e.st.UpdateMemo(context.Background(), row); !errors.Is(err, store.ErrMemoSuperseded) {
		t.Fatalf("UpdateMemo of a superseded row as held: %v", err)
	}
	neverRevived("UpdateMemo")

	// A rename and a merge of the category move the rows along and keep them superseded.
	if r := e.do("a", "PATCH", "/api/memo-categories/"+q, map[string]any{"name": "tq2"}); r.Code != 200 {
		t.Fatalf("rename: %d %s", r.Code, r.Body.String())
	}
	if m := e.memoRow(a.ID); m.Category != "tq2" || m.State != store.MemoStateSuperseded || m.SentAt == "" {
		t.Fatalf("after a rename: %+v", m)
	}
	if d := e.memoRow(derived); d.State != store.MemoStatePending || d.ReleaseHash != memoContentHash(d) || d.Provenance != "curator" {
		t.Fatalf("derived after a rename: %+v", d)
	}
	other := e.category("r", "tother")
	e.queue(other)
	if r := e.do("a", "PATCH", "/api/memo-categories/"+q, map[string]any{"name": "tother"}); r.Code != 200 {
		t.Fatalf("merge into another queue: %d %s", r.Code, r.Body.String())
	}
	if m := e.memoRow(a.ID); m.Category != "tother" || m.State != store.MemoStateSuperseded {
		t.Fatalf("after a merge: %+v", m)
	}
	neverRevived("a merge")
	// A Console move of the derived message out and back keeps it a message of curator provenance, and a
	// move into a queue from a plain category enters it held, never pending.
	plainCat := e.category("r", "tplain")
	if code, _ := console.update(derived, map[string]any{"category": "tplain"}); code != 200 {
		t.Fatal("move derived out")
	}
	if d := e.memoRow(derived); d.State != "" || d.Provenance != "curator" || d.DerivedFrom == "" {
		t.Fatalf("derived moved out: %+v", d)
	}
	if code, _ := console.update(derived, map[string]any{"category": "tother"}); code != 200 {
		t.Fatal("move derived in")
	}
	if d := e.memoRow(derived); d.State != store.MemoStateHeld {
		t.Fatalf("derived moved into a queue: %+v", d)
	}
	_ = plainCat

	// Settings deleted and created again: the superseded rows are sent notes, never pending.
	if r := e.do("a", "DELETE", "/api/memo-queues/"+other, nil); r.Code != 204 {
		t.Fatalf("delete settings: %d", r.Code)
	}
	if m := e.memoRow(a.ID); m.State != "" || m.SentAt == "" {
		t.Fatalf("superseded after the settings were deleted: %+v", m)
	}
	e.queue(other)
	neverRevived("settings deleted and created again")
	if m := e.memoRow(a.ID); m.State != store.MemoStateDelivered {
		t.Fatalf("a sent note entering a queue: %+v", m)
	}
	e.assertPendingReleaseMatches()
}

// The retention sweep collects a superseded source and keeps its derived message.
func TestMemoTidyRetentionCollectsSupersededSources(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a, b}, nil, merge("ab", a.ID, b.ID))
		all := decode[[]memoDTO](t, e.do("a", "GET", "/api/memos", nil))
		if len(all) != 3 {
			t.Fatalf("list within retention: %d rows", len(all))
		}
		if err := e.st.SweepSentMemos(context.Background(), time.Now().UTC().Add(time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		got := e.allMemos()
		if _, ok := got[a.ID]; ok || len(got) != 1 || got[out.Results[0]].State != store.MemoStatePending {
			t.Fatalf("after the sweep: %+v", got)
		}
		// With the sources collected the run can no longer be undone, and the list says so.
		if r := e.runs(q).Runs[0]; r.Undoable || len(r.Blocked) != 2 || r.Blocked[0].Why != "gone" && r.Blocked[1].Why != "gone" {
			t.Fatalf("run list after the sweep: %+v", r)
		}
		if rec := e.undo(out.Run.ID); rec.Code != 409 {
			t.Fatalf("undo after the sweep: %d", rec.Code)
		}
	})
}

// A write that clears sent_at or the run id of a superseded row fails on the database's CHECK, and
// the unique index allows one running run per member.
func TestMemoTidyDatabaseHoldsTheSchemaRules(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		ctx := context.Background()
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a, b}, nil, merge("ab", a.ID, b.ID))
		for _, stmt := range []string{
			`UPDATE memo SET sent_at='' WHERE id=?`,
			`UPDATE memo SET tidy_run_id='' WHERE id=?`,
		} {
			if _, err := e.st.DB().ExecContext(ctx, stmt, a.ID); err == nil {
				t.Fatalf("%s was accepted", stmt)
			}
		}
		if _, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET state='superseded' WHERE id=?`, e.tidyRow("r", "q", "c", "member", "", "", store.MemoStateHeld).ID); err == nil {
			t.Fatal("a superseded row without sent_at and a run id was accepted")
		}

		// One running run per member, across any number of processes: the database says no.
		mid := e.members["a"]
		reserve := func(id string) error {
			return e.st.MemoTx(ctx, mid, func(tx store.MemoTxOps) error {
				return tx.InsertTidyRun(store.MemoTidyRun{ID: id, QueueID: q, Trigger: "auto", State: store.TidyRunRunning, CreatedAt: memoNow(), UpdatedAt: memoNow()})
			})
		}
		if err := reserve("run-1"); err != nil {
			t.Fatal(err)
		}
		if err := reserve("run-2"); err == nil {
			t.Fatal("a second running run of one member was accepted")
		}
		// Another member's run is independent.
		if err := e.st.MemoTx(ctx, e.members["b"], func(tx store.MemoTxOps) error {
			return tx.InsertTidyRun(store.MemoTidyRun{ID: "run-b", QueueID: "x", Trigger: "auto", State: store.TidyRunRunning, CreatedAt: memoNow(), UpdatedAt: memoNow()})
		}); err != nil {
			t.Fatalf("another member's run: %v", err)
		}

		// A late apply after a takeover changes nothing: the run was finished by the takeover.
		c := e.tidyRow("r", "q", "c", "member", "", "", store.MemoStatePending)
		d := e.tidyRow("r", "q", "d", "member", "", "", store.MemoStatePending)
		before := e.allMemos()
		in := memoTidyApplyIn{MembershipID: mid, QueueID: q, RunID: "run-1", Trigger: "auto", Input: tidyItems(c, d), Ops: []memotidy.Op{merge("cd", c.ID, d.ID)}}
		if err := e.st.MemoTx(ctx, mid, func(tx store.MemoTxOps) error {
			ok, err := tx.FinishTidyRun(store.MemoTidyRun{ID: "run-1", State: store.TidyRunFailed, Reason: "taken_over", FinishedAt: memoNow(), UpdatedAt: memoNow()})
			if err == nil && !ok {
				t.Fatal("takeover did not finish the run")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := memoTidyApply(ctx, e.st, in); !errors.Is(err, errMemoTidyNotRunning) {
			t.Fatalf("a late apply: %v", err)
		}
		if after := e.allMemos(); !reflect.DeepEqual(before, after) {
			t.Fatal("a late apply changed rows")
		}
		// An apply on a reserved run finishes it.
		if err := reserve("run-3"); err != nil {
			t.Fatal(err)
		}
		in.RunID = "run-3"
		out, err := memoTidyApply(ctx, e.st, in)
		if err != nil || out.Run.ID != "run-3" || out.Run.State != store.TidyRunApplied {
			t.Fatalf("apply on a reserved run: %+v %v", out.Run, err)
		}
	})
}

// A member's deletion removes their runs.
func TestMemoTidyMembershipDeletionRemovesRuns(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		ctx := context.Background()
		if err := e.st.MemoTx(ctx, e.members["b"], func(tx store.MemoTxOps) error {
			return tx.InsertTidyRun(store.MemoTidyRun{ID: "run-b", QueueID: "x", Trigger: "auto", State: store.TidyRunFailed, CreatedAt: memoNow(), UpdatedAt: memoNow()})
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.MemoTx(ctx, e.members["a"], func(tx store.MemoTxOps) error {
			return tx.InsertTidyRun(store.MemoTidyRun{ID: "run-a", QueueID: "x", Trigger: "auto", State: store.TidyRunFailed, CreatedAt: memoNow(), UpdatedAt: memoNow()})
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.DeleteMembership(ctx, e.members["b"]); err != nil {
			t.Fatal(err)
		}
		if n := e.count("memo_tidy_run"); n != 1 {
			t.Fatalf("%d runs left, want only a's", n)
		}
	})
}

// The settings: omitted means off, destinations are validated, and the mark is read-only.
func TestMemoTidySettings(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q, d1, d2, plain := e.category("r", "q"), e.category("r", "d1"), e.category("r", "d2"), e.category("r", "plain")
		e.queue(d1)
		e.queue(d2)
		put := func(body map[string]any) *httptest.ResponseRecorder {
			body["instruction"] = "do it"
			return e.do("a", "PUT", "/api/memo-queues/"+q, body)
		}
		if r := put(map[string]any{}); r.Code != 200 {
			t.Fatalf("default: %d %s", r.Code, r.Body.String())
		} else if dto := decode[memoQueueDTO](t, r); dto.Tidy || dto.NoTidyAt != "" || dto.TidyDestinations == nil || len(dto.TidyDestinations) != 0 {
			t.Fatalf("omitted means off: %+v", dto)
		}
		if !strings.Contains(e.do("a", "GET", "/api/memo-queues", nil).Body.String(), `"tidy":false,"tidyDestinations":[],"noTidyAt":""`) {
			t.Fatalf("list wire: %s", e.do("a", "GET", "/api/memo-queues", nil).Body.String())
		}
		if r := put(map[string]any{"tidy": true, "tidyDestinations": []string{d1, d2, d1}}); r.Code != 200 {
			t.Fatalf("on: %d %s", r.Code, r.Body.String())
		} else if dto := decode[memoQueueDTO](t, r); !dto.Tidy || !reflect.DeepEqual(dto.TidyDestinations, []string{d1, d2}) {
			t.Fatalf("on: %+v", dto)
		}
		if got := e.queueRow(q); !got.Tidy || !reflect.DeepEqual(got.TidyDestinations, []string{d1, d2}) {
			t.Fatalf("stored: %+v", got)
		}
		// The mark is the dispatcher's: a PUT neither sets nor clears it.
		if err := e.st.SetMemoQueueNoTidy(context.Background(), q, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if r := put(map[string]any{"tidy": true, "noTidyAt": "", "tidyDestinations": []string{d1}}); r.Code != 200 || decode[memoQueueDTO](t, r).NoTidyAt != "2026-01-01T00:00:00Z" {
			t.Fatalf("a PUT touched noTidyAt: %d %s", r.Code, r.Body.String())
		}
		many := make([]string, 0, 11)
		for i := 0; i < store.MaxTidyDestinations+1; i++ {
			many = append(many, e.category("r", "m"+string(rune('a'+i))))
		}
		for name, dests := range map[string][]string{
			"itself":            {q},
			"a plain category":  {plain},
			"an unknown id":     {"nope"},
			"an empty id":       {""},
			"too many":          many,
			"another's queue":   {e.foreignQueue()},
			"one good, one bad": {d1, plain},
		} {
			if r := put(map[string]any{"tidy": true, "tidyDestinations": dests}); r.Code != 400 || !strings.Contains(r.Body.String(), "memo_queue_bad_settings") {
				t.Fatalf("%s: %d %s", name, r.Code, r.Body.String())
			}
		}
		// A destination whose settings are deleted is not listed any more.
		if r := e.do("a", "DELETE", "/api/memo-queues/"+d2, nil); r.Code != 204 {
			t.Fatal("delete d2")
		}
		put(map[string]any{"tidy": true, "tidyDestinations": []string{d1}})
		e.queue(d2)
		put(map[string]any{"tidy": true, "tidyDestinations": []string{d1, d2}})
		if r := e.do("a", "DELETE", "/api/memo-queues/"+d2, nil); r.Code != 204 {
			t.Fatal("delete d2 again")
		}
		for _, dto := range decode[memoQueuesWire](t, e.do("a", "GET", "/api/memo-queues", nil)).Queues {
			if dto.CategoryID == q && !reflect.DeepEqual(dto.TidyDestinations, []string{d1}) {
				t.Fatalf("a destination that is no queue any more is listed: %v", dto.TidyDestinations)
			}
		}
	})
}

// foreignQueue is a queue of member b.
func (e *inboxEnv) foreignQueue() string {
	e.t.Helper()
	rec := e.do("b", "POST", "/api/memo-categories", map[string]any{"repo": "r", "name": "theirs"})
	id := decode[memoCategoryDTO](e.t, rec).ID
	if rec := e.do("b", "PUT", "/api/memo-queues/"+id, map[string]any{"instruction": "x"}); rec.Code != 200 {
		e.t.Fatalf("b's queue: %d %s", rec.Code, rec.Body.String())
	}
	return id
}

// The undo and the run list are the Console's: nothing of them is on the token face, and another
// member's run is not reachable.
func TestMemoTidyRoutesAreConsoleOnly(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a, b}, nil, merge("ab", a.ID, b.ID))
		for _, c := range []struct{ method, path string }{
			{"POST", "/internal/memo-tidy-runs/" + out.Run.ID + "/undo"},
			{"GET", "/internal/memo-queues/" + q + "/tidy-runs"},
			{"POST", "/internal/memo-tidy/apply"},
		} {
			if rec := e.memoTokenDo("a", c.method, c.path, nil); rec.Code != 404 && rec.Code != 405 {
				t.Fatalf("token face %s %s: %d", c.method, c.path, rec.Code)
			}
		}
		if rec := e.memoTokenDo("a", "POST", "/api/memo-tidy-runs/"+out.Run.ID+"/undo", nil); rec.Code == 200 {
			t.Fatalf("the token face reached the Console route: %d", rec.Code)
		}
		if e.memoRow(a.ID).State != store.MemoStateSuperseded {
			t.Fatal("the run was undone through a face that may not")
		}
		// Another member can neither undo nor list it.
		if rec := e.do("b", "POST", "/api/memo-tidy-runs/"+out.Run.ID+"/undo", nil); rec.Code != 404 {
			t.Fatalf("another member's undo: %d", rec.Code)
		}
		if rec := e.do("b", "GET", "/api/memo-queues/"+q+"/tidy-runs", nil); rec.Code != 404 {
			t.Fatalf("another member's list: %d", rec.Code)
		}
		if rec := e.do("a", "GET", "/api/memo-queues/nope/tidy-runs", nil); rec.Code != 404 {
			t.Fatalf("an unknown category: %d", rec.Code)
		}
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(e.do("a", "GET", "/api/memo-queues/"+q+"/tidy-runs", nil).Body.Bytes(), &raw)
		for _, k := range []string{"runs", "usedToday", "dailyCap"} {
			if _, ok := raw[k]; !ok {
				t.Fatalf("run list lacks %s", k)
			}
		}
	})
}

// The statements of a binary before this migration, run against a superseded row: whatever it
// does, the row is never pending afterwards, and a statement that clears sent_at is refused.
func TestMemoTidyOlderWriterCannotReviveASupersededRow(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		ctx := context.Background()
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a, b}, nil, merge("ab", a.ID, b.ID))
		mid := e.members["a"]
		// The UpdateMemo statement of store_memo.go (writeMemo) as it stood before migration 0142, verbatim.
		// If writeMemo changes its columns, compare this copy: it stands for the older binary.
		olderWrite := func(m store.Memo) error {
			rel := [9]string{}
			if m.State == store.MemoStatePending {
				rel = [9]string{m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.RefProvider, m.RefKey, m.RefURL}
			}
			_, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET repo=?, category=?, kind=?, body=?, ref_path=?, attachments=?, position=?, sent_at=?,
			   state=?, release_hash=?, released_at=?, send_token=?, send_started_at=?,
			   ref_provider=?, ref_key=?, ref_url=?,
			   release_repo=?, release_category=?, release_kind=?, release_body=?, release_ref_path=?, release_attachments=?,
			   release_ref_provider=?, release_ref_key=?, release_ref_url=?, released_by=?
			   WHERE id=? AND membership_id=?`, m.Repo, m.Category, m.Kind, m.Body, m.RefPath, m.Attachments, m.Position, m.SentAt,
				m.State, m.ReleaseHash, m.ReleasedAt, m.SendToken, m.SendStartedAt, m.RefProvider, m.RefKey, m.RefURL,
				rel[0], rel[1], rel[2], rel[3], rel[4], rel[5], rel[6], rel[7], rel[8], m.ReleasedBy, m.ID, mid)
			return err
		}
		// The older bucket move and the older hand send write the row as it read it, with the
		// state its own rules give a row that has sent_at: locked rows keep it, a send settles it
		// as delivered, entering a queue makes it delivered.
		row := e.memoRow(a.ID)
		moved := row
		moved.Category = "elsewhere"
		if err := olderWrite(moved); err != nil {
			t.Fatalf("older bucket move: %v", err)
		}
		if got := e.memoRow(a.ID); got.State != store.MemoStateSuperseded || got.SentAt == "" {
			t.Fatalf("after the older bucket move: %+v", got)
		}
		sent := e.memoRow(a.ID)
		sent.State, sent.ReleaseHash, sent.ReleasedAt = store.MemoStateDelivered, "", ""
		if err := olderWrite(sent); err != nil {
			t.Fatalf("older settle of a hand send: %v", err)
		}
		if got := e.memoRow(a.ID); got.State != store.MemoStateDelivered || got.SentAt == "" {
			t.Fatalf("after the older send: %+v", got)
		}
		// An older statement that clears sent_at on a superseded row fails on the CHECK.
		_, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET sent_at='' WHERE id=? AND membership_id=?`, b.ID, mid)
		if err == nil {
			t.Fatal("an older statement cleared sent_at of a superseded row")
		}
		if got := e.memoRow(b.ID); got.State != store.MemoStateSuperseded || got.SentAt == "" {
			t.Fatalf("after the refused statement: %+v", got)
		}
		// The older retention sweep collects it.
		if _, err := e.st.DB().ExecContext(ctx, `DELETE FROM memo WHERE sent_at!='' AND sent_at<?`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if _, ok := e.allMemos()[b.ID]; ok {
			t.Fatal("the older sweep left a superseded row")
		}
	})
}

// A derived message under an older binary: its statements work on it as on any message, the
// derived columns survive them, and the older claim refuses it instead of sending it.
func TestMemoTidyOlderWriterAndADerivedRow(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		ctx := context.Background()
		q := e.category("r", "q")
		e.tidyQueue(q)
		a := e.tidyRow("r", "q", "a", "member", "", "", store.MemoStatePending)
		b := e.tidyRow("r", "q", "b", "member", "", "", store.MemoStatePending)
		out := e.tidyApply(q, store.TidyTriggerAuto, []store.Memo{a, b}, nil, merge("ab", a.ID, b.ID))
		id := out.Results[0]
		mid := e.members["a"]
		// The older Console edit writes content and release together, naming only its own columns.
		if _, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET body=?, release_body=?, release_hash=? WHERE id=? AND membership_id=?`, "ab edited", "ab edited",
			memoContentHash(func() store.Memo { m := e.memoRow(id); m.Body = "ab edited"; return m }()), id, mid); err != nil {
			t.Fatalf("older edit of a derived message: %v", err)
		}
		got := e.memoRow(id)
		if got.Provenance != "curator" || got.DerivedFrom == "" || got.SourceProvenance != "member" || got.TidyRunID != out.Run.ID {
			t.Fatalf("the older edit lost the derived columns: %+v", got)
		}
		e.assertPendingReleaseMatches()
		// The older edit that leaves the release behind is refused, as for any pending message.
		if _, err := e.st.DB().ExecContext(ctx, `UPDATE memo SET body='x' WHERE id=?`, id); err == nil {
			t.Fatal("an older statement changed a derived pending message without its release")
		}
		// A claim of it is refused here and so by an older binary, whose rule this is.
		if why := memoDeliveryItemProblem(got); why == "" {
			t.Fatal("a derived message passes the claim's item check")
		}
	})
}

func TestMemoTidyDailyUsedCountsEveryRun(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		ctx := context.Background()
		q := e.category("r", "q")
		e.tidyQueue(q)
		mid := e.members["a"]
		now := time.Now()
		for i, c := range []struct {
			state string
			age   time.Duration
		}{{store.TidyRunApplied, time.Hour}, {store.TidyRunSkipped, 2 * time.Hour}, {store.TidyRunFailed, 23 * time.Hour}, {store.TidyRunDropped, 25 * time.Hour}, {store.TidyRunUndone, 26 * time.Hour}} {
			at := now.Add(-c.age).UTC().Format(time.RFC3339Nano)
			if err := e.st.MemoTx(ctx, mid, func(tx store.MemoTxOps) error {
				return tx.InsertTidyRun(store.MemoTidyRun{ID: store.NewID(), QueueID: q, Trigger: "auto", State: c.state, CreatedAt: at, UpdatedAt: at})
			}); err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
		}
		if n, err := memoTidyDailyUsed(ctx, e.st, q, now); err != nil || n != 3 {
			t.Fatalf("used today: %d %v", n, err)
		}
		if got := e.runs(q); got.UsedToday != 3 || len(got.Runs) != 5 {
			t.Fatalf("list: %+v", got)
		}
	})
}
