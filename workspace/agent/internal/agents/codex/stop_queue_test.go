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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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

// completeTurn ends the nth turn the mock has started. Waiting for the nth turn/start call is
// not enough: the call is recorded as it arrives, before the mock names the turn, and a
// complete() sent in between ends the previous turn again (or nothing) and the pump waits for
// good (measured: 7 of 10 runs under -race). So it waits until the handle itself holds the turn
// the answer named.
func completeTurn(t *testing.T, m *mockCodexServer, h *threadHandle, n int) {
	t.Helper()
	want := "turn_" + string(rune('0'+n)) // the mock's id scheme
	deadline := time.Now().Add(waitBackstop)
	for {
		h.mu.Lock()
		named := h.turnID == want
		h.mu.Unlock()
		if named {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the handle never took turn %s", want)
		}
		time.Sleep(time.Millisecond)
	}
	m.complete("completed")
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
	completeTurn(t, m, h, 2)
	completeTurn(t, m, h, 3)
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
				completeTurn(t, m, h, 3)
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
	completeTurn(t, m, h, 2)
	waitPumpDone(t, h)
	if got := fmt.Sprint(sentTurns(m)); got != "[long queued]" {
		t.Fatalf("turns = %s, want the queued input to continue after the cancel", got)
	}
}

// Input accepted into an idle thread that the pump has not taken yet is the turn being started
// (decision 1): a first stop cancels it, and since it never reached the runtime it is kept for
// return as a first_stop discard.
func TestFirstStopCancelsInputNotYetTaken(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-not-taken")
	registerCodexTestHandle(t, h)
	h.mu.Lock()
	h.pumping = true // the pump goroutine accept starts has not run yet
	h.mu.Unlock()
	if err := h.Send(memberInput("about to start", "af_soon")); err != nil {
		t.Fatal(err)
	}
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst || res.Discard == nil || res.Discard.Reason != agents.DiscardFirstStop ||
		len(res.Discard.Items) != 1 || res.Discard.Items[0].ID != "af_soon" {
		t.Fatalf("stop = %+v, want a first stop keeping the unsent input", res)
	}
	h.mu.Lock()
	left, kept, st := h.tq().Len(), h.tq().Discards(), h.state
	h.pumping = false
	h.mu.Unlock()
	if left != 0 || len(kept) != 1 || kept[0].ID != res.Discard.ID || st != agents.TurnCancelled {
		t.Fatalf("after the stop: %d queued, %d kept, state %s; want the input cancelled and kept", left, len(kept), st)
	}
	h.pump()
	if got := m.callCount("turn/start"); got != 0 {
		t.Fatalf("turn/start count = %d, want the cancelled input never sent", got)
	}
}

// A resent native steer is not delivered twice: the queue's resend check covers it.
func TestResentSteerIsDeliveredOnce(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-steer-resend")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)
	for i := 0; i < 2; i++ {
		if err := h.Steer(memberInput("also this", "af_steer")); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.callCount("turn/steer"); got != 1 {
		t.Fatalf("turn/steer count = %d, want 1", got)
	}
	m.complete("completed")
	waitPumpDone(t, h)
}

// LiveHandle answers from the registry and starts nothing.
func TestLiveHandle(t *testing.T) {
	_, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-live")
	d := managedDriver{}
	if _, ok := d.LiveHandle(session.Meta{Name: h.name}); ok {
		t.Fatal("LiveHandle found a handle that was never registered")
	}
	registerCodexTestHandle(t, h)
	got, ok := d.LiveHandle(session.Meta{Name: h.name})
	if !ok || got != agents.ThreadHandle(h) {
		t.Fatal("LiveHandle did not return the registered handle")
	}
}

// A turn a previous Agent left running (taken over by Resume, no pump) is a running turn: a
// first stop ends it and the input queued behind it continues, rather than being taken for the
// turn being started.
func TestFirstStopOnATakenOverTurnKeepsTheQueue(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-taken-over-first")
	registerCodexTestHandle(t, h)
	h.mu.Lock()
	h.running, h.turnID, h.state = true, "turn_external", agents.TurnRunning
	h.mu.Unlock()
	if err := h.Send(memberInput("after it", "af_after")); err != nil {
		t.Fatal(err)
	}
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst || res.Discard != nil {
		t.Fatalf("stop = %+v, want a first stop discarding nothing", res)
	}
	waitCodexCalls(t, m, "turn/interrupt", 1)
	dispatchNotification(rpcMsg{Method: "turn/completed", Params: json.RawMessage(
		`{"threadId":"thr_test","turn":{"id":"turn_external","status":"interrupted"}}`)})
	completeTurn(t, m, h, 1)
	waitPumpDone(t, h)
	if got := fmt.Sprint(sentTurns(m)); got != "[after it]" {
		t.Fatalf("turns = %s, want the queued input to run after the taken-over turn", got)
	}
}

// DropHandle while a turn/start is out leaves a stop pending on it, so the turn it creates is
// interrupted as soon as the answer names it.
func TestDropHandleStopsTheTurnInFlight(t *testing.T) {
	m, cl := newMockCodexServer(t)
	hold := make(chan struct{})
	m.holdStart = hold
	h := newCodexTestHandle(t, cl, "codex-drop-in-flight")
	registerCodexTestHandle(t, h)
	if err := h.Send(memberInput("in flight", "af_flight")); err != nil {
		t.Fatal(err)
	}
	waitCodexCalls(t, m, "turn/start", 1)
	// The mock answers nothing while turn/start is held, DropHandle's unsubscribe included, so
	// DropHandle runs aside; alive=false is set in the same critical section as its stop.
	dropped := make(chan struct{})
	go func() {
		DropHandle(h.name)
		close(dropped)
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		h.mu.Lock()
		alive := h.alive
		h.mu.Unlock()
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("DropHandle never took the lock")
		}
	}
	if got := m.callCount("turn/interrupt"); got != 0 {
		t.Fatalf("turn/interrupt sent before the turn had an id: %d", got)
	}
	close(hold)
	<-dropped
	waitCodexCalls(t, m, "turn/interrupt", 1)
	// The dropped handle is out of the registry, so no turn/completed reaches its pump:
	// DropHandle itself ends that wait (#1307).
	waitPumpGone(t, h)
}

// A pump waiting for its turn's turn/completed must end with DropHandle (#1307). The handle
// leaves the registry first, so the turn/completed its turn/interrupt produces reaches nobody,
// and before the fix only a lost connection woke the pump.
func TestDropHandleEndsThePumpWaitingOnATurn(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-drop-running")
	registerCodexTestHandle(t, h)
	startLong(t, m, h)

	DropHandle(h.name)
	waitCodexCalls(t, m, "turn/interrupt", 1)
	waitPumpGone(t, h)
	h.mu.Lock()
	st := h.state
	h.mu.Unlock()
	if st != agents.TurnCancelled {
		t.Fatalf("state = %s, want cancelled", st)
	}
}

// waitPumpGone fails when the pump is still running a few seconds after DropHandle. Short on
// purpose: DropHandle ends the wait at once, and the backstop of waitPumpDone would only turn a
// regression into a 30-second test.
func waitPumpGone(t *testing.T, h *threadHandle) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		h.mu.Lock()
		pumping := h.pumping
		h.mu.Unlock()
		if !pumping {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the pump outlived DropHandle")
		}
	}
}
