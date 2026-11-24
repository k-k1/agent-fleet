package main

import (
	"context"
	"fmt"
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
		if ok, _ := e.st.RecordAnswer(ctx, da.ID, "Done: late", store.AnswerDone, store.DeliveryTS(time.Now()), memoAnswerWindow); ok {
			t.Fatal("an answer was kept after its window")
		}
		if ok, err := e.st.RecordAnswer(ctx, da.ID, "Partial: x", store.AnswerPartial, since, memoAnswerWindow); err != nil || !ok {
			t.Fatalf("first: %v %v", ok, err)
		}
		if ok, _ := e.st.RecordAnswer(ctx, da.ID, "Done: y", store.AnswerDone, since, memoAnswerWindow); ok {
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

// otherMemberChains makes n chains like prChain's (first turn clean, snapshot prState) and moves
// each to member, with session "q-<member>": their deliveries close before closedAt, the first
// latest, so they are listed ahead of a delivery closed at closedAt. It answers their ids.
func (e *deliveryEnv) otherMemberChains(q, member string, n int, closedAt time.Time, prState string) []string {
	e.t.Helper()
	ctx := context.Background()
	db := e.st.DB()
	var ids []string
	for i := 0; i < n; i++ {
		p := e.pending("app", "impl", fmt.Sprintf("p%d", i))
		rec := e.do("a", "POST", "/api/memos", afterMemo("app", "impl", fmt.Sprintf("waits for p%d", i), p.ID))
		if rec.Code != http.StatusCreated {
			e.t.Fatalf("dependent: %d %s", rec.Code, rec.Body.String())
		}
		dep := decode[memoDTO](e.t, rec)
		d := e.fired(q)
		if !sameIDs(d.MessageIDs, p.ID) {
			e.t.Fatalf("claimed %v, want p%d", d.MessageIDs, i)
		}
		e.closeWithPRs(d.ID, "clean", 90_000, closedAt.Add(-time.Duration(i+1)*time.Second), prState)
		if _, err := db.ExecContext(ctx, `UPDATE memo_delivery SET session=?, membership_id=? WHERE id=?`,
			"q-"+member, e.members[member], d.ID); err != nil {
			e.t.Fatal(err)
		}
		for _, id := range []string{p.ID, dep.ID} {
			if _, err := db.ExecContext(ctx, `UPDATE memo SET membership_id=? WHERE id=?`, e.members[member], id); err != nil {
				e.t.Fatal(err)
			}
		}
		ids = append(ids, d.ID)
	}
	return ids
}

// collectAnswers pages its waited-for list past the rows of a stopped workspace, however many: they
// stay unstamped (read at the start), the member is left out of the next pages, and a running
// workspace's row behind more of them than the tick has pages is read in the tick (issue #4891).
func TestMemoAnswerListPages(t *testing.T) {
	saved := memoAnswersListed
	memoAnswersListed = 1
	t.Cleanup(func() { memoAnswersListed = saved })
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: behind the stopped rows"))
		stopped := e.otherMemberChains(q, "b", memoAnswerListPages+1, now.Add(-time.Minute), store.PRStateNone)
		views := map[string]*memoMemberView{e.members["b"]: {mirrorOK: true}}
		e.runner().collectAnswers(context.Background(), views, now)
		for _, id := range stopped {
			if got := e.delivery(id); got.AnswerTriedAt != "" || got.AnswerClass != "" {
				t.Fatalf("the stopped workspace's row was stamped or read: %+v", got)
			}
		}
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the running workspace's row behind them: %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}

// An Agent that reports its workspace running but does not answer the session list costs one try:
// the row is stamped, so the next ticks reach the rows behind it. The first member's list call is
// held until the pass's budget ends (the pass is run under a short budget of its own), the member
// behind is not read in that tick, and the next tick reads its Done:.
func TestMemoAnswerUnansweredAgentIsStamped(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: the member behind"))
		calls := e.silentMember() // c: its Agent holds every call until the caller gives up
		silent := e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), store.PRStateNone)[0]
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		done := make(chan struct{})
		go func() {
			defer close(done)
			e.runner().collectAnswers(ctx, map[string]*memoMemberView{}, now)
		}()
		select {
		case path := <-calls:
			if path != "/sessions" {
				t.Fatalf("the silent Agent was first asked %s, want its session list", path)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the silent Agent's session list was never asked")
		}
		<-done
		cancel()
		if got := e.delivery(silent); got.AnswerTriedAt == "" {
			t.Fatal("a row of an Agent that did not answer was left unstamped")
		}
		if got := e.delivery(da.ID); got.AnswerClass != "" || len(agent.answerCallsSoFar()) != 0 {
			t.Fatalf("the member behind was read in a tick the silent Agent used up: %+v", got)
		}
		actx, acancel := context.WithTimeout(context.Background(), memoAnswerBudget)
		defer acancel()
		e.runner().collectAnswers(actx, map[string]*memoMemberView{}, now.Add(time.Minute))
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone || got.AnswerHead != "Done: the member behind" {
			t.Fatalf("the next tick: %q %q", got.AnswerHead, got.AnswerClass)
		}
		select {
		case path := <-calls:
			t.Fatalf("the stamped row's Agent was asked again (%s) within memoAnswerEvery", path)
		default:
		}
	})
}

// stoppedMembers adds n members, each with a chain like otherMemberChains', closed before
// closedAt, and answers the views a tick has of them (their workspaces stopped) and their
// deliveries.
func (e *deliveryEnv) stoppedMembers(q string, n int, closedAt time.Time, prState string) (func() map[string]*memoMemberView, []string) {
	e.t.Helper()
	ctx := context.Background()
	var members, ids []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("stopped%d", i)
		email := name + "@example.com"
		id, err := e.st.UpsertIdentity(ctx, email, sanitizeUser(email), "")
		if err != nil {
			e.t.Fatal(err)
		}
		mem, err := e.st.EnsureMembership(ctx, id.ID, e.t1.ID, "member")
		if err != nil {
			e.t.Fatal(err)
		}
		e.members[name] = mem.ID
		members = append(members, mem.ID)
		ids = append(ids, e.otherMemberChains(q, name, 1, closedAt.Add(-time.Duration(i)*time.Minute), prState)...)
	}
	views := func() map[string]*memoMemberView {
		v := map[string]*memoMemberView{}
		for _, m := range members {
			v[m] = &memoMemberView{mirrorOK: true}
		}
		return v
	}
	return views, ids
}

// More stopped members than the tick has pages, each ahead of a running workspace's row: the tick
// reaches them alone, and the next tick leaves out from its first page those it still finds
// stopped, so the running one is read. Theirs stay unstamped (read at the start).
func TestMemoAnswerStoppedMembersCarried(t *testing.T) {
	saved := memoAnswersListed
	memoAnswersListed = 1
	t.Cleanup(func() { memoAnswersListed = saved })
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: behind the stopped members"))
		views, stopped := e.stoppedMembers(q, memoAnswerListPages+1, now.Add(-time.Minute), store.PRStateNone)
		r := e.runner()
		r.collectAnswers(context.Background(), views(), now)
		if got := e.delivery(da.ID); got.AnswerClass != "" {
			t.Fatalf("read in the first tick, so the test does not need the carry: %+v", got)
		}
		r.collectAnswers(context.Background(), views(), now.Add(time.Minute))
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the running workspace's row in the next tick: %q %q", got.AnswerHead, got.AnswerClass)
		}
		for _, id := range stopped {
			if got := e.delivery(id); got.AnswerTriedAt != "" || got.AnswerClass != "" {
				t.Fatalf("a stopped workspace's row was stamped or read: %+v", got)
			}
		}
	})
}

// A member left out as stopped on the last tick whose workspace runs now is not left out: its row
// is read in the tick, as it was before the carry.
func TestMemoAnswerCarriedMemberThatStartedIsRead(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: right after the start"))
		r := e.runner()
		r.collectAnswers(context.Background(), map[string]*memoMemberView{e.members["a"]: {mirrorOK: true}}, now)
		if got := e.delivery(da.ID); got.AnswerTriedAt != "" || len(r.answerSkipped) != 1 {
			t.Fatalf("stopped: stamped %q, carried %v", got.AnswerTriedAt, r.answerSkipped)
		}
		r.collectAnswers(context.Background(), map[string]*memoMemberView{}, now.Add(time.Minute))
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("not read in the tick after the start: %q %q", got.AnswerHead, got.AnswerClass)
		}
		if len(r.answerSkipped) != 0 {
			t.Fatalf("still carried %v", r.answerSkipped)
		}
	})
}

// A member whose session list the PR snapshot pass could not read is not kept as unreachable: the
// answer pass after it, in the same tick, resolves it again and reads its answer.
func TestMemoAnswerNotStarvedByThePRPass(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.prChain("clean", now.Add(-time.Minute))
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: read in the same tick"))
		agent.mu.Lock()
		agent.sessionsFailNext = 1
		agent.mu.Unlock()
		views := map[string]*memoMemberView{}
		r := e.runner()
		r.collectPRSnapshots(context.Background(), views, now)
		if got := e.delivery(da.ID); got.PRState != "" || got.PRTriedAt != "" {
			t.Fatalf("the PR pass read without a session list: %+v", got)
		}
		agent.mu.Lock()
		left := agent.sessionsFailNext
		agent.mu.Unlock()
		if left != 0 {
			t.Fatal("the PR pass never asked for the session list")
		}
		r.collectAnswers(context.Background(), views, now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the answer pass after it: %q %q (tried %q)", got.AnswerHead, got.AnswerClass, got.AnswerTriedAt)
		}
	})
}

// On Postgres, an answer whose RecordAnswer waits on a row only locked (FOR UPDATE, released with no
// change, so the UPDATE would not re-check its WHERE) past the window's edge is not kept.
func TestMemoAnswerWindowAfterALockWait(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		if !strings.HasSuffix(t.Name(), "/postgres") {
			t.Skip("SQLite takes its write lock before it reads the clock")
		}
		_, _, da, _ := e.noPRChain(time.Now().Add(-time.Minute))
		ctx := context.Background()
		db := e.st.DB()
		edge := time.Now().Add(1500 * time.Millisecond)
		if _, err := db.ExecContext(ctx, `UPDATE memo_delivery SET closed_at=? WHERE id=?`,
			store.DeliveryTS(edge.Add(-memoAnswerWindow)), da.ID); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var one, holder int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memo_delivery WHERE id=? FOR UPDATE`, da.ID).Scan(&one); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holder); err != nil {
			t.Fatal(err)
		}
		type result struct {
			ok  bool
			err error
		}
		res := make(chan result, 1)
		go func() {
			ok, err := e.st.RecordAnswer(ctx, da.ID, "Done: after the wait", store.AnswerDone, "", memoAnswerWindow)
			res <- result{ok, err}
		}()
		for waiting := 0; waiting == 0; {
			if time.Now().After(edge) {
				t.Fatal("RecordAnswer never waited on the lock before the edge")
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))`,
				holder).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Until(edge) + 300*time.Millisecond)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		r := <-res
		if r.err != nil || r.ok {
			t.Fatalf("kept after a lock wait past the edge: %v %v", r.ok, r.err)
		}
		if got := e.delivery(da.ID); got.AnswerClass != "" {
			t.Fatalf("kept %q", got.AnswerClass)
		}
	})
}

// RecordAnswer judges the window by the database's clock as well as by the caller's since: a since
// that says the close is inside the window does not keep an answer the database's clock puts past
// it (an UPDATE that waited on a lock past the edge), and an answer just inside it is kept.
func TestMemoAnswerWindowByTheDatabaseClock(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, _, da, _ := e.noPRChain(time.Now().Add(-time.Minute))
		ctx := context.Background()
		closeAt := func(at time.Time) {
			if _, err := e.st.DB().ExecContext(ctx, `UPDATE memo_delivery SET closed_at=? WHERE id=?`, store.DeliveryTS(at), da.ID); err != nil {
				t.Fatal(err)
			}
		}
		closeAt(time.Now().Add(-memoAnswerWindow - 2*time.Second))
		if ok, err := e.st.RecordAnswer(ctx, da.ID, "Done: late", store.AnswerDone, "", memoAnswerWindow); err != nil || ok {
			t.Fatalf("kept past the database's window: %v %v", ok, err)
		}
		closeAt(time.Now().Add(-memoAnswerWindow + 5*time.Second))
		if ok, err := e.st.RecordAnswer(ctx, da.ID, "Done: in time", store.AnswerDone, "", memoAnswerWindow); err != nil || !ok {
			t.Fatalf("not kept inside the window: %v %v", ok, err)
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

// pinnedReply is a transcript whose run ended with run's answer before pin, and a person's turn
// after it that ended with theirs (issue #4891).
func pinnedReply(pin time.Time, run, theirs string) func() (int, any) {
	at := func(d time.Duration) string { return pin.Add(d).UTC().Format(time.RFC3339Nano) }
	return func() (int, any) {
		return http.StatusOK, map[string]any{"name": "q-a", "status": "idle", "alive": true, "messages": []map[string]any{
			{"role": "user", "ts": at(-time.Hour), "parts": []map[string]any{{"kind": "text", "text": "the task"}}},
			{"role": "assistant", "ts": at(-time.Second), "parts": []map[string]any{{"kind": "tool", "tool": "Bash"}, {"kind": "text", "text": run}}},
			{"role": "user", "ts": at(3 * time.Minute), "parts": []map[string]any{{"kind": "text", "text": "something else"}}},
			{"role": "assistant", "ts": at(4 * time.Minute), "parts": []map[string]any{{"kind": "text", "text": theirs}}},
		}}
	}
}

// setCloseTurnEnd pins a closed delivery's answer read to at, as kept by the read the close was
// based on (gen 7); heldGen is the read the close was actually based on.
func (e *deliveryEnv) setCloseTurnEnd(id string, at string, heldGen int64) {
	e.t.Helper()
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo_delivery SET close_turn_end_at=?, close_turn_gen=7, held_gen=? WHERE id=?`,
		at, heldGen, id); err != nil {
		e.t.Fatal(err)
	}
}

// agentSecond is a turn end as the Agent stamps it: to the second, truncated.
func agentSecond(t time.Time) string { return t.Truncate(time.Second).Format(time.RFC3339) }

// The read is pinned to the turn end the close saw: a person's turn after it is not the run's,
// whether the list read before it showed no later turn (a resume inside one tick) or showed no
// row at all (resumed, then archived). Without a pin the end of the transcript is read as before.
func TestMemoAnswerPinnedToTheCloseTurn(t *testing.T) {
	for _, c := range []struct {
		name   string
		listed bool
		pinned bool
		class  string
		head   string
	}{
		{"resumed inside one tick", true, true, store.AnswerDone, "Done: the run"},
		{"resumed, then archived", false, true, store.AnswerDone, "Done: the run"},
		{"no pin", false, false, store.AnswerPartial, "Partial: the person's"},
	} {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			now := time.Now()
			pin := now.Add(-10 * time.Minute)
			agent, _, da, _ := e.noPRChain(pin.Add(2 * time.Minute))
			if c.pinned {
				e.setCloseTurnEnd(da.ID, agentSecond(pin), 7)
			}
			if c.listed {
				// The list was read before the person's turn began: it shows the run's own.
				agent.addSession("q-a", map[string]any{"alive": true, "state": "idle",
					"lastTurnStartAt": pin.Add(-time.Hour).UTC().Format(time.RFC3339Nano)})
			}
			agent.setAnswer("q-a", pinnedReply(pin, "Done: the run", "Partial: the person's"))
			e.settleNow(now)
			if got := e.delivery(da.ID); got.AnswerClass != c.class || got.AnswerHead != c.head {
				t.Fatalf("%s: kept %q %q", c.name, got.AnswerHead, got.AnswerClass)
			}
		})
	}
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// A turn that started after the pinned end but before the close (by this Control Plane's
		// clock) is a later turn: the list is compared on the Agent's clock.
		now := time.Now()
		pin := now.Add(-10 * time.Minute)
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(pin), 7)
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle",
			"lastTurnStartAt": pin.Add(time.Minute).UTC().Format(time.RFC3339Nano)})
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: x"))
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerUnknown {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// Every turn of the tail page is the person's: the run's answer is read from the page before.
		now := time.Now()
		pin := now.Add(-10 * time.Minute)
		agent, _, da, _ := e.noPRChain(pin.Add(2 * time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(pin), 7)
		_, full := pinnedReply(pin, "Done: the run", "Partial: the person's")()
		msgs := full.(map[string]any)["messages"].([]map[string]any)
		calls := 0
		agent.setAnswer("q-a", func() (int, any) {
			calls++
			page := map[string]any{"name": "q-a", "status": "idle", "messages": msgs[2:], "firstLine": 200, "hasMore": true}
			if calls > 1 {
				page = map[string]any{"name": "q-a", "status": "idle", "messages": msgs[:2], "firstLine": 0, "hasMore": false}
			}
			return http.StatusOK, page
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone || calls != 2 {
			t.Fatalf("kept %q %q in %d reads", got.AnswerHead, got.AnswerClass, calls)
		}
	})
}

// The close keeps the root session's turn end its read saw, and nothing moves it after the close.
func TestMemoAnswerCloseKeepsTheTurnEnd(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		first := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		end := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
		agent.setRow(d.Session, map[string]any{"state": "working", "lastTurnEndAt": first})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.State != store.DeliveryOpen || got.CloseTurnEndAt != first {
			t.Fatalf("open: %+v", got)
		}
		agent.setRow(d.Session, map[string]any{"state": "idle", "lastTurnEndAt": end})
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.CloseTurnEndAt != end || got.CloseTurnGen != got.HeldGen {
			t.Fatalf("closed: state %s, turn end %q, want %q", got.State, got.CloseTurnEndAt, end)
		}
		agent.setRow(d.Session, map[string]any{"state": "idle", "lastTurnEndAt": time.Now().UTC().Format(time.RFC3339Nano)})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.CloseTurnEndAt != end {
			t.Fatalf("moved after the close: %q", got.CloseTurnEndAt)
		}
	})
}

// The pin's edges (review of #5030): a turn end stamped to the second covers the run's turn that
// started and ended inside that second; a person's turn a second or two after it is cut; a page
// that starts inside the person's turn is not read as the run; a transcript that holds the
// person's turns only, read to its first line, no longer holds the run's answer; and a pin that
// is not the read the close was based on is not used.
func TestMemoAnswerPinEdges(t *testing.T) {
	msg := func(role, ts, text string) map[string]any {
		return map[string]any{"role": role, "ts": ts, "parts": []map[string]any{{"kind": "text", "text": text}}}
	}
	at := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	idle := func(firstLine int, msgs ...map[string]any) map[string]any {
		return map[string]any{"name": "q-a", "status": "idle", "messages": msgs, "firstLine": firstLine, "hasMore": firstLine > 0}
	}
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// Started at .2 and ended at .8 of one second: the end reads as that second.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second).Add(800 * time.Millisecond)
		agent, _, da, _ := e.noPRChain(end.Add(2 * time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle", "lastTurnStartAt": at(end.Add(-600 * time.Millisecond))})
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusOK, idle(0, msg("user", at(end.Add(-600*time.Millisecond)), "the task"), msg("assistant", at(end.Add(-100*time.Millisecond)), "Done: in one second"))
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("same second: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// A person's resume two seconds after the end is a later turn.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Second))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusOK, idle(0, msg("user", at(end.Add(-time.Hour)), "the task"), msg("assistant", at(end.Add(-time.Second)), "Partial: the run"),
				msg("user", at(end.Add(2*time.Second)), "go on"), msg("assistant", at(end.Add(time.Minute)), "Done: the person's"))
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerPartial {
			t.Fatalf("quick resume: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// The tail page starts inside the person's turn: its user turn is on the page before.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		calls := 0
		agent.setAnswer("q-a", func() (int, any) {
			calls++
			if calls == 1 {
				return http.StatusOK, idle(200, msg("assistant", at(end.Add(5*time.Minute)), "Done: other work"))
			}
			return http.StatusOK, idle(0, msg("user", at(end.Add(-time.Hour)), "the task"), msg("assistant", at(end.Add(-time.Second)), "Partial: the run"),
				msg("user", at(end.Add(4*time.Minute)), "other work"))
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerPartial || calls != 2 {
			t.Fatalf("page inside a later turn: kept %q %q in %d reads", got.AnswerHead, got.AnswerClass, calls)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// The whole transcript is the person's: the run's answer is no longer there.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusOK, idle(0, msg("user", at(end.Add(4*time.Minute)), "other work"), msg("assistant", at(end.Add(5*time.Minute)), "Done: other work"))
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerUnknown {
			t.Fatalf("only later turns: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// An older Control Plane kept a later read (held_gen 8) and closed on it, leaving the pin of
		// read 7, the end of the run's first turn. The run's second turn is read, as without a pin.
		now := time.Now()
		first := now.Add(-30 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(first), 8)
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle", "lastTurnStartAt": at(first.Add(10 * time.Minute))})
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusOK, idle(0, msg("user", at(first.Add(-time.Hour)), "the task"), msg("assistant", at(first.Add(-time.Second)), "Partial: first turn"),
				msg("user", at(first.Add(10*time.Minute)), "a peer's go on"), msg("assistant", at(first.Add(15*time.Minute)), "Done: second turn"))
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone || got.AnswerHead != "Done: second turn" {
			t.Fatalf("pin of another read: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}

// A close from the mirror keeps the pin of the last kept report; a close made without a report
// forgets it.
func TestMemoAnswerPinOnEveryClose(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		end := agentSecond(time.Now().Add(-time.Minute))
		agent.setRow(d.Session, map[string]any{"state": "idle", "lastTurnEndAt": end})
		e.settleNow(time.Now())
		e.stopWorkspace(agent, d.Session, "")
		e.settleNow(time.Now())
		e.settleNow(time.Now())
		got := e.delivery(d.ID)
		if got.State != store.DeliveryClosed || got.CloseTurnEndAt != end || got.CloseTurnGen != got.HeldGen {
			t.Fatalf("mirror close: state %s, pin %q gen %d held %d", got.State, got.CloseTurnEndAt, got.CloseTurnGen, got.HeldGen)
		}
		if _, ok := memoAnswerPin(got); !ok {
			t.Fatal("a mirror close lost its pin")
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		agent, d := e.openReported(nil)
		agent.setRow(d.Session, map[string]any{"state": "idle", "lastTurnEndAt": agentSecond(time.Now().Add(-time.Minute))})
		e.settleNow(time.Now())
		if got := e.delivery(d.ID); got.CloseTurnGen == 0 {
			t.Fatalf("no pin kept: %+v", got)
		}
		e.runner().closeOpen(context.Background(), e.delivery(d.ID), store.DeliveryClean)
		got := e.delivery(d.ID)
		if _, ok := memoAnswerPin(got); got.State != store.DeliveryClosed || ok || got.CloseTurnEndAt != "" {
			t.Fatalf("close without a report: %+v", got)
		}
	})
}

// Round 2 of the review of #5030: a kiro transcript (a user turn stamped to the second, assistant
// turns without a time), a codex error turn stamped when read, and a person's end from the run
// history.
func TestMemoAnswerPinKindsAndEnd(t *testing.T) {
	user := func(ts, text string) map[string]any {
		return map[string]any{"role": "user", "ts": ts, "parts": []map[string]any{{"kind": "text", "text": text}}}
	}
	said := func(text string) map[string]any { // kiro: no time
		return map[string]any{"role": "assistant", "parts": []map[string]any{{"kind": "text", "text": text}}}
	}
	sec := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	idle := func(firstLine int, msgs ...map[string]any) map[string]any {
		return map[string]any{"name": "q-a", "status": "idle", "messages": msgs, "firstLine": firstLine, "hasMore": firstLine > 0}
	}
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// The run ended in 10:00:00; a person resumed at 10:00:01.4, stamped 10:00:01.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Second))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle", "lastTurnStartAt": sec(end.Add(-time.Hour))})
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusOK, idle(0, user(sec(end.Add(-time.Hour)), "the task"), said("Partial: the run"),
				user(sec(end.Add(time.Second)), "go on"), said("Done: the person's"))
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerPartial {
			t.Fatalf("resume in the next second: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// The list read before the resume: the next second's start is a later turn there as well.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		agent.addSession("q-a", map[string]any{"alive": true, "state": "idle", "lastTurnStartAt": end.Add(time.Second).UTC().Format(time.RFC3339Nano)})
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: x"))
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerUnknown {
			t.Fatalf("list at the next second: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// A kiro tail page that starts with the person's answer: their input is on the page before.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		calls := 0
		agent.setAnswer("q-a", func() (int, any) {
			calls++
			if calls == 1 {
				return http.StatusOK, idle(200, said("Done: other work"), user(sec(end.Add(5*time.Minute)), "more"), said("Done: more"))
			}
			return http.StatusOK, idle(0, user(sec(end.Add(-time.Hour)), "the task"), said("Partial: the run"), user(sec(end.Add(4*time.Minute)), "other work"))
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerPartial || calls != 2 {
			t.Fatalf("kiro page inside a later turn: kept %q %q in %d reads", got.AnswerHead, got.AnswerClass, calls)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// The run's second turn was refused at turn/start: codex shows the error turn stamped when
		// it is read. It is still the run's last answer, and none is read from it.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusOK, idle(0, user(sec(end.Add(-time.Hour)), "the task"), said("Done: first turn"),
				map[string]any{"role": "assistant", "ts": sec(now), "parts": []map[string]any{{"kind": "error", "text": "refused"}}})
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerUnknown {
			t.Fatalf("codex error stamped when read: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// A person ends the run from the history after a turn the last read did not see: the pin of
		// that read is not used.
		agent, d := e.openReported(nil)
		first := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
		agent.setRow(d.Session, map[string]any{"state": "idle", "lastTurnEndAt": agentSecond(first)})
		e.settleNow(time.Now())
		rec := e.do("a", "POST", "/api/memo-queues/"+d.QueueID+"/deliveries/"+d.ID+"/end", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("end: %d %s", rec.Code, rec.Body.String())
		}
		got := e.delivery(d.ID)
		if _, ok := memoAnswerPin(got); got.Outcome != store.DeliveryEnded || got.CloseTurnGen != got.HeldGen || ok {
			t.Fatalf("ended: outcome %s, pin gen %d held %d, pinned %v", got.Outcome, got.CloseTurnGen, got.HeldGen, ok)
		}
	})
}

// Round 3 of the review of #5030: a run that called tools for more lines than the pages read has
// its answer read from the tail page, and a kind whose turn end is read off a poll is not pinned.
func TestMemoAnswerPinLongRunAndPolledKinds(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		calls := 0
		agent.setAnswer("q-a", func() (int, any) {
			calls++
			return http.StatusOK, map[string]any{"name": "q-a", "status": "idle", "firstLine": 5000, "hasMore": true, "messages": []map[string]any{
				{"role": "assistant", "ts": end.Add(-time.Minute).UTC().Format(time.RFC3339Nano), "parts": []map[string]any{{"kind": "tool", "tool": "Bash"}}},
				{"role": "assistant", "ts": end.Add(-300 * time.Millisecond).UTC().Format(time.RFC3339Nano), "parts": []map[string]any{{"kind": "text", "text": "Done: long run"}}},
			}}
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone || calls != 1 {
			t.Fatalf("long run: kept %q %q in %d reads", got.AnswerHead, got.AnswerClass, calls)
		}
	})
	for _, kind := range []string{"kiro", "agy", "copilot", "cursor"} {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			agent, d := e.openReported(nil)
			agent.setRow(d.Session, map[string]any{"kind": kind, "state": "idle", "lastTurnEndAt": agentSecond(time.Now().Add(-time.Minute))})
			e.settleNow(time.Now())
			e.settleNow(time.Now())
			got := e.delivery(d.ID)
			if _, ok := memoAnswerPin(got); got.State != store.DeliveryClosed || ok || got.CloseTurnEndAt != "" {
				t.Fatalf("%s: state %s, pin %q", kind, got.State, got.CloseTurnEndAt)
			}
		})
	}
}

// Round 4 of the review of #5030: opencode drops an input of attachments alone from its turns, so
// a person's later turn shows no user turn; its answer, stamped after the pin, is not the run's.
func TestMemoAnswerPinLaterTurnWithoutInput(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		ts := func(d time.Duration) string { return end.Add(d).UTC().Format(time.RFC3339Nano) }
		agent.setAnswer("q-a", func() (int, any) { // archived: no row in the list
			return http.StatusOK, map[string]any{"name": "q-a", "status": "stopped", "firstLine": 0, "hasMore": false, "messages": []map[string]any{
				{"role": "user", "ts": ts(-time.Hour), "parts": []map[string]any{{"kind": "text", "text": "the task"}}},
				{"role": "assistant", "ts": ts(-time.Second), "parts": []map[string]any{{"kind": "text", "text": "Partial: the run"}}},
				{"role": "assistant", "ts": ts(4 * time.Minute), "parts": []map[string]any{{"kind": "text", "text": "Done: the attachment's"}}},
			}}
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerPartial {
			t.Fatalf("kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		// The later turn failed before it reached the model: opencode stores the error with its own
		// time and id. It is not the run's last answer either.
		now := time.Now()
		end := now.Add(-10 * time.Minute).Truncate(time.Second)
		agent, _, da, _ := e.noPRChain(end.Add(time.Minute))
		e.setCloseTurnEnd(da.ID, agentSecond(end), 7)
		ts := func(d time.Duration) string { return end.Add(d).UTC().Format(time.RFC3339Nano) }
		agent.setAnswer("q-a", func() (int, any) {
			return http.StatusOK, map[string]any{"name": "q-a", "status": "stopped", "firstLine": 0, "hasMore": false, "messages": []map[string]any{
				{"role": "user", "ts": ts(-time.Hour), "anchorId": "msg_1", "parts": []map[string]any{{"kind": "text", "text": "the task"}}},
				{"role": "assistant", "ts": ts(-time.Second), "anchorId": "msg_2", "parts": []map[string]any{{"kind": "text", "text": "Done: the run"}}},
				{"role": "assistant", "ts": ts(4 * time.Minute), "anchorId": "msg_4", "parts": []map[string]any{{"kind": "error", "text": "provider failed"}}},
			}}
		})
		e.settleNow(now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("later stored error: kept %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}
