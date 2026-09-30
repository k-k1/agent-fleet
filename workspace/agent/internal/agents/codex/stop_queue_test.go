package codex

// The two-stage stop (ADR 0105) on the codex driver: what a stop does to the queue, checked
// through the mock app-server down to the turns it ends up running.

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

func memberInput(prompt, id string) agents.TurnInput {
	return agents.TurnInput{Prompt: prompt, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginMember}}
}

func peerInput(prompt, id string) agents.TurnInput {
	return agents.TurnInput{Prompt: prompt, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginPeer, From: "other"}}
}

func sentTurns(m *mockCodexServer) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.turns...)
}

// waitPumpDone blocks until the pump has nothing left: no turn running, nothing queued, no
// pump goroutine. The pump writes the status store under HOME, so a test must not return
// before this.
func waitPumpDone(t *testing.T, h *threadHandle) {
	t.Helper()
	deadline := time.Now().Add(waitBackstop)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		busy := h.pumping || h.running || h.tq().Len() > 0 || h.tq().Head() != nil
		h.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the pump never went idle")
}

// startLong starts a turn that runs until the test ends it, and waits until it runs.
func startLong(t *testing.T, m *mockCodexServer, h *threadHandle) {
	t.Helper()
	if err := h.Send(memberInput("long", "af_long")); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 1)
	waitCodexState(t, h, agents.TurnRunning)
}

func episode(h *threadHandle) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tq().Episode()
}

// Decision 1: a first stop ends the running turn only. The member's own follow-up and a peer
// message both continue, one turn each, in the order they were queued.
func TestFirstStopEndsTheTurnAndTheQueueContinues(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-first-stop")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)
	for _, in := range []agents.TurnInput{memberInput("own follow-up", "af_own"), peerInput("from a peer", "af_peer")} {
		if err := h.Send(in); err != nil {
			t.Fatal(err)
		}
	}

	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst || res.Discard != nil {
		t.Fatalf("first stop = %+v, want first with nothing discarded", res)
	}
	waitCodexCalls(t, m, "turn/start", 2)
	m.complete("completed")
	waitCodexCalls(t, m, "turn/start", 3)
	m.complete("completed")
	waitPumpDone(t, h)

	if got := fmt.Sprint(sentTurns(m)); got != "[long own follow-up from a peer]" {
		t.Fatalf("turns = %s, want the queue to continue in order after the stop", got)
	}
	if got := m.callCount("turn/interrupt"); got != 1 {
		t.Fatalf("turn/interrupt count = %d, want 1", got)
	}
	waitCodexState(t, h, agents.TurnCompleted)
	if episode(h) {
		t.Fatal("the stop episode outlived the queue it continued into")
	}
}

// Decision 2: a stop inside the episode stops the continued turn and discards the rest, the
// peer message included, and keeps what it discarded for return.
func TestSecondStopDiscardsTheRestAndKeepsIt(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-second-stop")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)
	for _, in := range []agents.TurnInput{memberInput("own follow-up", "af_own"), peerInput("from a peer", "af_peer"), memberInput("then deploy", "af_deploy")} {
		if err := h.Send(in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 2)
	waitCodexState(t, h, agents.TurnRunning)

	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopSecond || res.Discard == nil {
		t.Fatalf("second stop = %+v, want second with a discard", res)
	}
	var got []string
	for _, it := range res.Discard.Items {
		got = append(got, it.ID+"/"+it.Origin.Kind)
	}
	if fmt.Sprint(got) != "[af_peer/peer af_deploy/member]" {
		t.Fatalf("discarded %v, want the peer message and the member's follow-up", got)
	}
	waitCodexState(t, h, agents.TurnCancelled)
	waitPumpDone(t, h)
	time.Sleep(100 * time.Millisecond)
	if got := fmt.Sprint(sentTurns(m)); got != "[long own follow-up]" {
		t.Fatalf("turns = %s, want nothing started after the second stop", got)
	}
	if got := m.callCount("turn/interrupt"); got != 2 {
		t.Fatalf("turn/interrupt count = %d, want 2", got)
	}
	h.mu.Lock()
	kept := h.tq().Discards()
	h.mu.Unlock()
	if len(kept) != 1 || kept[0].ID != res.Discard.ID || kept[0].Reason != agents.DiscardSecondStop {
		t.Fatalf("kept discards = %+v, want the second stop's", kept)
	}
	if !h.DismissDiscard(kept[0].ID) || h.DismissDiscard(kept[0].ID) {
		t.Fatal("DismissDiscard does not drop the kept discard exactly once")
	}
}

// Decision 3: discard_queue is the brake outside any episode too.
func TestDiscardQueueWorksOutsideAnEpisode(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-discard-queue")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)
	if err := h.Send(memberInput("queued", "af_q")); err != nil {
		t.Fatal(err)
	}
	if episode(h) {
		t.Fatal("an episode is open before any stop")
	}
	res, err := h.Interrupt(agents.InterruptOpts{DiscardQueue: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopDiscard || res.Discard == nil || len(res.Discard.Items) != 1 || res.Discard.Items[0].ID != "af_q" {
		t.Fatalf("discard stop = %+v, want the queued input discarded", res)
	}
	waitCodexState(t, h, agents.TurnCancelled)
	waitPumpDone(t, h)
	time.Sleep(100 * time.Millisecond)
	if got := m.callCount("turn/start"); got != 1 {
		t.Fatalf("turn/start count = %d, want the discarded input never sent", got)
	}
}

// Decision 3: input still uncommitted when the discard takes the lock never starts. Here it
// waits behind a turn a previous Agent left running (taken over by Resume, no pump), which the
// stop ends as it always did.
func TestDiscardBeforeCommitIsNeverSent(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-discard-before-commit")
	registerCodexTestHandle(t, h)
	h.mu.Lock()
	h.running, h.turnID, h.state = true, "turn_external", agents.TurnRunning
	h.mu.Unlock()
	if err := h.Send(memberInput("waits", "af_wait")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Interrupt(agents.InterruptOpts{DiscardQueue: true}); err != nil {
		t.Fatal(err)
	}
	raw, ok := m.lastCall("turn/interrupt")
	var p struct {
		TurnID string `json:"turnId"`
	}
	if !ok || json.Unmarshal(raw, &p) != nil || p.TurnID != "turn_external" {
		t.Fatalf("turn/interrupt = %s, want the taken-over turn stopped", raw)
	}
	dispatchNotification(rpcMsg{Method: "turn/completed", Params: json.RawMessage(
		`{"threadId":"thr_test","turn":{"id":"turn_external","status":"interrupted"}}`)})
	waitCodexState(t, h, agents.TurnCancelled)
	waitPumpDone(t, h)
	time.Sleep(100 * time.Millisecond)
	if got := m.callCount("turn/start"); got != 0 {
		t.Fatalf("turn/start count = %d, want the discarded input never sent", got)
	}
}

// Decision 5: an entry can be removed while it waits; once its turn/start is out it is
// already started.
func TestRemoveQueued(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-remove")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)
	for _, in := range []agents.TurnInput{memberInput("remove me", "af_rm"), memberInput("keep me", "af_keep")} {
		if err := h.Send(in); err != nil {
			t.Fatal(err)
		}
	}
	it, err := h.RemoveQueued("af_rm")
	if err != nil || it.ID != "af_rm" || it.Text != "remove me" {
		t.Fatalf("remove = %+v, %v", it, err)
	}
	if _, err := h.RemoveQueued("af_rm"); !errors.Is(err, agents.ErrNotQueued) {
		t.Fatalf("second remove = %v, want not queued", err)
	}
	m.complete("completed")
	waitCodexCalls(t, m, "turn/start", 2)
	waitCodexState(t, h, agents.TurnRunning)
	if _, err := h.RemoveQueued("af_keep"); !errors.Is(err, agents.ErrAlreadyStarted) {
		t.Fatalf("remove of the started entry = %v, want already started", err)
	}
	m.complete("completed")
	waitPumpDone(t, h)
	if got := fmt.Sprint(sentTurns(m)); got != "[long keep me]" {
		t.Fatalf("turns = %s, want the removed entry never sent", got)
	}
}

// Decision 2: new member input ends the episode, and a peer message does not — although both
// rewrite the displayed state to queued, which is why the episode is not read from it.
func TestEpisodeEndsOnMemberInputOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    agents.TurnInput
		want  agents.StopKind
		turns string
	}{
		{"peer keeps the episode", peerInput("from a peer", "af_peer"), agents.StopSecond, "[long first follow-up]"},
		{"member ends the episode", memberInput("new request", "af_new"), agents.StopFirst, "[long first follow-up new request]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, cl := newMockCodexServer(t)
			h := newCodexTestHandle(t, cl, "codex-episode-"+tc.in.ClientMessageID)
			registerCodexTestHandle(t, h)
			startLong(t, m, h)
			if err := h.Send(memberInput("first follow-up", "af_first")); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
				t.Fatal(err)
			}
			waitCodexCalls(t, m, "turn/start", 2)
			waitCodexState(t, h, agents.TurnRunning)
			queued, err := h.SendQueued(tc.in)
			if err != nil || !queued {
				t.Fatalf("send behind the continued turn: queued=%v err=%v", queued, err)
			}
			h.mu.Lock()
			st := h.state
			h.mu.Unlock()
			if st != agents.TurnQueued {
				t.Fatalf("displayed state = %s, want queued (the overwrite the episode must survive)", st)
			}
			res, err := h.Interrupt(agents.InterruptOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Stop != tc.want {
				t.Fatalf("stop = %s, want %s", res.Stop, tc.want)
			}
			if tc.want == agents.StopFirst {
				waitCodexCalls(t, m, "turn/start", 3)
				m.complete("completed")
			}
			waitPumpDone(t, h)
			time.Sleep(100 * time.Millisecond)
			if got := fmt.Sprint(sentTurns(m)); got != tc.turns {
				t.Fatalf("turns = %s, want %s", got, tc.turns)
			}
		})
	}
}

// A native steer of member input ends the episode as well: it is accepted without the queue.
func TestNativeSteerEndsTheEpisode(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-steer-episode")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)
	if err := h.Send(peerInput("from a peer", "af_peer")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 2)
	waitCodexState(t, h, agents.TurnRunning)
	if !episode(h) {
		t.Fatal("no episode while the continued turn runs")
	}
	if err := h.Steer(memberInput("also this", "af_steer")); err != nil {
		t.Fatal(err)
	}
	if got := m.callCount("turn/steer"); got != 1 {
		t.Fatalf("turn/steer count = %d, want the steer delivered natively", got)
	}
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst {
		t.Fatalf("stop after a member steer = %s, want first", res.Stop)
	}
	waitCodexState(t, h, agents.TurnCancelled)
	waitPumpDone(t, h)
}

// Decision 7: cancelling a question stops the turn through Interrupt, as a first stop, so what
// was queued before the question continues.
func TestQuestionCancelIsAFirstStop(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-question-cancel-queue")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)
	if err := h.Send(memberInput("queued", "af_q")); err != nil {
		t.Fatal(err)
	}
	m.ask()
	waitCodexState(t, h, agents.TurnWaitingInteraction)
	if err := h.Respond(agents.InteractionReply{ID: "item_q1", Decision: agents.DecisionCancel}); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 2)
	m.complete("completed")
	waitPumpDone(t, h)
	if got := fmt.Sprint(sentTurns(m)); got != "[long queued]" {
		t.Fatalf("turns = %s, want the queued input to continue after the cancel", got)
	}
}
