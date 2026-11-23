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
		return http.StatusOK, map[string]any{"status": status, "alive": alive, "cursor": 9, "messages": []map[string]any{
			{"role": "user", "text": "the task", "parts": []map[string]any{{"kind": "text", "text": "the task"}}},
			{"role": "assistant", "text": "Looking.\n" + text, "parts": []map[string]any{
				{"kind": "text", "text": "Looking."}, {"kind": "tool", "tool": "Bash"}, {"kind": "text", "text": text}}},
			{"role": "assistant", "sidechain": true, "text": "Done: a subagent's words", "parts": []map[string]any{
				{"kind": "text", "text": "Done: a subagent's words"}}},
		}}
	}
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
// that could not be read: a session the Agent no longer has, or a kind without a transcript.
func TestMemoAnswerNotDoneHolds(t *testing.T) {
	cases := []struct {
		name, head, class string
		reply             func() (int, any)
	}{
		{"partial", "Partial: #1 part 1, next part queued", store.AnswerPartial,
			transcriptReply("idle", true, "Partial: #1 part 1, next part queued\nmore")},
		{"held", "Held: #1 part 2, PR #9 not merged", store.AnswerPartial, transcriptReply("stopped", false, "Held: #1 part 2, PR #9 not merged")},
		{"other", "I stopped at the review limit.", store.AnswerOther, transcriptReply("idle", true, "I stopped at the review limit.")},
		{"no answer", "", store.AnswerUnknown, func() (int, any) {
			return http.StatusOK, map[string]any{"status": "idle", "alive": true, "messages": []any{}}
		}},
		{"session gone", "", store.AnswerUnknown, func() (int, any) {
			return http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": "no such session"}}
		}},
		{"no transcript", "", store.AnswerUnknown, func() (int, any) {
			return http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "unsupported_kind", "message": "x"}}
		}},
	}
	for _, c := range cases {
		eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
			now := time.Now()
			agent, q, da, b := e.noPRChain(now.Add(-time.Minute))
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

// A read that is no answer of the Agent's transcript decides nothing: no route, an error, a cut
// reply, or a session working again (a person took it over). The dependent keeps waiting, the read
// is made again after 5 minutes, and once the window is over the dependent goes to held.
func TestMemoAnswerNoAnswerKeepsWaiting(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		now := time.Now()
		agent, q, da, b := e.noPRChain(now.Add(-time.Minute))
		at := now
		for _, reply := range []func() (int, any){
			nil, // no route: a plain 404
			func() (int, any) {
				return http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "down"}}
			},
			func() (int, any) { return http.StatusOK, map[string]any{"status": "idle"} }, // no messages
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

// A kept answer is never rewritten: a later turn of the session is not what the run ended with.
func TestMemoAnswerWrittenOnce(t *testing.T) {
	eachDeliveryDialect(t, func(t *testing.T, e *deliveryEnv) {
		_, _, da, _ := e.noPRChain(time.Now().Add(-time.Minute))
		ctx := context.Background()
		if ok, err := e.st.RecordAnswer(ctx, da.ID, "Partial: x", store.AnswerPartial); err != nil || !ok {
			t.Fatalf("first: %v %v", ok, err)
		}
		if ok, _ := e.st.RecordAnswer(ctx, da.ID, "Done: y", store.AnswerDone); ok {
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
