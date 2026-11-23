package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/memotidy"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Dependencies between messages (ADR 0137, addendum 2026-11-19, part A, step 1).

func afterMemo(repo, category, body string, after ...string) map[string]any {
	m := textMemo(repo, category, body)
	m["after"] = after
	return m
}

func sameIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The list is written by every face, resolves a reference key to the newest message holding it,
// and refuses whatever could not be satisfied by the queue's order.
func TestMemoAfterWrite(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		e.queue(e.category("r", "other"))
		e.category("r", "notes")
		console := memoFaceClient{"console", e}
		code, a := console.create(refMemo("r", "q", "first", ghRef("1")))
		if code != http.StatusCreated {
			t.Fatalf("create a: %d", code)
		}
		_, b := console.create(textMemo("r", "q", "second"))

		// An id and a key naming the same message are one prerequisite.
		code, c := console.create(afterMemo("r", "q", "third", a.ID, "ACME/app#1"))
		if code != http.StatusCreated || !sameIDs(c.After, a.ID) || c.State != store.MemoStatePending {
			t.Fatalf("create with after: %d %+v", code, c)
		}
		if row := e.memoRow(c.ID); row.AfterIDs == "" || row.ReleaseHash != memoContentHash(row) {
			t.Fatalf("stored %+v", row)
		}
		for name, body := range map[string]map[string]any{
			"unknown key":   afterMemo("r", "q", "x", "acme/app#999"),
			"unknown id":    afterMemo("r", "q", "x", "0123456789abcdef0123456789abcdef"),
			"other queue":   afterMemo("r", "other", "x", a.ID),
			"not a queue":   afterMemo("r", "notes", "x", a.ID),
			"empty element": afterMemo("r", "q", "x", " "),
		} {
			rec := e.do("a", "POST", "/api/memos", body)
			if rec.Code != http.StatusBadRequest || deliveryErrCode(t, rec) != errCodeMemoAfterInvalid {
				t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
			}
		}
		// A prerequisite may come later in the queue (issue #4876), but no message may wait for
		// itself, directly or through the messages it waits for.
		if code, _ := console.update(a.ID, map[string]any{"after": []string{c.ID}}); code != http.StatusBadRequest {
			t.Fatalf("two messages waiting for each other: %d", code)
		}
		if code, up := console.update(a.ID, map[string]any{"after": []string{b.ID}}); code != http.StatusOK || !sameIDs(up.After, b.ID) {
			t.Fatalf("a later prerequisite: %d %+v", code, up)
		}
		if row := e.memoRow(a.ID); row.AfterIDs == "" || row.State != store.MemoStatePending {
			t.Fatalf("stored %+v", row)
		}
		for name, after := range map[string]string{"by id": c.ID, "by key": "acme/app#1"} {
			// c waits for a, a for b: b waiting for c (or for a, by a's key) closes a cycle.
			rec := e.do("a", "PATCH", "/api/memos/"+b.ID, map[string]any{"after": []string{after}})
			if rec.Code != http.StatusBadRequest || deliveryErrCode(t, rec) != errCodeMemoAfterInvalid {
				t.Fatalf("a cycle through three messages %s: %d %s", name, rec.Code, rec.Body.String())
			}
		}
		if row := e.memoRow(b.ID); row.AfterIDs != "" {
			t.Fatalf("a refused list was stored: %+v", row)
		}
		if code, _ := console.update(a.ID, map[string]any{"after": []string{}}); code != http.StatusOK {
			t.Fatalf("clear a: %d", code)
		}
		// The /mcp face takes a later prerequisite too.
		_, later := console.create(textMemo("r", "q", "later"))
		if code, up := (memoFaceClient{"mcp", e}).update(b.ID, map[string]any{"after": []string{later.ID}}); code != http.StatusOK || !sameIDs(up.After, later.ID) {
			t.Fatalf("mcp: a later prerequisite: %d %+v", code, up)
		}
		if code, _ := console.update(c.ID, map[string]any{"after": []string{c.ID}}); code != http.StatusBadRequest {
			t.Fatalf("a message waiting for itself: %d", code)
		}
		// Replace, keep and clear.
		if code, up := console.update(c.ID, map[string]any{"after": []string{b.ID, a.ID}}); code != http.StatusOK || !sameIDs(up.After, b.ID, a.ID) {
			t.Fatalf("replace: %d %+v", code, up)
		}
		if code, up := console.update(c.ID, map[string]any{"body": "third, edited"}); code != http.StatusOK || !sameIDs(up.After, b.ID, a.ID) {
			t.Fatalf("an update without after changed it: %d %+v", code, up)
		}
		// Its prerequisites cannot follow it into another category.
		if code, _ := console.update(c.ID, map[string]any{"category": "other"}); code != http.StatusBadRequest {
			t.Fatalf("a move with prerequisites: %d", code)
		}
		if code, up := console.update(c.ID, map[string]any{"after": []string{}}); code != http.StatusOK || len(up.After) != 0 {
			t.Fatalf("clear: %d %+v", code, up)
		}
		if row := e.memoRow(c.ID); row.AfterIDs != "" {
			t.Fatalf("cleared row %+v", row)
		}
		// A key names the newest message holding it that is not superseded.
		e.setStateKeepingRelease(a.ID, store.MemoStateDelivered)
		_, a2 := console.create(refMemo("r", "q", "first, again", ghRef("1")))
		if code, d := console.create(afterMemo("r", "q", "fourth", "acme/app#1")); code != http.StatusCreated || !sameIDs(d.After, a2.ID) {
			t.Fatalf("the newest holder: %d %+v (want %s)", code, d, a2.ID)
		}
		// The workspace faces write it too, and their message waits for the press as always.
		for _, f := range workspaceFaces(e) {
			code, m := f.create(afterMemo("r", "q", "from "+f.name, b.ID))
			if code != http.StatusCreated || !sameIDs(m.After, b.ID) || m.State != store.MemoStateHeld {
				t.Fatalf("%s create: %d %+v", f.name, code, m)
			}
		}
		e.assertPendingReleaseMatches()
	})
}

// The list is part of the release, appended only when it is not empty: a message without
// prerequisites hashes exactly as before they existed, and a change of the list from inside the
// workspace sends a released message back to held.
func TestMemoAfterIsPartOfTheRelease(t *testing.T) {
	legacy := func(m store.Memo) string {
		b, _ := json.Marshal(append([]string{"memo-content/v1", m.Kind, m.Body, m.RefPath, m.Attachments, m.Repo, m.Category}, memoRefHashParts(m)...))
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	for _, m := range []store.Memo{{Kind: "text", Body: "x", Repo: "r", Category: "q"},
		{Kind: "text", Body: "x", RefProvider: "github", RefKey: "a/b#1", RefURL: refURL}} {
		if memoContentHash(m) != legacy(m) {
			t.Fatalf("a message without prerequisites hashes differently now: %+v", m)
		}
		withAfter := m
		withAfter.AfterIDs = `["p"]`
		if memoContentHash(withAfter) == memoContentHash(m) {
			t.Fatal("the content hash does not cover the prerequisites")
		}
	}
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		console, tok := memoFaceClient{"console", e}, memoFaceClient{"token", e}
		_, p := console.create(textMemo("r", "q", "prerequisite"))
		for _, f := range []memoFaceClient{tok, {"mcp", e}} {
			_, m := tok.create(textMemo("r", "q", "dependent via "+f.name))
			if r := e.release(m.ID, m.ContentHash); r.Code != http.StatusOK {
				t.Fatalf("release: %d", r.Code)
			}
			if code, _ := f.update(m.ID, map[string]any{"after": []string{p.ID}}); code != http.StatusOK {
				t.Fatalf("%s update: %d", f.name, code)
			}
			if got := e.memoRow(m.ID); got.State != store.MemoStateHeld || got.ReleaseHash != "" {
				t.Fatalf("%s: a changed list left the message %+v", f.name, got)
			}
		}
		// The Console's own change is the member's: the message stays released.
		_, m := console.create(textMemo("r", "q", "console dependent"))
		if code, up := console.update(m.ID, map[string]any{"after": []string{p.ID}}); code != http.StatusOK || up.State != store.MemoStatePending {
			t.Fatalf("console update: %d %+v", code, up)
		}
		e.assertPendingReleaseMatches()
		// The release copy is checked by the database: a pending row whose list differs from the
		// one it was released with is refused, as the other release columns are.
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET after_ids='' WHERE id=?`, m.ID); err == nil {
			t.Fatal("a pending row's list changed under its release")
		}
	})
}

// closeDeliveryAs closes a delivery and delivers its messages, with a first turn as given: turn ""
// leaves it unrecorded.
func (e *deliveryEnv) closeDeliveryAs(id, outcome, turn string, ms int64, closedAt time.Time) {
	e.t.Helper()
	ended := ""
	if turn != "" {
		ended = closedAt.UTC().Format(time.RFC3339)
		if turn == "none" {
			turn = ""
		}
	}
	db := e.st.DB()
	if _, err := db.ExecContext(context.Background(), `UPDATE memo_delivery SET state='closed', outcome=?, closed_at=?,
		turn_outcome=?, turn_ms=?, turn_ended_at=? WHERE id=?`, outcome, store.DeliveryTS(closedAt), turn, ms, ended, id); err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE memo SET state='delivered', sent_at=? WHERE delivery_id=? AND state IN ('claimed','delivered')`,
		store.NowTS(), id); err != nil {
		e.t.Fatal(err)
	}
}

// closeWithPRs is closeDeliveryAs with a PR snapshot that sums up to prState.
func (e *deliveryEnv) closeWithPRs(id, turn string, ms int64, closedAt time.Time, prState string) {
	e.t.Helper()
	e.closeDeliveryAs(id, store.DeliveryClean, turn, ms, closedAt)
	e.setPRs(id, prState)
}

// setAnswer writes a delivery's last answer as the answer pass would leave it.
func (e *deliveryEnv) setAnswer(id, head, class string) {
	e.t.Helper()
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET answer_head=?, answer_class=? WHERE id=?`,
		head, class, id); err != nil {
		e.t.Fatal(err)
	}
}

// closeNoPR is closeWithPRs with no PR and a last answer of class ("" for none read).
func (e *deliveryEnv) closeNoPR(id, turn string, ms int64, closedAt time.Time, class string) {
	e.t.Helper()
	e.closeWithPRs(id, turn, ms, closedAt, store.PRStateNone)
	e.setAnswer(id, "head of a "+class+" answer", class)
}

// setPRs writes a delivery's PR snapshot as a read of the Agent would leave it.
func (e *deliveryEnv) setPRs(id, prState string) {
	e.t.Helper()
	snap := map[string]string{store.PRStateNone: "[]", store.PRStateGone: "",
		store.PRStateMerged:  `[{"provider":"github","key":"o/r#7","state":"merged"}]`,
		store.PRStateWaiting: `[{"provider":"github","key":"o/r#7","state":"open"}]`,
		store.PRStateClosed:  `[{"provider":"github","key":"o/r#7","state":"closed"}]`}[prState]
	at := store.DeliveryTS(time.Now())
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET pr_state=?, pr_snapshot=?, pr_read_at=?, pr_changed_at=? WHERE id=?`,
		prState, snap, at, at, id); err != nil {
		e.t.Fatal(err)
	}
}

// statsOf is the dispatcher's SQL statistics and pendingOf for one queue.
func (e *deliveryEnv) statsOf(queueID string) (sql, goSide store.QueuePending) {
	e.t.Helper()
	all, err := e.st.ListMemberQueuePendingStats(context.Background(), e.members["a"])
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.st.MemoTx(context.Background(), e.members["a"], func(tx store.MemoTxOps) error {
		c, _, err := tx.GetCategory(queueID)
		if err != nil {
			return err
		}
		rows, err := tx.ListMemosIn(c.Repo, c.Name)
		if err != nil {
			return err
		}
		goSide = pendingOf(rows, memoafter.EligibleIn(memoafter.TxLookup(tx, rows, time.Now())), time.Now())
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return all[queueID], goSide
}

// The SQL statistics and the Go rule agree on every state a prerequisite can be in, and only a
// clean close with a good first turn satisfies it, and a run without a PR only when its last answer
// reads as done.
func TestMemoAfterSQLAgreesWithGo(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		cases := []struct {
			name string
			set  func(p memoDTO, q string)
			ok   bool
		}{
			{"pending", func(memoDTO, string) {}, false},
			{"claimed", func(_ memoDTO, q string) { e.fired(q) }, false},
			{"open delivery", func(p memoDTO, q string) {
				d := e.fired(q)
				e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET state='open' WHERE id=?`, d.ID)
				e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='delivered', sent_at=? WHERE id=?`, store.NowTS(), p.ID)
			}, false},
			{"clean, clean turn, no PR, answer done", func(_ memoDTO, q string) { e.closeNoPR(e.fired(q).ID, "clean", 5_000, now, store.AnswerDone) }, true},
			// A run without a PR that did not say Done: (issue #4876), or whose answer is not read.
			{"clean, clean turn, no PR, answer not read", func(_ memoDTO, q string) { e.closeNoPR(e.fired(q).ID, "clean", 5_000, now, "") }, false},
			{"clean, clean turn, no PR, answer partial", func(_ memoDTO, q string) { e.closeNoPR(e.fired(q).ID, "clean", 5_000, now, store.AnswerPartial) }, false},
			{"clean, clean turn, no PR, answer other", func(_ memoDTO, q string) { e.closeNoPR(e.fired(q).ID, "clean", 5_000, now, store.AnswerOther) }, false},
			{"clean, clean turn, no PR, answer unknown", func(_ memoDTO, q string) { e.closeNoPR(e.fired(q).ID, "clean", 5_000, now, store.AnswerUnknown) }, false},
			// A merged PR satisfies whatever the answer says.
			{"clean, clean turn, merged, answer partial", func(_ memoDTO, q string) {
				d := e.fired(q)
				e.closeWithPRs(d.ID, "clean", 5_000, now, store.PRStateMerged)
				e.setAnswer(d.ID, "Partial: part 1", store.AnswerPartial)
			}, true},
			{"clean, clean turn, merged", func(_ memoDTO, q string) { e.closeWithPRs(e.fired(q).ID, "clean", 5_000, now, store.PRStateMerged) }, true},
			// Step 2: a snapshot not taken, a PR open, closed unmerged or a session gone.
			{"clean, clean turn, no snapshot", func(_ memoDTO, q string) { e.closeDeliveryAs(e.fired(q).ID, "clean", "clean", 5_000, now) }, false},
			{"clean, clean turn, PR open", func(_ memoDTO, q string) { e.closeWithPRs(e.fired(q).ID, "clean", 5_000, now, store.PRStateWaiting) }, false},
			{"clean, clean turn, PR closed", func(_ memoDTO, q string) { e.closeWithPRs(e.fired(q).ID, "clean", 5_000, now, store.PRStateClosed) }, false},
			{"clean, clean turn, session gone", func(_ memoDTO, q string) { e.closeWithPRs(e.fired(q).ID, "clean", 5_000, now, store.PRStateGone) }, false},
			// A first turn not recorded: only a merged PR shows a success.
			{"clean, no turn, merged", func(_ memoDTO, q string) { e.closeWithPRs(e.fired(q).ID, "", 0, now, store.PRStateMerged) }, true},
			{"clean, no turn, no PR", func(_ memoDTO, q string) { e.closeWithPRs(e.fired(q).ID, "", 0, now, store.PRStateNone) }, false},
			{"clean, no turn, no PR, answer done", func(_ memoDTO, q string) { e.closeNoPR(e.fired(q).ID, "", 0, now, store.AnswerDone) }, false},
			{"clean, quiet kind, long turn", func(_ memoDTO, q string) { e.closeNoPR(e.fired(q).ID, "none", 120_000, now, store.AnswerDone) }, true},
			{"clean, quiet kind, quick turn", func(_ memoDTO, q string) { e.closeDeliveryAs(e.fired(q).ID, "clean", "none", 30_000, now) }, false},
			{"clean, failed turn", func(_ memoDTO, q string) { e.closeDeliveryAs(e.fired(q).ID, "clean", "turn-failed", 120_000, now) }, false},
			// The duration alone never stands in for a first turn that was not recorded.
			{"clean, no turn yet", func(_ memoDTO, q string) { e.closeDeliveryAs(e.fired(q).ID, "clean", "", 120_000, now) }, false},
			{"clean, overdue retire", func(_ memoDTO, q string) {
				d := e.fired(q)
				e.closeDeliveryAs(d.ID, "clean", "clean", 5_000, now)
				e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET overdue_at=?, retire_gen=3 WHERE id=?`, store.DeliveryTS(now), d.ID)
			}, false},
			{"aborted", func(_ memoDTO, q string) { e.closeDeliveryAs(e.fired(q).ID, "aborted", "clean", 5_000, now) }, false},
			{"sent by hand", func(p memoDTO, _ string) { e.markSent(p.ID) }, false},
			{"deleted", func(p memoDTO, _ string) {
				e.st.DB().ExecContext(context.Background(), `DELETE FROM memo WHERE id=?`, p.ID)
			}, false},
		}
		e.useQueueAgent("")
		for i, c := range cases {
			name := "q" + itoaTest(i)
			q := e.queueWith("app", name, map[string]any{"maxActive": 6})
			p := e.pending("app", name, "prerequisite")
			rec := e.do("a", "POST", "/api/memos", afterMemo("app", name, "dependent", p.ID))
			if rec.Code != http.StatusCreated {
				t.Fatalf("%s: dependent: %d %s", c.name, rec.Code, rec.Body.String())
			}
			d := decode[memoDTO](t, rec)
			c.set(p, q)
			sqlSide, goSide := e.statsOf(q)
			if sqlSide.Eligible != goSide.Eligible || sqlSide.Count != goSide.Count {
				t.Fatalf("%s: SQL %+v, Go %+v", c.name, sqlSide, goSide)
			}
			wantPending := 1
			if c.name == "pending" {
				wantPending = 2 // the prerequisite itself, which has no prerequisite
			}
			wantEligible := 0
			if c.ok {
				wantEligible = 1
			}
			if c.name == "pending" {
				wantEligible = 1
			}
			if goSide.Count != wantPending || goSide.Eligible != wantEligible {
				t.Fatalf("%s: %+v, want %d pending and %d eligible", c.name, goSide, wantPending, wantEligible)
			}
			if got := e.memoRow(d.ID); got.State != store.MemoStatePending {
				t.Fatalf("%s: the dependent is %s before any tick", c.name, got.State)
			}
		}
	})
}

// A press and the dispatcher claim only eligible messages, skip a blocked one without stopping the
// queue, fire a dependent once its prerequisite closed clean, and put a dependent whose
// prerequisite went wrong to held with the reason.
func TestMemoAfterDispatch(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"maxActive": 3}, 0)
		a := e.pending("app", "impl", "a")
		b := decode[memoDTO](t, e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "b waits for a", a.ID)))
		c := e.pending("app", "impl", "c")

		// A dependent is never batched with its prerequisite.
		if s, g := e.statsOf(q); s.Count != 3 || s.Eligible != 2 || g.Eligible != 2 {
			t.Fatalf("stats %+v / %+v, want 3 pending, 2 eligible", s, g)
		}
		da := e.fired(q)
		dc := e.fired(q)
		if !sameIDs(da.MessageIDs, a.ID) || !sameIDs(dc.MessageIDs, c.ID) {
			t.Fatalf("claimed %v then %v, want a then c (b skipped)", da.MessageIDs, dc.MessageIDs)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict || deliveryErrCode(t, rec) != errCodeMemoQueueNothingPending {
			t.Fatalf("a press with only a blocked message: %d %s", rec.Code, rec.Body.String())
		}
		// The waiting reason names the blocked message, and the list says what it waits for.
		if w := e.queueWaiting(q); w == nil || w.Reason != waitPrerequisites || w.Detail == nil || w.Detail.Blocked != 1 {
			t.Fatalf("waiting %+v, want prerequisites", w)
		}
		// A press would be refused too, and the list says so for the same reason.
		if fb := e.queueFireBlock(q); fb == nil || fb.Reason != waitPrerequisites || fb.Detail == nil || fb.Detail.Blocked != 1 {
			t.Fatalf("fireBlock %+v, want prerequisites", fb)
		}
		if st := e.listed(b.ID).AfterStatus; len(st) != 1 || st[0].ID != a.ID || st[0].State != memoafter.Waiting || st[0].Reason != memoafter.WhyRunning {
			t.Fatalf("after status %+v", st)
		}
		// Closed clean, first turn not collected yet: still waiting, and a tick holds nothing.
		e.closeDeliveryAs(da.ID, store.DeliveryClean, "", 0, time.Now())
		e.runner().checkAfter(context.Background(), e.queueRow(q), time.Now())
		if got := e.memoRow(b.ID); got.State != store.MemoStatePending {
			t.Fatalf("b is %s while a's first turn is not collected", got.State)
		}
		// Once the first turn is recorded and good, and the session's PRs read as merged, b is claimed.
		e.closeDeliveryAs(da.ID, store.DeliveryClean, "clean", 90_000, time.Now())
		if rec := e.fire(q); rec.Code != http.StatusConflict {
			t.Fatalf("b fired before a's PRs were read: %d %s", rec.Code, rec.Body.String())
		}
		if st := e.listed(b.ID).AfterStatus; len(st) != 1 || st[0].Reason != memoafter.WhyPR {
			t.Fatalf("after status %+v, want waiting for the PRs", st)
		}
		e.setPRs(da.ID, store.PRStateMerged)
		// Its prerequisite is done: the block lifts before anything fires.
		if fb := e.queueFireBlock(q); fb != nil {
			t.Fatalf("fireBlock %+v after the prerequisite finished", fb)
		}
		if db := e.fired(q); !sameIDs(db.MessageIDs, b.ID) {
			t.Fatalf("claimed %v, want b", db.MessageIDs)
		}

		// A prerequisite that went wrong sends the dependent to held, with the reason.
		x := e.pending("app", "impl", "x")
		y := decode[memoDTO](t, e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "y waits for x", x.ID)))
		e.closeDeliveryAs(dc.ID, store.DeliveryClean, "clean", 90_000, time.Now()) // a slot for x
		dx := e.fired(q)
		e.closeDeliveryAs(dx.ID, store.DeliveryAborted, "clean", 90_000, time.Now())
		e.runner().checkAfter(context.Background(), e.queueRow(q), time.Now())
		got := e.memoRow(y.ID)
		hr := memoafter.ParseHeldReason(got.HeldReason)
		if got.State != store.MemoStateHeld || got.ReleaseHash != "" || hr == nil || hr.After != x.ID || hr.Reason != memoafter.WhyAborted {
			t.Fatalf("y after x aborted: %+v (%+v)", got, hr)
		}
		if l := e.listed(y.ID); l.HeldReason == nil || l.HeldReason.Reason != memoafter.WhyAborted || len(l.AfterStatus) != 1 || l.AfterStatus[0].State != memoafter.Failed {
			t.Fatalf("listed y %+v", l)
		}
		// The person's release brings it back and clears the reason; it is held again on the next pass.
		if r := e.release(y.ID, memoContentHash(got)); r.Code != http.StatusOK {
			t.Fatalf("release: %d %s", r.Code, r.Body.String())
		}
		if got := e.memoRow(y.ID); got.State != store.MemoStatePending || got.HeldReason != "" {
			t.Fatalf("released y %+v", got)
		}
		e.assertPendingReleaseMatches()
	})
}

// A threshold counts only eligible messages, and a draining queue drains while any is pending.
func TestMemoAfterThresholdCountsEligible(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"openAfter": 2}, 0)
		p := e.pending("app", "impl", "p")
		e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='' WHERE id=?`, p.ID)
		e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "d1", p.ID))
		e.pending("app", "impl", "free")
		w := e.queueWaiting(q)
		if w == nil || w.Reason != waitBelowThreshold || w.Detail == nil || w.Detail.Waiting != 1 || w.Detail.Blocked != 1 {
			t.Fatalf("waiting %+v, want below_threshold with 1 waiting and 1 blocked", w)
		}
		// A blocked message keeps a drain going (the total), but opens nothing by itself.
		pend := store.QueuePending{Count: 1, Eligible: 0}
		if open, _ := openState(store.MemoQueue{OpenAfter: 1, Draining: true}, pend, time.Now()); !open {
			t.Fatal("a draining queue closed while a blocked message is pending")
		}
		if open, _ := openState(store.MemoQueue{OpenAfter: 1}, pend, time.Now()); open {
			t.Fatal("a blocked message opened a threshold")
		}
	})
}

// A message blocked longer than max_task_hours since it became pending is told once.
func TestMemoAfterBlockedTooLongIsToldOnce(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", map[string]any{"maxTaskHours": 1}, 0)
		p := e.pending("app", "impl", "p")
		e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='' WHERE id=?`, p.ID)
		d := decode[memoDTO](t, e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "d", p.ID)))
		r := e.runner()
		r.checkAfter(context.Background(), e.queueRow(q), time.Now())
		if n := e.notificationsOf(memoQueueBlockedKind); n != 0 {
			t.Fatalf("%d notices before the bound", n)
		}
		later := time.Now().Add(2 * time.Hour)
		r.checkAfter(context.Background(), e.queueRow(q), later)
		r.checkAfter(context.Background(), e.queueRow(q), later.Add(time.Minute))
		if n := e.notificationsOf(memoQueueBlockedKind); n != 1 {
			t.Fatalf("%d notices, want 1", n)
		}
		if got := e.memoRow(d.ID); got.State != store.MemoStatePending {
			t.Fatalf("a dependent of a held prerequisite is %s, want pending", got.State)
		}
	})
}

// The sweep keeps a delivered message that a waiting message names, and only that one.
func TestMemoAfterSweepKeepsAPrerequisite(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		console := memoFaceClient{"console", e}
		_, p := console.create(textMemo("r", "q", "prerequisite"))
		_, other := console.create(textMemo("r", "q", "unrelated"))
		_, d := console.create(afterMemo("r", "q", "dependent", p.ID))
		old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
		for _, id := range []string{p.ID, other.ID} {
			if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='delivered', sent_at=? WHERE id=?`, old, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.st.SweepSentMemos(context.Background(), memoRetainBefore()); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := e.st.GetMemo(context.Background(), other.ID); ok {
			t.Fatal("the sweep kept an unrelated old message")
		}
		if _, ok, _ := e.st.GetMemo(context.Background(), p.ID); !ok {
			t.Fatal("the sweep deleted a prerequisite a pending message waits for")
		}
		e.markSent(d.ID)
		if err := e.st.SweepSentMemos(context.Background(), memoRetainBefore()); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := e.st.GetMemo(context.Background(), p.ID); ok {
			t.Fatal("the sweep kept a prerequisite nothing waits for any more")
		}
	})
}

// queueWaiting is the "waiting" the member's queue list shows for one queue.
func (e *deliveryEnv) queueWaiting(queueID string) *memoWaiting {
	e.t.Helper()
	for _, q := range decode[struct{ Queues []memoQueueDTO }](e.t, e.do("a", "GET", "/api/memo-queues", nil)).Queues {
		if q.CategoryID == queueID {
			return q.Waiting
		}
	}
	e.t.Fatal("queue not listed")
	return nil
}

// queueFireBlock is the fireBlock of the queue's row in the member's list: why a press of "Run
// now" would be refused (nil when it would claim something).
func (e *deliveryEnv) queueFireBlock(queueID string) *memoWaiting {
	e.t.Helper()
	for _, q := range decode[struct{ Queues []memoQueueDTO }](e.t, e.do("a", "GET", "/api/memo-queues", nil)).Queues {
		if q.CategoryID == queueID {
			return q.FireBlock
		}
	}
	e.t.Fatal("queue not listed")
	return nil
}

// listed is one message as the member's list shows it.
func (e *deliveryEnv) listed(id string) memoDTO {
	e.t.Helper()
	for _, m := range decode[[]memoDTO](e.t, e.do("a", "GET", "/api/memos", nil)) {
		if m.ID == id {
			return m
		}
	}
	e.t.Fatal("message not listed")
	return memoDTO{}
}

// A tidy run only keeps a message that has or is a prerequisite: a merge of a dependent with its
// prerequisite drops the whole reply.
func TestMemoAfterTidyKeepsLinkedMessages(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.queue(q)
		console := memoFaceClient{"console", e}
		_, p := console.create(textMemo("r", "q", "prerequisite"))
		_, d := console.create(afterMemo("r", "q", "dependent", p.ID))
		var input []memoTidyItem
		for _, id := range []string{p.ID, d.ID} {
			m := e.memoRow(id)
			input = append(input, memoTidyItem{ID: id, Hash: memoContentHash(m), State: m.State})
		}
		out, err := memoTidyApply(context.Background(), e.st, memoTidyApplyIn{MembershipID: e.members["a"], QueueID: q,
			Trigger: store.TidyTriggerManual, Input: input,
			Ops: []memotidy.Op{{Op: memotidy.OpMerge, IDs: []string{p.ID, d.ID}, Text: "both"}}})
		if err != nil {
			t.Fatal(err)
		}
		if out.Run.State != store.TidyRunDropped || out.Run.Reason != memotidy.CodeKeepOnly {
			t.Fatalf("run %+v, want dropped with %s", out.Run, memotidy.CodeKeepOnly)
		}
		if got := e.memoRow(d.ID); got.State != store.MemoStatePending {
			t.Fatalf("the dependent is %s", got.State)
		}
	})
}

// A message that leaves every queue (its settings removed, its category deleted or merged into a
// plain one) waits for nothing: the list is dropped with the state, so the note can still move.
func TestMemoAfterLeavingTheQueueDropsTheList(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		q := e.category("r", "q")
		e.queue(q)
		e.category("r", "notes")
		console := memoFaceClient{"console", e}
		_, p := console.create(textMemo("r", "q", "prerequisite"))
		_, d := console.create(afterMemo("r", "q", "dependent", p.ID))
		if rec := e.do("a", "DELETE", "/api/memo-queues/"+q, nil); rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
			t.Fatalf("delete settings: %d %s", rec.Code, rec.Body.String())
		}
		if got := e.memoRow(d.ID); got.State != "" || got.AfterIDs != "" || got.HeldReason != "" {
			t.Fatalf("a plain note kept %+v", got)
		}
		if code, _ := console.update(d.ID, map[string]any{"category": "notes"}); code != http.StatusOK {
			t.Fatalf("moving the plain note: %d", code)
		}
	})
}

// Changing what a held message waits for drops the reason it came back for, which named the old list.
func TestMemoAfterEditClearsTheHeldReason(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		console := memoFaceClient{"console", e}
		_, p := console.create(textMemo("r", "q", "prerequisite"))
		_, d := console.create(afterMemo("r", "q", "dependent", p.ID))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='',
			held_reason=? WHERE id=?`, `{"after":"`+p.ID+`","reason":"aborted"}`, d.ID); err != nil {
			t.Fatal(err)
		}
		if code, _ := console.update(d.ID, map[string]any{"body": "dependent, edited"}); code != http.StatusOK || e.memoRow(d.ID).HeldReason == "" {
			t.Fatalf("a text edit dropped the reason: %d", code)
		}
		if code, up := console.update(d.ID, map[string]any{"after": []string{}}); code != http.StatusOK || up.HeldReason != nil || e.memoRow(d.ID).HeldReason != "" {
			t.Fatalf("the reason outlived the list: %d %+v", code, up)
		}
	})
}

// A claimed message keeps its prerequisite from the sweep: its delivery may not be sent, and the
// message then waits again.
func TestMemoAfterSweepKeepsAPrerequisiteOfAClaim(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q"))
		console := memoFaceClient{"console", e}
		_, p := console.create(textMemo("r", "q", "prerequisite"))
		_, d := console.create(afterMemo("r", "q", "dependent", p.ID))
		old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
		db := e.st.DB()
		if _, err := db.ExecContext(context.Background(), `UPDATE memo SET state='delivered', sent_at=? WHERE id=?`, old, p.ID); err != nil {
			t.Fatal(err)
		}
		e.setStateKeepingRelease(d.ID, store.MemoStateClaimed)
		if err := e.st.SweepSentMemos(context.Background(), memoRetainBefore()); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := e.st.GetMemo(context.Background(), p.ID); !ok {
			t.Fatal("the sweep deleted a prerequisite of a claimed message")
		}
	})
}

// With an Agent that cannot show a reference, a queue whose only eligible message carries one and
// whose other message waits for it says why nothing starts.
func TestMemoAfterWaitingReasonsCountEligibleOnly(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", nil, 0)
		rec := e.do("a", "POST", "/api/memos", refMemo("app", "impl", "with a reference", ghRef("5")))
		m1 := decode[memoDTO](t, rec)
		e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "waits for it", m1.ID))
		if err := e.st.SetMemoQueueNoRefs(context.Background(), q, store.DeliveryTS(time.Now())); err != nil {
			t.Fatal(err)
		}
		if w := e.queueWaiting(q); w == nil || w.Reason != waitNoRefs {
			t.Fatalf("waiting %+v, want %s", w, waitNoRefs)
		}
		if s, g := e.statsOf(q); s.Refs != 1 || g.Refs != 1 || s.Eligible != 1 || g.Eligible != 1 {
			t.Fatalf("stats SQL %+v / Go %+v", s, g)
		}
	})
}

// The hold pass leaves a row in a hand send alone, whatever its prerequisites say.
func TestMemoAfterHoldSkipsAHandSend(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, q := e.autoQueue("app", "impl", nil, 0)
		p := e.pending("app", "impl", "p")
		d := decode[memoDTO](t, e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "d", p.ID)))
		db := e.st.DB()
		if _, err := db.ExecContext(context.Background(), `DELETE FROM memo WHERE id=?`, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(context.Background(), `UPDATE memo SET send_token='tok', send_started_at=? WHERE id=?`,
			time.Now().UTC().Format(time.RFC3339), d.ID); err != nil {
			t.Fatal(err)
		}
		e.runner().checkAfter(context.Background(), e.queueRow(q), time.Now())
		if got := e.memoRow(d.ID); got.State != store.MemoStatePending || got.SendToken != "tok" {
			t.Fatalf("a row in a hand send became %+v", got)
		}
	})
}

// A message may wait for one queued after it: the dispatcher passes over the older one, runs the
// newer, and runs the older on the tick after the newer is satisfied.
func TestMemoAfterALaterPrerequisiteDispatches(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, _ = e.autoQueue("app", "impl", map[string]any{"maxActive": 3}, 0)
		a := e.pending("app", "impl", "a, older")
		b := e.pending("app", "impl", "b, newer")
		if rec := e.do("a", "PATCH", "/api/memos/"+a.ID, map[string]any{"after": []string{b.ID}}); rec.Code != http.StatusOK {
			t.Fatalf("a waits for b: %d %s", rec.Code, rec.Body.String())
		}
		now := time.Now()
		fired, _ := e.autoTick(now)
		if len(fired) != 1 || !sameIDs(fired[0].MessageIDs, b.ID) {
			t.Fatalf("fired %+v, want b alone", fired)
		}
		if again, _ := e.autoTick(now.Add(time.Minute)); len(again) != 0 {
			t.Fatalf("fired %+v while b runs", again)
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET held_by='', held_session='' WHERE id=?`, fired[0].ID); err != nil {
			t.Fatal(err)
		}
		e.closeWithPRs(fired[0].ID, "clean", 90_000, now.Add(2*time.Minute), store.PRStateMerged)
		if next, _ := e.autoTick(now.Add(3 * time.Minute)); len(next) != 1 || !sameIDs(next[0].MessageIDs, a.ID) {
			t.Fatalf("fired %+v, want a", next)
		}
	})
}

// The cycle walk follows a link into another category: a cycle that a later merge of the two
// categories would close is refused when its last link is written.
func TestMemoAfterCycleAcrossCategories(t *testing.T) {
	eachDialect(t, func(t *testing.T, e *inboxEnv) {
		e.queue(e.category("r", "q1"))
		e.queue(e.category("r", "q2"))
		console := memoFaceClient{"console", e}
		_, a := console.create(textMemo("r", "q1", "a"))
		_, b := console.create(textMemo("r", "q1", "b"))
		_, c := console.create(textMemo("r", "q2", "c"))
		if code, _ := console.update(a.ID, map[string]any{"after": []string{b.ID}}); code != http.StatusOK {
			t.Fatalf("a waits for b: %d", code)
		}
		if code, up := console.update(b.ID, map[string]any{"category": "q2", "after": []string{c.ID}}); code != http.StatusOK || !sameIDs(up.After, c.ID) {
			t.Fatalf("b moves to q2 and waits for c: %d %+v", code, up)
		}
		rec := e.do("a", "PATCH", "/api/memos/"+c.ID, map[string]any{"category": "q1", "after": []string{a.ID}})
		if rec.Code != http.StatusBadRequest || deliveryErrCode(t, rec) != errCodeMemoAfterInvalid {
			t.Fatalf("c waits for a through b in another category: %d %s", rec.Code, rec.Body.String())
		}
	})
}
