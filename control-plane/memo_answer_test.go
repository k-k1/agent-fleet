package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/memoafter"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The last answer of a delivery's root session (issue #4876): read after the close, kept on the
// delivery, and for a run without a PR the only thing that tells a finished run from one that
// stopped part way.

// transcriptReply is an Agent's GET /sessions/{name}/messages answer whose last answer is text,
// after a tool call in the same turn.
func transcriptReply(status string, alive bool, text string) func() (int, any) {
	return func() (int, any) {
		return http.StatusOK, map[string]any{"name": "q-a", "status": status, "alive": alive, "cursor": 9, "messages": []map[string]any{
			{"role": "user", "text": "the task", "parts": []map[string]any{{"kind": "text", "text": "the task"}}},
			{"role": "assistant", "text": "Looking.\n" + text, "parts": []map[string]any{
				{"kind": "text", "text": "Looking."}, {"kind": "tool", "tool": "Bash"}, {"kind": "text", "text": text}}},
			{"role": "assistant", "sidechain": true, "text": "Done: a subagent's words", "parts": []map[string]any{
				{"kind": "text", "text": "Done: a subagent's words"}}},
		}}
	}
}

// addSession puts a row for session in the Agent's list, as a running Agent reports it.
func (a *fakeQueueAgent) addSession(name string, kv map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	row := map[string]any{"name": name, "kind": "claude", "alive": false, "state": "stopped"}
	for k, v := range kv {
		row[k] = v
	}
	a.rows = append(a.rows, row)
}

// noPRChain is prChain whose session opened no PR.
func (e *deliveryEnv) noPRChain(closedAt time.Time) (*fakeQueueAgent, string, store.MemoDelivery, memoDTO) {
	e.t.Helper()
	agent, q, da, b := e.prChain("clean", closedAt)
	agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
	return agent, q, da, b
}

// A run without a PR whose last answer says Done: satisfies its dependent once the answer is read,
// and the answer is read once, from the tail of the root session, and shown on the run.
func TestMemoAnswerDoneSatisfies(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.noPRChain(now.Add(-time.Minute))
		// Before the answer is read the dependent waits for it, and fires for nothing else.
		e.setPRs(da.ID, store.PRStateNone)
		if st := e.listed(b.ID).AfterStatus; len(st) != 1 || st[0].State != memoafter.Waiting || st[0].Reason != memoafter.WhyAnswer {
			t.Fatalf("after status %+v, want waiting for the answer", st)
		}
		if rec := e.fire(q); rec.Code != http.StatusConflict {
			t.Fatalf("b fired before a's answer was read: %d %s", rec.Code, rec.Body.String())
		}
		agent.setAnswer("q-a", transcriptReply("idle", true, "**Done:** fixed #1\n\nDetails follow."))
		e.settleNow(now)
		calls := agent.answerCallsSoFar()
		if len(calls) != 1 || !strings.Contains(calls[0], "tail=1") || !strings.HasPrefix(calls[0], "q-a?") {
			t.Fatalf("reads %v, want one tail read of q-a", calls)
		}
		got := e.delivery(da.ID)
		if got.AnswerClass != store.AnswerDone || got.AnswerHead != "**Done:** fixed #1" {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
		if dto := memoDeliveryToDTO(got); dto.Answer != got.AnswerHead || dto.AnswerClass != store.AnswerDone {
			t.Fatalf("listed %+v", dto)
		}
		e.settleNow(now.Add(time.Hour))
		if n := len(agent.answerCallsSoFar()); n != 1 {
			t.Fatalf("a kept answer was read again (%d reads)", n)
		}
		if d := e.fired(q); !sameIDs(d.MessageIDs, b.ID) {
			t.Fatalf("claimed %v, want b", d.MessageIDs)
		}
	})
}

// Every answer that does not say Done: sends the dependent to held with the head, and so does one
// that cannot be told: a response that ended on a tool call, a transcript with no answer at all,
// or a kind without a transcript.
func TestMemoAnswerNotDoneHolds(t *testing.T) {
	cases := []struct {
		name, head, class string
		reply             func() (int, any)
		listed            bool // the Agent's list has the session
	}{
		{"partial", "Partial: #1 part 1, next part queued", store.AnswerPartial,
			transcriptReply("idle", true, "Partial: #1 part 1, next part queued\nmore"), false},
		{"held", "Held: #1 part 2, PR #9 not merged", store.AnswerPartial, transcriptReply("stopped", false, "Held: #1 part 2, PR #9 not merged"), true},
		{"other", "I stopped at the review limit.", store.AnswerOther, transcriptReply("idle", true, "I stopped at the review limit."), false},
		{"ended on a tool call", "", store.AnswerUnknown, func() (int, any) {
			return http.StatusOK, map[string]any{"name": "q-a", "status": "idle", "alive": true, "messages": []map[string]any{
				{"role": "assistant", "parts": []map[string]any{{"kind": "text", "text": "Done: wrote it"}}},
				{"role": "assistant", "parts": []map[string]any{{"kind": "tool", "tool": "Bash"}}}}}
		}, false},
		{"no answer in the whole transcript", "", store.AnswerUnknown, func() (int, any) {
			return http.StatusOK, map[string]any{"name": "q-a", "status": "idle", "alive": true, "firstLine": 0, "hasMore": false,
				"messages": []map[string]any{{"role": "user", "text": "the task", "parts": []map[string]any{{"kind": "text", "text": "the task"}}}}}
		}, false},
		{"no transcript", "", store.AnswerUnknown, func() (int, any) {
			return http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "unsupported_kind", "message": "x"}}
		}, true},
	}
	for _, c := range cases {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			now := time.Now()
			agent, q, da, b := e.noPRChain(now.Add(-time.Minute))
			if c.listed {
				agent.addSession("q-a", nil)
			}
			agent.setAnswer("q-a", c.reply)
			e.settleNow(now)
			got := e.delivery(da.ID)
			if got.AnswerClass != c.class || got.AnswerHead != c.head {
				t.Fatalf("%s: kept %q %q", c.name, got.AnswerHead, got.AnswerClass)
			}
			e.runner().checkAfter(context.Background(), e.queueRow(q), now)
			row := e.memoRow(b.ID)
			hr := memoafter.ParseHeldReason(row.HeldReason)
			if row.State != store.MemoStateHeld || hr == nil || hr.Reason != memoafter.WhyNotDone {
				t.Fatalf("%s: b %+v (%+v)", c.name, row, hr)
			}
			if st := e.listed(b.ID).AfterStatus; len(st) != 1 || st[0].State != memoafter.Failed || st[0].Answer != c.head {
				t.Fatalf("%s: after status %+v", c.name, st)
			}
		})
	}
}

// A read that is no answer of the Agent's transcript decides nothing: no route, a 404, an error, a
// cut reply, one about another session or without a status, or a session working again. The dependent keeps waiting, the read
// is made again after 5 minutes, and once the window is over the dependent goes to held.
func TestMemoAnswerNoAnswerKeepsWaiting(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.noPRChain(now.Add(-time.Minute))
		at := now
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle"})
		gone := func() (int, any) {
			return http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": "x"}}
		}
		withName := func(name string) func() (int, any) {
			return func() (int, any) {
				_, body := transcriptReply("idle", true, "Done: someone else's")()
				body.(map[string]any)["name"] = name
				return http.StatusOK, body
			}
		}
		noStatus := func() (int, any) {
			_, body := transcriptReply("idle", true, "Done: x")()
			delete(body.(map[string]any), "status")
			return http.StatusOK, body
		}
		for _, reply := range []func() (int, any){
			nil,  // no route: a plain 404
			gone, // a 404: a gateway's, or the Agent's for a session it no longer has, waits for the window
			func() (int, any) {
				return http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "down"}}
			},
			func() (int, any) { return http.StatusOK, map[string]any{"name": "q-a", "status": "idle"} }, // no messages
			func() (int, any) { // read empty, as a transcript that could not be read
				return http.StatusOK, map[string]any{"name": "q-a", "status": "idle", "messages": []any{}, "firstLine": 0, "hasMore": false}
			},
			withName("q-other"),
			noStatus,
			transcriptReply("working", true, "Done: not yet"),
		} {
			agent.setAnswer("q-a", reply)
			before := len(agent.answerCallsSoFar())
			e.settleNow(at)
			if n := len(agent.answerCallsSoFar()); n != before+1 {
				t.Fatalf("%d reads at %v, want one more than %d", n, at, before)
			}
			if got := e.delivery(da.ID); got.AnswerClass != "" {
				t.Fatalf("kept %q from no answer", got.AnswerClass)
			}
			e.settleNow(at.Add(time.Minute))
			if n := len(agent.answerCallsSoFar()); n != before+1 {
				t.Fatalf("read again within 5 minutes (%d reads)", n)
			}
			at = at.Add(6 * time.Minute)
		}
		e.runner().checkAfter(context.Background(), e.queueRow(q), at)
		if row := e.memoRow(b.ID); row.State != store.MemoStatePending {
			t.Fatalf("b is %s inside the window", row.State)
		}
		late := now.Add(25 * time.Hour)
		e.settleNow(late)
		if dto := memoDeliveryToDTOAt(e.delivery(da.ID), late); dto.AnswerClass != store.AnswerUnknown || dto.Answer != "" {
			t.Fatalf("listed after the window %+v", dto)
		}
		e.runner().checkAfter(context.Background(), e.queueRow(q), late)
		row := e.memoRow(b.ID)
		if hr := memoafter.ParseHeldReason(row.HeldReason); row.State != store.MemoStateHeld || hr == nil || hr.Reason != memoafter.WhyNotDone {
			t.Fatalf("b after the window %+v", row)
		}
	})
}

// A kept answer is never rewritten (a later turn of the session is not what the run ended with),
// and none is kept once the window to read it ended.
func TestMemoAnswerWrittenOnce(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, _, da, _ := e.noPRChain(time.Now().Add(-time.Minute))
		ctx := context.Background()
		since := store.DeliveryTS(time.Now().Add(-memoAnswerWindow))
		if ok, _ := e.st.RecordAnswer(ctx, da.ID, "Done: late", store.AnswerDone, store.DeliveryTS(time.Now())); ok {
			t.Fatal("an answer was kept after its window")
		}
		if ok, err := e.st.RecordAnswer(ctx, da.ID, "Partial: x", store.AnswerPartial, since); err != nil || !ok {
			t.Fatalf("first: %v %v", ok, err)
		}
		if ok, _ := e.st.RecordAnswer(ctx, da.ID, "Done: y", store.AnswerDone, since); ok {
			t.Fatal("a kept answer was overwritten")
		}
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerPartial || got.AnswerHead != "Partial: x" {
			t.Fatalf("%+v", got)
		}
	})
}

// A stopped workspace whose queue waits on an answer alone is woken to read it, as for a PR
// snapshot: a run that closed while the workspace was stopping is not held for the 24 hours.
func TestMemoAnswerWakes(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.noPRChain(now.Add(-time.Minute))
		e.setPRs(da.ID, store.PRStateNone)
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 1 || e.prWakes(now.Add(-time.Hour)) != 1 {
			t.Fatalf("%d wakes, want one", len(wakes))
		}
		agent.setAnswer("q-a", transcriptReply("stopped", false, "Done: nothing to change"))
		e.setRuntime(agent, "running")
		fired, _ := e.autoTick(now.Add(time.Minute))
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("not read right after the wake: %+v", got)
		}
		if len(fired) != 1 || !sameIDs(fired[0].MessageIDs, b.ID) || fired[0].QueueID != q {
			t.Fatalf("fired %+v, want b", fired)
		}
	})
}

// A turn that started after the close means a person took the session over: the end of its
// transcript is no longer the run's answer, and none is read from it.
func TestMemoAnswerTurnAfterTheClose(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.noPRChain(now.Add(-10 * time.Minute))
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle",
			"lastTurnStartAt": now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)})
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: the person's other task"))
		e.settleNow(now)
		if n := len(agent.answerCallsSoFar()); n != 0 {
			t.Fatalf("%d transcript reads of a session taken over", n)
		}
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerUnknown {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
		e.runner().checkAfter(context.Background(), e.queueRow(q), now)
		if row := e.memoRow(b.ID); row.State != store.MemoStateHeld {
			t.Fatalf("b is %s", row.State)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// The run's own last turn started before the close: read as usual.
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.addSession("q-a", map[string]any{"lastTurnStartAt": now.Add(-20 * time.Minute).UTC().Format(time.RFC3339)})
		agent.setAnswer("q-a", transcriptReply("stopped", false, "Done: x"))
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}

// An answer further back than the tail page is found by reading back page by page: claude counts
// raw log lines, and bookkeeping lines after the last answer hold no turn. Past the pages read the
// read decides nothing.
func TestMemoAnswerReadsBack(t *testing.T) {
	page := func(firstLine int, msgs []map[string]any) map[string]any {
		return map[string]any{"name": "q-a", "status": "idle", "alive": true, "messages": msgs, "firstLine": firstLine, "hasMore": firstLine > 0}
	}
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		calls := 0
		agent.setAnswer("q-a", func() (int, any) {
			calls++
			if calls == 1 {
				return http.StatusOK, page(800, []map[string]any{})
			}
			return http.StatusOK, page(600, []map[string]any{{"role": "assistant", "parts": []map[string]any{{"kind": "text", "text": "Done: far back"}}}})
		})
		e.settleNow(now)
		got := agent.answerCallsSoFar()
		if len(got) != 2 || !strings.Contains(got[0], "tail=1") || !strings.Contains(got[1], "before=800") {
			t.Fatalf("reads %v, want the tail then the page before line 800", got)
		}
		if d := e.delivery(da.ID); d.AnswerClass != store.AnswerDone || d.AnswerHead != "Done: far back" {
			t.Fatalf("kept %q %q", d.AnswerHead, d.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", func() (int, any) { return http.StatusOK, page(5000, []map[string]any{}) })
		e.settleNow(now)
		if n := len(agent.answerCallsSoFar()); n != memoAnswerPages {
			t.Fatalf("%d reads, want %d pages", n, memoAnswerPages)
		}
		if d := e.delivery(da.ID); d.AnswerClass != "" {
			t.Fatalf("kept %q from pages without an answer", d.AnswerClass)
		}
	})
}

// A chain A <- B <- C in a stopped workspace: C waits for B, which has not run, and that does not
// keep the read B waits for (A's answer) from waking the workspace.
func TestMemoAnswerWakesForAChain(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, b := e.noPRChain(now.Add(-time.Minute))
		e.setPRs(da.ID, store.PRStateNone)
		if rec := e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "c waits for b", b.ID)); rec.Code != http.StatusCreated {
			t.Fatalf("c: %d %s", rec.Code, rec.Body.String())
		}
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 1 {
			t.Fatalf("%d wakes, want one", len(wakes))
		}
	})
}

// While its workspace is stopped, a delivery no message waits for is stamped as tried, so such
// rows rotate instead of filling the list; one a message waits for is not, and is read as soon as
// the workspace runs.
func TestMemoAnswerStoppedRowsRotate(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, b := e.noPRChain(now.Add(-time.Minute))
		e.setRuntime(agent, "stopped")
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerTriedAt != "" {
			t.Fatalf("a waited-for read was stamped while stopped: %+v", got.AnswerTriedAt)
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='' WHERE id=?`, b.ID); err != nil {
			t.Fatal(err)
		}
		e.settleNow(now.Add(time.Minute))
		if got := e.delivery(da.ID); got.AnswerTriedAt == "" {
			t.Fatal("a read nothing waits for was not stamped while stopped")
		}
	})
}

// Each message is judged on its own for a wake: one that waits on a first turn does not keep the
// read another waits for from waking the workspace, and one that also waits for a held message
// asks for no wake, as a read could not let it run.
func TestMemoAnswerWakeJudgesEachMessage(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		e.setPRs(da.ID, store.PRStateNone)
		d := e.pending("app", "impl", "d")
		if rec := e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "c waits for d", d.ID)); rec.Code != http.StatusCreated {
			t.Fatalf("c: %d %s", rec.Code, rec.Body.String())
		}
		dd := e.fired(q)
		if !sameIDs(dd.MessageIDs, d.ID) {
			t.Fatalf("claimed %v, want d", dd.MessageIDs)
		}
		e.closeWithPRs(dd.ID, "", 0, now.Add(-time.Minute), store.PRStateNone) // no first turn recorded
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 1 {
			t.Fatalf("%d wakes, want one for b's read", len(wakes))
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, b := e.prChain("clean", now.Add(-time.Minute))
		e.setPRs(da.ID, store.PRStateWaiting)
		h := e.pending("app", "impl", "h")
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='' WHERE id=?`, h.ID); err != nil {
			t.Fatal(err)
		}
		if rec := e.do("a", "PATCH", "/api/memos/"+b.ID, map[string]any{"after": []string{da.MessageIDs[0], h.ID}}); rec.Code != http.StatusOK {
			t.Fatalf("b waits for a and h: %d %s", rec.Code, rec.Body.String())
		}
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now); len(wakes) != 0 {
			t.Fatalf("%d wakes for a message a read cannot let run", len(wakes))
		}
	})
}

// A retire answered released (keys typed in the session count for it) is no turn: with no turn
// started after the close, the run's answer is read as usual.
func TestMemoAnswerReleasedRetireStillReads(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET retire_result='released' WHERE id=?`, da.ID); err != nil {
			t.Fatal(err)
		}
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle",
			"lastTurnStartAt": now.Add(-20 * time.Minute).UTC().Format(time.RFC3339)})
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: the run's own"))
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}

// A 404 decides nothing even for a session the Agent's list lacks (it leaves archived sessions
// out): the next read that answers is kept, and the dependent fires.
func TestMemoAnswer404ThenAnswer(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": "x"}}
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != "" {
			t.Fatalf("kept %q from a 404", got.AnswerClass)
		}
		agent.setAnswer("q-a", transcriptReply("stopped", false, "Done: archived"))
		e.settleNow(now.Add(6 * time.Minute))
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
		if d := e.fired(q); !sameIDs(d.MessageIDs, b.ID) {
			t.Fatalf("claimed %v, want b", d.MessageIDs)
		}
	})
}

// A threshold the waiting messages would meet once their reads answer does not keep the reads
// from waking the workspace; the fire still counts only what is satisfied.
func TestMemoAnswerWakeUnderAThreshold(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		e.setPRs(da.ID, store.PRStateNone)
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_queue SET open_after=1 WHERE category_id=?`, q); err != nil {
			t.Fatal(err)
		}
		e.setRuntime(agent, "stopped")
		if _, wakes := e.autoTick(now.Add(5 * time.Minute)); len(wakes) != 1 { // past the quiet minute
			t.Fatalf("%d wakes, want one for b's read", len(wakes))
		}
	})
}

// collectAnswers pages its waited-for list past the rows of a stopped workspace: they stay
// unstamped (read at the start), and a running workspace's row behind them is read in the tick.
func TestMemoAnswerListPages(t *testing.T) {
	saved := memoAnswersListed
	memoAnswersListed = 1
	t.Cleanup(func() { memoAnswersListed = saved })
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: behind the stopped row"))
		// A second chain, closed earlier so it is listed first, moved to member b, whose
		// workspace is stopped.
		p2 := e.pending("app", "impl", "p2")
		d2rec := e.do("a", "POST", "/api/memos", afterMemo("app", "impl", "waits for p2", p2.ID))
		if d2rec.Code != http.StatusCreated {
			t.Fatalf("dependent: %d %s", d2rec.Code, d2rec.Body.String())
		}
		dep2 := decode[memoDTO](t, d2rec)
		d2 := e.fired(q)
		if !sameIDs(d2.MessageIDs, p2.ID) {
			t.Fatalf("claimed %v, want p2", d2.MessageIDs)
		}
		e.closeWithPRs(d2.ID, "clean", 90_000, now.Add(-2*time.Minute), store.PRStateNone)
		db := e.st.DB()
		for _, stmt := range []string{
			`UPDATE memo_delivery SET session='q-b', membership_id=? WHERE id=?`,
		} {
			if _, err := db.ExecContext(context.Background(), stmt, e.members["b"], d2.ID); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{p2.ID, dep2.ID} {
			if _, err := db.ExecContext(context.Background(), `UPDATE memo SET membership_id=? WHERE id=?`, e.members["b"], id); err != nil {
				t.Fatal(err)
			}
		}
		views := map[string]*memoMemberView{e.members["b"]: {mirrorOK: true}}
		e.runner().collectAnswers(context.Background(), views, now)
		if got := e.delivery(d2.ID); got.AnswerTriedAt != "" || got.AnswerClass != "" {
			t.Fatalf("the stopped workspace's row was stamped or read: %+v", got)
		}
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the running workspace's row behind it: %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}

// An Agent that reports its workspace running but does not answer the session list costs one try:
// the row is stamped, so the next ticks reach the rows behind it.
func TestMemoAnswerUnansweredAgentIsStamped(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.mu.Lock()
		agent.sessionsDown = true
		agent.mu.Unlock()
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerTriedAt == "" {
			t.Fatal("a row of an Agent that did not answer was left unstamped")
		}
	})
}

// With a threshold of two, one message that could fire and one that waits on a read: the read
// wakes the workspace (the two together would open the queue), and nothing fires before it.
func TestMemoAnswerWakeWithAnEligibleMessageBelowTheThreshold(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		e.setPRs(da.ID, store.PRStateNone)
		e.pending("app", "impl", "x, free")
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_queue SET open_after=2 WHERE category_id=?`, q); err != nil {
			t.Fatal(err)
		}
		e.setRuntime(agent, "stopped")
		fired, wakes := e.autoTick(now.Add(5 * time.Minute))
		if len(wakes) != 1 || len(fired) != 0 {
			t.Fatalf("%d wakes and %d fires, want one wake and no fire", len(wakes), len(fired))
		}
	})
}

// A page cut at the reply bound is asked again, at the same place, in a smaller page.
func TestMemoAnswerShrinksAPageCutAtTheBound(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		big := strings.Repeat("x", memoAnswerReplyMax)
		agent.setAnswer("q-a", func() (int, any) {
			n := len(agent.answerCalls) // the fake holds its lock while it answers
			if n == 1 {
				return http.StatusOK, map[string]any{"name": "q-a", "status": "idle", "messages": []map[string]any{
					{"role": "assistant", "parts": []map[string]any{{"kind": "text", "text": "Done: small"}, {"kind": "text", "text": big}}}}}
			}
			return http.StatusOK, map[string]any{"name": "q-a", "status": "idle", "messages": []map[string]any{
				{"role": "assistant", "parts": []map[string]any{{"kind": "text", "text": "Done: small"}}}}}
		})
		e.settleNow(now)
		calls := agent.answerCallsSoFar()
		if len(calls) != 2 || !strings.Contains(calls[0], "limit=200") || !strings.Contains(calls[1], "limit=50") || !strings.Contains(calls[1], "tail=1") {
			t.Fatalf("reads %v, want the tail again in a page of 50", calls)
		}
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}
