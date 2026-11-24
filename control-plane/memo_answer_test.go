package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
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
		ctx, cancel := tickEndsOnFirstCall(t, calls)
		defer cancel()
		e.runner().collectAnswers(ctx, map[string]*memoMemberView{}, now)
		select {
		case path := <-calls:
			if path != "/sessions" {
				t.Fatalf("the silent Agent was first asked %s, want its session list", path)
			}
		default:
			t.Fatal("the silent Agent's session list was never asked")
		}
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

// stoppedMembers adds n members whose workspaces are stopped (and recorded so), each with rows chains
// like otherMemberChains', closed before closedAt (a member's rows next to each other in close
// order), and answers the views a tick has of them (stopped) and their deliveries.
func (e *deliveryEnv) stoppedMembers(q string, n, rows int, closedAt time.Time, prState string) (func() map[string]*memoMemberView, []string) {
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
		ws := store.Workspace{ID: "ws-" + name, TenantID: e.t1.ID, MembershipID: mem.ID, ContainerName: name,
			Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "stopped", CreatedAt: store.NowTS()}
		if err := e.st.CreateWorkspace(ctx, ws); err != nil {
			e.t.Fatal(err)
		}
		e.mgr.mu.Lock()
		e.mgr.rts[mem.ID] = cachedRT{rt: stubRuntime{state: "stopped"}, ws: ws}
		e.mgr.mu.Unlock()
		ids = append(ids, e.otherMemberChains(q, name, rows, closedAt.Add(-time.Duration(i)*time.Minute), prState)...)
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

// More stopped members than the tick has pages, each ahead of a running workspace's row: the pass
// looks up the members recorded as stopped before it lists rows and leaves out those its view
// finds stopped, so the running one is read in the tick. Theirs stay unstamped (read at the start).
func TestMemoAnswerStoppedMembersLookedUp(t *testing.T) {
	saved := memoAnswersListed
	memoAnswersListed = 1
	t.Cleanup(func() { memoAnswersListed = saved })
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: behind the stopped members"))
		views, stopped := e.stoppedMembers(q, memoAnswerListPages+1, 2, now.Add(-time.Minute), store.PRStateNone)
		e.runner().collectAnswers(context.Background(), views(), now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the running workspace's row behind them: %q %q", got.AnswerHead, got.AnswerClass)
		}
		for _, id := range stopped {
			if got := e.delivery(id); got.AnswerTriedAt != "" || got.AnswerClass != "" {
				t.Fatalf("a stopped workspace's row was stamped or read: %+v", got)
			}
		}
	})
}

// A member recorded as stopped whose workspace runs (the record's write was lost) is not left out:
// its row is read in the tick.
func TestMemoAnswerStaleStoppedRecordIsRead(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, _, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: the record was stale"))
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE workspace SET state='stopped' WHERE membership_id=?`, e.members["a"]); err != nil {
			t.Fatal(err)
		}
		e.runner().collectAnswers(context.Background(), map[string]*memoMemberView{}, now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("a running member recorded as stopped: %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}

// queueAgentFor puts a fake Agent behind member's workspace, in runtime state, its workspace
// recorded as running unless it already has one.
func (e *deliveryEnv) queueAgentFor(member, state string) *fakeQueueAgent {
	e.t.Helper()
	a := newFakeQueueAgent(e.t)
	a.mu.Lock()
	a.report = true
	a.mu.Unlock()
	ctx := context.Background()
	ws, ok, err := e.st.GetWorkspaceByMembership(ctx, e.members[member])
	if err != nil {
		e.t.Fatal(err)
	}
	if !ok {
		ws = store.Workspace{ID: "ws-" + member, TenantID: e.t1.ID, MembershipID: e.members[member], ContainerName: member,
			Network: "n", DataDir: "/d", AgentPort: "1", AgentToken: "tok", State: "running", CreatedAt: store.NowTS()}
		if err := e.st.CreateWorkspace(ctx, ws); err != nil {
			e.t.Fatal(err)
		}
	}
	e.setRuntimeOf(member, a, state)
	return a
}

// setRuntimeOf is setRuntime for member's workspace.
func (e *deliveryEnv) setRuntimeOf(member string, a *fakeQueueAgent, state string) {
	ws, _, _ := e.st.GetWorkspaceByMembership(context.Background(), e.members[member])
	e.mgr.mu.Lock()
	e.mgr.rts[e.members[member]] = cachedRT{rt: stubRuntime{endpoint: a.srv.URL, token: "tok", state: state}, ws: ws}
	e.mgr.mu.Unlock()
}

// recordStopped records member's workspace as stopped, whatever its runtime says.
func (e *deliveryEnv) recordStopped(member string) {
	e.t.Helper()
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE workspace SET state='stopped' WHERE membership_id=?`, e.members[member]); err != nil {
		e.t.Fatal(err)
	}
}

// namedReply is transcriptReply about session.
func namedReply(session, text string) func() (int, any) {
	return func() (int, any) {
		_, body := transcriptReply("idle", true, text)()
		body.(map[string]any)["name"] = session
		return http.StatusOK, body
	}
}

// The look-up of the members recorded as stopped asks no Agent: a running member recorded as
// stopped whose Agent does not answer costs the pass nothing before the older rows of another.
func TestMemoAnswerStaleRecordAsksNoAgentFirst(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-2 * time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: older"))
		calls := e.silentMember()
		e.otherMemberChains(q, "c", 1, now.Add(-time.Minute+time.Second), store.PRStateNone) // newer than a's
		e.recordStopped("c")
		ctx, cancel := tickEndsOnFirstCall(t, calls)
		defer cancel()
		e.runner().collectAnswers(ctx, map[string]*memoMemberView{}, now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the older row of the running member: %q %q", got.AnswerHead, got.AnswerClass)
		}
		if len(calls) == 0 {
			t.Fatal("the silent member's row was never reached, so the test proves nothing")
		}
	})
}

// A member left out as stopped at the start of the pass whose workspace starts while the pass reads
// other rows is read in the same tick.
func TestMemoAnswerStartedDuringTheTickIsRead(t *testing.T) {
	fastStartPoll(t)
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		c := e.queueAgentFor("c", "stopped")
		e.recordStopped("c")
		ids := e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), store.PRStateNone) // older than a's
		c.setAnswer("q-c", namedReply("q-c", "Done: started meanwhile"))
		agent.setAnswer("q-a", namedReply("q-a", "Done: a"))
		e.setFlipRuntime("c", c, "running", false)
		e.runner().collectAnswers(context.Background(), map[string]*memoMemberView{}, now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("a: %q %q", got.AnswerHead, got.AnswerClass)
		}
		if got := e.delivery(ids[0]); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the member that started during the tick: %q %q", got.AnswerHead, got.AnswerClass)
		}
	})
}

// A member the PR snapshot pass could not reach is resolved again only after the rows of the
// others: with one read allowed, the newer row of a healthy member is the one read, as on main,
// and the retried member's row stays unstamped for the next tick.
func TestMemoAnswerPRFailedMemberReadAfterTheOthers(t *testing.T) {
	saved := memoAnswersPerTick
	memoAnswersPerTick = 1
	t.Cleanup(func() { memoAnswersPerTick = saved })
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: newer"))
		c := e.queueAgentFor("c", "running")
		c.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
		c.setAnswer("q-c", namedReply("q-c", "Done: older, read again"))
		c.mu.Lock()
		c.sessionsFailNext = 1
		c.mu.Unlock()
		ids := e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), "") // older than a's
		r := e.runner()
		views := map[string]*memoMemberView{}
		r.collectPRSnapshots(context.Background(), views, now)
		if !r.prFailed[e.members["c"]] {
			t.Fatalf("the PR pass did not record c: %v", r.prFailed)
		}
		r.collectAnswers(context.Background(), views, now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("a's row: %q %q", got.AnswerHead, got.AnswerClass)
		}
		if got := e.delivery(ids[0]); got.AnswerClass != "" || got.AnswerTriedAt != "" {
			t.Fatalf("c's row was read or stamped ahead of the others: %+v", got)
		}
	})
}

// flipRuntime answers "stopped" to its first State call (the pass's look-up) and then to every
// later one; with block set the later calls wait for their context instead.
type flipRuntime struct {
	stubRuntime
	calls *atomic.Int32
	then  string
	block bool
	gate  *atomic.Bool  // when set, "stopped" until it is true
	ran   *atomic.Int32 // counts the calls that answered the state after the first
}

func (f flipRuntime) State(ctx context.Context) string {
	if f.calls.Add(1) == 1 {
		return "stopped"
	}
	if f.block {
		<-ctx.Done()
		return ""
	}
	if f.gate != nil && !f.gate.Load() {
		return "stopped"
	}
	f.ran.Add(1)
	return f.then
}

// setFlipRuntime puts a flipRuntime in front of member's Agent.
func (e *deliveryEnv) setFlipRuntime(member string, a *fakeQueueAgent, then string, block bool) {
	e.setFlipRuntimeAt(member, a, then, block, nil)
}

// setFlipRuntimeAt is setFlipRuntime that reports "stopped" until gate is true. It answers the
// counts of State calls so far and of those that answered running.
func (e *deliveryEnv) setFlipRuntimeAt(member string, a *fakeQueueAgent, then string, block bool, gate *atomic.Bool) (calls, ran *atomic.Int32) {
	ws, _, _ := e.st.GetWorkspaceByMembership(context.Background(), e.members[member])
	calls, ran = &atomic.Int32{}, &atomic.Int32{}
	e.mgr.mu.Lock()
	defer e.mgr.mu.Unlock()
	e.mgr.rts[e.members[member]] = cachedRT{rt: flipRuntime{stubRuntime: stubRuntime{endpoint: a.srv.URL, token: "tok", state: "running"}, calls: calls, then: then, block: block, gate: gate, ran: ran}, ws: ws}
	return calls, ran
}

// tickEndsOnFirstCall returns a context cancelled as soon as the silent Agent behind calls has
// been called once, in place of a fixed deadline the pass would sit out: the silent Agent holds
// every call until its caller gives up, so the tick is used up at that moment. The 30 s bound only
// ends a test whose Agent is never asked.
func tickEndsOnFirstCall(t *testing.T, calls <-chan string) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		limit := time.After(30 * time.Second)
		for len(calls) == 0 {
			select {
			case <-stop:
				return
			case <-limit:
				cancel()
				return
			case <-time.After(time.Millisecond):
			}
		}
		cancel()
	}()
	return ctx, cancel
}

// fastStartPoll shortens the pause between two rounds of the poll for started members.
func fastStartPoll(t *testing.T) {
	saved := memoStartPollEvery
	memoStartPollEvery = 2 * time.Millisecond
	t.Cleanup(func() { memoStartPollEvery = saved })
}

// The retry of a member the PR pass could not reach is made after the rows of both lists: the
// healthy member's history-only row is read before the silent member's Agent is asked a second
// time, and the retry is made afterwards.
func TestMemoAnswerPRFailedRetryDoesNotDelayOthers(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		_, q, _, _ := e.noPRChain(now.Add(-time.Hour))
		calls := e.downMember()                                   // 502 at once
		e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), "") // c: a waited-for row
		b := e.queueAgentFor("b", "running")
		bRow := e.otherMemberChains(q, "b", 1, now.Add(-30*time.Second), store.PRStateNone)[0]
		var held string
		if err := e.st.DB().QueryRowContext(context.Background(), `SELECT m.id FROM memo m JOIN memo p ON m.after_ids LIKE '%"' || p.id || '"%' WHERE p.delivery_id=?`, bRow).Scan(&held); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='' WHERE id=?`, held); err != nil {
			t.Fatal(err)
		}
		views := map[string]*memoMemberView{}
		r := e.runner()
		pctx, pcancel := tickEndsOnFirstCall(t, calls)
		r.collectPRSnapshots(pctx, views, now)
		pcancel()
		if !r.prFailed[e.members["c"]] {
			t.Fatalf("the PR pass did not record c: %v", r.prFailed)
		}
		<-calls // the PR pass's own call
		var asked atomic.Int32
		asked.Store(-1)
		b.setAnswer("q-b", func() (int, any) {
			asked.Store(int32(len(calls))) // the silent Agent's calls since the PR pass's
			return namedReply("q-b", "Done: not delayed")()
		})
		actx, acancel := context.WithTimeout(context.Background(), memoAnswerRetryWait+3*time.Second)
		defer acancel()
		r.collectAnswers(actx, views, now)
		if got := e.delivery(bRow); got.AnswerClass != store.AnswerDone {
			t.Fatalf("b's history row: %q", got.AnswerClass)
		}
		if n := asked.Load(); n != 0 {
			t.Fatalf("the silent Agent had been asked %d more times when b's row was read", n)
		}
		if len(calls) == 0 {
			t.Fatal("the silent member was never resolved again")
		}
	})
}

// The check for started members never takes the pass's reads' time: with a State call that blocks
// after the first, every row is still read at once.
func TestMemoAnswerStartCheckDoesNotBlockReads(t *testing.T) {
	savedL := memoAnswersListed
	memoAnswersListed = 1 // a page per row: the old per-page re-check would block each time
	t.Cleanup(func() { memoAnswersListed = savedL })
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setAnswer("q-a", namedReply("q-a", "Done: a"))
		more := e.otherMemberChains(q, "a", 3, now.Add(-time.Minute), store.PRStateNone)
		c := e.queueAgentFor("c", "stopped")
		e.recordStopped("c")
		e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), store.PRStateNone)
		e.setFlipRuntime("c", c, "", true)
		start := time.Now()
		e.runner().collectAnswers(context.Background(), map[string]*memoMemberView{}, now)
		if d := time.Since(start); d > time.Second {
			t.Fatalf("the pass took %v with a blocked state call", d)
		}
		for _, id := range append([]string{da.ID}, more...) {
			if got := e.delivery(id); got.AnswerClass != store.AnswerDone {
				t.Fatalf("a healthy row: %q", got.AnswerClass)
			}
		}
	})
}

// holdDependent makes the delivery's dependent held, so the delivery's row is in the history list
// only (no message waits for it).
func (e *deliveryEnv) holdDependent(delivery string) {
	e.t.Helper()
	var held string
	if err := e.st.DB().QueryRowContext(context.Background(), `SELECT m.id FROM memo m JOIN memo p ON m.after_ids LIKE '%"' || p.id || '"%' WHERE p.delivery_id=?`, delivery).Scan(&held); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='' WHERE id=?`, held); err != nil {
		e.t.Fatal(err)
	}
}

// A started member's rows are merged into the pages by the pages' order: its row older than the
// next one is read, and its newer rows do not push out the older healthy rows main reads first.
// Order: A, C's old row, 19 rows of B, 19 newer rows of C; twenty reads fit.
func TestMemoAnswerStartedRowsMergeByOrder(t *testing.T) {
	fastStartPoll(t)
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-40 * time.Minute))
		agent.setAnswer("q-a", namedReply("q-a", "Done: a"))
		c := e.queueAgentFor("c", "stopped")
		e.recordStopped("c")
		cOld := e.otherMemberChains(q, "c", 1, now.Add(-25*time.Minute), store.PRStateNone)[0]
		bRows := e.otherMemberChains(q, "a", 19, now.Add(-20*time.Minute), store.PRStateNone)
		cNew := e.otherMemberChains(q, "c", 19, now.Add(-10*time.Minute), store.PRStateNone)
		c.setAnswer("q-c", namedReply("q-c", "Done: c"))
		e.setFlipRuntime("c", c, "running", false)
		e.runner().collectAnswers(context.Background(), map[string]*memoMemberView{}, now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("A: %q", got.AnswerClass)
		}
		if got := e.delivery(cOld); got.AnswerClass != store.AnswerDone {
			t.Fatalf("C's old row: %q", got.AnswerClass)
		}
		done := 0
		for _, id := range bRows {
			if e.delivery(id).AnswerClass == store.AnswerDone {
				done++
			}
		}
		if done != 18 {
			t.Fatalf("%d of B's older rows were read, want 18", done)
		}
		for _, id := range cNew {
			if got := e.delivery(id); got.AnswerClass != "" {
				t.Fatalf("a newer row of C pushed out an older healthy row: %+v", got)
			}
		}
	})
}

// The same for the PR snapshot pass.
func TestMemoPRSnapshotStartedRowsMergeByOrder(t *testing.T) {
	fastStartPoll(t)
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.prChain("clean", now.Add(-40*time.Minute))
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer("merged") })
		c := e.queueAgentFor("c", "stopped")
		e.recordStopped("c")
		cOld := e.otherMemberChains(q, "c", 1, now.Add(-25*time.Minute), "")[0]
		bRows := e.otherMemberChains(q, "a", 19, now.Add(-20*time.Minute), "")
		cNew := e.otherMemberChains(q, "c", 19, now.Add(-10*time.Minute), "")
		c.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer("merged") })
		e.setFlipRuntime("c", c, "running", false)
		e.runner().collectPRSnapshots(context.Background(), map[string]*memoMemberView{}, now)
		if got := e.delivery(da.ID); got.PRState != store.PRStateMerged {
			t.Fatalf("A: %+v", got)
		}
		if got := e.delivery(cOld); got.PRState != store.PRStateMerged {
			t.Fatalf("C's old row: %+v", got)
		}
		done := 0
		for _, id := range bRows {
			if e.delivery(id).PRState == store.PRStateMerged {
				done++
			}
		}
		if done != 18 {
			t.Fatalf("%d of B's older rows were read, want 18", done)
		}
		for _, id := range cNew {
			if got := e.delivery(id); got.PRState != "" {
				t.Fatalf("a newer row of C pushed out an older healthy row: %+v", got)
			}
		}
	})
}

// waitUntil polls cond for up to two seconds.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Error("a condition the test waits for never held")
}

// The answer pass's second resolution of a PR-failed member never queues on the workspace build
// lock: with it held, the pass and the retry return in time.
func TestMemoAnswerPRFailedRetryDoesNotQueueOnTheBuildLock(t *testing.T) {
	eachDeliveryDialectRealTime(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
		agent.setAnswer("q-a", namedReply("q-a", "Done: a"))
		calls := e.silentMember()
		silent := e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), "")[0]
		views := map[string]*memoMemberView{}
		r := e.runner()
		pctx, pcancel := tickEndsOnFirstCall(t, calls)
		r.collectPRSnapshots(pctx, views, now)
		pcancel()
		<-calls
		if !r.prFailed[e.members["c"]] {
			t.Fatalf("the PR pass did not record c: %v", r.prFailed)
		}
		id := e.members["c"]
		e.mgr.mu.Lock()
		delete(e.mgr.rts, id)
		e.mgr.mu.Unlock()
		bl := e.mgr.buildLockFor(id)
		bl.Lock()
		defer bl.Unlock()
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.collectAnswers(context.Background(), views, now)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			// Release the lock and let the pass end before failing, so a regression fails here
			// instead of leaving the goroutine behind.
			bl.Unlock()
			<-done
			bl.Lock()
			t.Fatal("the pass queued behind the build lock")
		}
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("a's row: %q", got.AnswerClass)
		}
		_ = silent
	})
}

// A start seen while the history list is read goes before the next history row: the waited-for
// rows come first in the order, however long the history rows take.
func TestMemoAnswerStartedDuringHistoryGoesFirst(t *testing.T) {
	fastStartPoll(t)
	saved := memoAnswersPerTick
	memoAnswersPerTick = 3
	t.Cleanup(func() { memoAnswersPerTick = saved })
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Hour))
		hist := e.otherMemberChains(q, "a", 5, now.Add(-30*time.Minute), store.PRStateNone)
		for _, id := range hist {
			e.holdDependent(id)
		}
		c := e.queueAgentFor("c", "stopped")
		e.recordStopped("c")
		cRow := e.otherMemberChains(q, "c", 1, now.Add(-20*time.Minute), store.PRStateNone)[0]
		c.setAnswer("q-c", namedReply("q-c", "Done: c"))
		var gate atomic.Bool
		var ran *atomic.Int32
		var calls atomic.Int32
		agent.setAnswer("q-a", func() (int, any) {
			if calls.Add(1) == 2 { // the first row of the history list
				gate.Store(true)
				waitUntil(t, func() bool { return ran.Load() > 0 }) // the poll has seen C running
				time.Sleep(30 * time.Millisecond)
			}
			return namedReply("q-a", "Done: a")()
		})
		_, ran = e.setFlipRuntimeAt("c", c, "running", false, &gate)
		e.runner().collectAnswers(context.Background(), map[string]*memoMemberView{}, now)
		if got := e.delivery(cRow); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the waited-for row of the member that started: %q", got.AnswerClass)
		}
		_ = da
	})
}

// A slow state call for one member does not hold up another: B is asked again and seen running
// while C's later look-up is still blocked.
func TestMemoAnswerSlowStateCallDoesNotDelayAnotherStart(t *testing.T) {
	fastStartPoll(t)
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, _, _ := e.noPRChain(now.Add(-time.Hour))
		c := e.queueAgentFor("c", "stopped")
		b := e.queueAgentFor("b", "stopped")
		e.recordStopped("c")
		e.recordStopped("b")
		e.otherMemberChains(q, "c", 1, now.Add(-30*time.Minute), store.PRStateNone) // C is first in the candidate list
		bRow := e.otherMemberChains(q, "b", 1, now.Add(-20*time.Minute), store.PRStateNone)[0]
		b.setAnswer("q-b", namedReply("q-b", "Done: b"))
		e.setFlipRuntime("c", c, "", true) // the later calls wait for their context
		var gate atomic.Bool
		states, ran := e.setFlipRuntimeAt("b", b, "running", false, &gate)
		var once atomic.Bool
		agent.setAnswer("q-a", func() (int, any) {
			if once.CompareAndSwap(false, true) {
				waitUntil(t, func() bool { return states.Load() >= 2 }) // B answered stopped to the poll
				gate.Store(true)                                        // B starts while C's look-up is still blocked
				waitUntil(t, func() bool { return ran.Load() > 0 })
				time.Sleep(30 * time.Millisecond)
			}
			return namedReply("q-a", "Done: a")()
		})
		e.runner().collectAnswers(context.Background(), map[string]*memoMemberView{}, now)
		if got := e.delivery(bRow); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the member that started behind the slow one: %q", got.AnswerClass)
		}
	})
}

// A member that is starting is not read, and its row is not stamped; one that runs but whose
// session list fails is not stamped either, so its row is read as soon as the view can be read.
// Three ticks: stopped -> starting, stopped -> running with the list failing, stopped -> running.
func TestMemoAnswerStartingMemberIsNotStamped(t *testing.T) {
	fastStartPoll(t)
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		// a's row is never answered, so it is due in every tick and keeps the pass busy long enough
		// for the poll to see the member's state.
		agent.setAnswer("q-a", func() (int, any) {
			time.Sleep(100 * time.Millisecond)
			return http.StatusNotFound, map[string]any{}
		})
		c := e.queueAgentFor("c", "stopped")
		e.recordStopped("c")
		cRow := e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), store.PRStateNone)[0]
		c.setAnswer("q-c", namedReply("q-c", "Done: after the start"))
		tick := func(n int, then string) {
			e.setFlipRuntime("c", c, then, false)
			e.runner().collectAnswers(context.Background(), map[string]*memoMemberView{}, now.Add(time.Duration(n)*6*time.Minute))
		}
		tick(1, "starting")
		if got := e.delivery(cRow); got.AnswerClass != "" || got.AnswerTriedAt != "" {
			t.Fatalf("a row of a starting workspace was read or stamped: %+v", got)
		}
		c.mu.Lock()
		c.sessionsFailNext = 1
		c.mu.Unlock()
		tick(2, "running")
		if got := e.delivery(cRow); got.AnswerClass != "" || got.AnswerTriedAt != "" {
			t.Fatalf("a row whose session list failed was stamped: %+v", got)
		}
		tick(3, "running")
		if got := e.delivery(cRow); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the third tick: %q %q", got.AnswerHead, got.AnswerClass)
		}
		_ = da
	})
}

// The state look-up never queues behind a workspace build: with the build lock held it answers
// unknown at once, and no caller is left waiting on the lock.
func TestMemoRuntimeStateDoesNotQueueOnTheBuildLock(t *testing.T) {
	eachDeliveryDialectRealTime(t, func(t *testing.T, e *deliveryEnv) {
		e.noPRChain(time.Now().Add(-time.Minute))
		e.queueAgentFor("c", "stopped")
		id := e.members["c"]
		e.mgr.mu.Lock()
		delete(e.mgr.rts, id)
		e.mgr.mu.Unlock()
		bl := e.mgr.buildLockFor(id)
		bl.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		start := time.Now()
		got := make(chan string, 1)
		go func() { got <- e.runner().memoRuntimeState(ctx, id) }()
		var st string
		select {
		case st = <-got:
		case <-time.After(5 * time.Second):
			// Release the lock and join before failing, so a regression fails here and not at
			// the package timeout.
			bl.Unlock()
			<-got
			t.Fatal("the state look-up queued behind the build lock")
		}
		if d := time.Since(start); st != "" || d > time.Second {
			t.Fatalf("state %q after %v with the build lock held", st, d)
		}
		bl.Unlock()
		if !bl.TryLock() {
			t.Fatal("a caller is still waiting on the build lock")
		}
		bl.Unlock()
	})
}

// The rows are read in the order they are due: a running member's older rows are all read before
// a newer row of a member whose Agent does not answer, which then costs what is left of the pass.
func TestMemoAnswerOlderRowsFirst(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-3 * time.Minute))
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: older"))
		more := e.otherMemberChains(q, "a", 2, now.Add(-2*time.Minute), store.PRStateNone)
		calls := e.silentMember()
		e.otherMemberChains(q, "c", 1, now.Add(-time.Minute+time.Second), store.PRStateNone) // newer than a's
		ctx, cancel := tickEndsOnFirstCall(t, calls)
		defer cancel()
		e.runner().collectAnswers(ctx, map[string]*memoMemberView{}, now)
		for _, id := range append([]string{da.ID}, more...) {
			if got := e.delivery(id); got.AnswerClass != store.AnswerDone {
				t.Fatalf("an older row of the running member: %q %q", got.AnswerHead, got.AnswerClass)
			}
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

// A member whose session list the PR snapshot pass could not read and that still does not answer
// costs the answer pass at most memoAnswerRetryWait: the rows behind it are read, and its row is
// stamped as an Agent's that does not answer is.
func TestMemoAnswerPRFailedMemberRetriedBriefly(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, _ := e.noPRChain(now.Add(-time.Minute))
		agent.setPRs(true, func(memoPRSnapshotRequest) (int, any) { return http.StatusOK, prAnswer() })
		agent.setAnswer("q-a", transcriptReply("idle", true, "Done: not kept waiting"))
		calls := e.silentMember()
		silent := e.otherMemberChains(q, "c", 1, now.Add(-time.Minute), "")[0] // ahead of a in both lists
		views := map[string]*memoMemberView{}
		r := e.runner()
		pctx, pcancel := tickEndsOnFirstCall(t, calls)
		r.collectPRSnapshots(pctx, views, now)
		pcancel()
		if !r.prFailed[e.members["c"]] {
			t.Fatalf("the PR pass did not record the silent member: %v", r.prFailed)
		}
		select {
		case path := <-calls:
			if path != "/sessions" {
				t.Fatalf("the silent Agent was asked %s", path)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the PR pass never asked the silent Agent")
		}
		actx, acancel := context.WithTimeout(context.Background(), memoAnswerRetryWait+2*time.Second)
		defer acancel()
		r.collectAnswers(actx, views, now)
		if got := e.delivery(da.ID); got.AnswerClass != store.AnswerDone {
			t.Fatalf("the member behind the silent one: %q %q", got.AnswerHead, got.AnswerClass)
		}
		if len(calls) == 0 {
			t.Fatal("the silent member was never asked again, so the retry was not reached")
		}
		if got := e.delivery(silent); got.AnswerTriedAt == "" {
			t.Fatal("the silent member's row was not stamped")
		}
	})
}

// A member the PR snapshot pass could not reach (its workspace neither running nor stopped then) is
// resolved again by the answer pass, and both its rows, waited for and history only, are read.
func TestMemoAnswerPRFailedMemberRetriedInBothLists(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		_, q, _, _ := e.noPRChain(now.Add(-time.Hour))
		ids := e.otherMemberChains(q, "c", 2, now.Add(-time.Minute), "")
		var held string
		if err := e.st.DB().QueryRowContext(context.Background(), `SELECT m.id FROM memo m JOIN memo p ON m.after_ids LIKE '%"' || p.id || '"%' WHERE p.delivery_id=?`, ids[1]).Scan(&held); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.DB().ExecContext(context.Background(), `UPDATE memo SET state='held', release_hash='', released_at='' WHERE id=?`, held); err != nil {
			t.Fatal(err)
		}
		r := e.runner()
		views := map[string]*memoMemberView{}
		r.collectPRSnapshots(context.Background(), views, now)
		if !r.prFailed[e.members["c"]] {
			t.Fatalf("the PR pass did not record the member it could not reach: %v", r.prFailed)
		}
		agent := newFakeQueueAgent(t)
		agent.setAnswer("q-c", func() (int, any) {
			_, body := transcriptReply("idle", true, "Done: resolved again")()
			body.(map[string]any)["name"] = "q-c"
			return http.StatusOK, body
		})
		ws, ok, err := e.st.GetWorkspaceByMembership(context.Background(), e.members["c"])
		if err != nil || !ok {
			t.Fatalf("c's workspace: %v %v", ok, err)
		}
		e.mgr.mu.Lock()
		e.mgr.rts[e.members["c"]] = cachedRT{rt: stubRuntime{endpoint: agent.srv.URL, token: "tok", state: "running"}, ws: ws}
		e.mgr.mu.Unlock()
		r.collectAnswers(context.Background(), views, now)
		for _, id := range ids {
			if got := e.delivery(id); got.AnswerClass != store.AnswerDone {
				t.Fatalf("a row of the member resolved again: %q %q", got.AnswerHead, got.AnswerClass)
			}
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
		edge := time.Now().Add(5 * time.Second) // room for the writer to reach the wait on a loaded host
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
			if waiting == 0 {
				time.Sleep(20 * time.Millisecond)
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
	eachDeliveryDialectRealTime(t, func(t *testing.T, e *deliveryEnv) {
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

// gatedRuntime answers "running" once release is closed.
type gatedRuntime struct {
	stubRuntime
	release chan struct{}
}

func (g gatedRuntime) State(ctx context.Context) string {
	select {
	case <-g.release:
		return "running"
	case <-ctx.Done():
		return ""
	}
}

// The poll reports its first round done only after a member seen running is published: a pass
// that waits for the round must find the result when it takes.
func TestMemoStartWatchPublishesBeforeTheFirstRoundIsDone(t *testing.T) {
	eachDeliveryDialectRealTime(t, func(t *testing.T, e *deliveryEnv) {
		e.noPRChain(time.Now().Add(-time.Minute))
		a := e.queueAgentFor("c", "stopped")
		ws, _, _ := e.st.GetWorkspaceByMembership(context.Background(), e.members["c"])
		release := make(chan struct{})
		e.mgr.mu.Lock()
		e.mgr.rts[e.members["c"]] = cachedRT{rt: gatedRuntime{stubRuntime: stubRuntime{endpoint: a.srv.URL, token: "tok"}, release: release}, ws: ws}
		e.mgr.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		w := e.runner().watchStarts(ctx, []string{e.members["c"]})
		w.mu.Lock() // the publish waits here
		close(release)
		select {
		case <-w.first:
			w.mu.Unlock()
			t.Fatal("the first round was reported done before the running member was published")
		case <-time.After(200 * time.Millisecond):
		}
		w.mu.Unlock()
		select {
		case <-w.first:
		case <-time.After(5 * time.Second):
			t.Fatal("the first round never ended")
		}
		if got := w.take(); len(got) != 1 || got[0] != e.members["c"] {
			t.Fatalf("taken %v after the first round", got)
		}
	})
}
