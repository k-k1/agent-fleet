package opencode

// The two-stage stop (ADR 0105) on the opencode driver: what a stop does to the queue, checked
// through the mock serve down to the /message calls it ends up making.

import (
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

// waitTurns blocks until serve has received n turns.
func waitTurns(t *testing.T, m *mockServe, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(sentTurns(m)) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("serve received %q, want %d turns", sentTurns(m), n)
}

// endTurn lets the turn serve is running return, as a completion.
func endTurn(m *mockServe) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.turnGate != nil {
		select {
		case <-m.turnGate:
		default:
			close(m.turnGate)
		}
	}
}

// startBlocking sends one turn that runs until aborted or ended, and waits until serve has it.
func startBlocking(t *testing.T, m *mockServe, h *threadHandle) {
	t.Helper()
	m.turnDelay = 30 * time.Second
	if err := h.Send(memberInput("long", "msg_long")); err != nil {
		t.Fatal(err)
	}
	waitTurns(t, m, 1)
	waitState(t, h, agents.TurnRunning)
}

func episode(h *threadHandle) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tq().Episode()
}

// Decision 1: a first stop ends the running turn only; the member's own follow-up and a peer
// message both continue, one turn each, in order. The episode ends with the queue.
func TestFirstStopEndsTheTurnAndTheQueueContinues(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	startBlocking(t, m, h)
	for _, in := range []agents.TurnInput{memberInput("own follow-up", "msg_own"), peerInput("from a peer", "msg_peer")} {
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
	waitTurns(t, m, 2)
	endTurn(m)
	waitTurns(t, m, 3)
	endTurn(m)
	waitPumpIdle(t, h)
	if got := fmt.Sprint(sentTurns(m)); got != "[long own follow-up from a peer]" {
		t.Fatalf("turns = %s, want the queue to continue in order", got)
	}
	waitState(t, h, agents.TurnCompleted)
	if episode(h) {
		t.Fatal("the stop episode outlived the queue it continued into")
	}
}

// Decision 2: a stop inside the episode aborts the continued turn and discards the rest, the
// peer message included, and keeps it for return.
func TestSecondStopDiscardsTheRestAndKeepsIt(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	startBlocking(t, m, h)
	for _, in := range []agents.TurnInput{memberInput("own follow-up", "msg_own"), peerInput("from a peer", "msg_peer"), memberInput("then deploy", "msg_deploy")} {
		if err := h.Send(in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
		t.Fatal(err)
	}
	waitTurns(t, m, 2)
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if res.Discard != nil {
		for _, it := range res.Discard.Items {
			got = append(got, it.ID+"/"+it.Origin.Kind)
		}
	}
	if res.Stop != agents.StopSecond || fmt.Sprint(got) != "[msg_peer/peer msg_deploy/member]" {
		t.Fatalf("second stop = %s discarding %v, want second discarding the peer message and the follow-up", res.Stop, got)
	}
	waitPumpIdle(t, h)
	time.Sleep(100 * time.Millisecond)
	if got := fmt.Sprint(sentTurns(m)); got != "[long own follow-up]" {
		t.Fatalf("turns = %s, want nothing sent after the second stop", got)
	}
	waitState(t, h, agents.TurnCancelled)
	h.mu.Lock()
	kept := h.tq().Discards()
	h.mu.Unlock()
	if len(kept) != 1 || kept[0].ID != res.Discard.ID {
		t.Fatalf("kept discards = %+v, want the second stop's", kept)
	}
}

// Decision 3: discard_queue is the brake outside any episode too.
func TestDiscardQueueWorksOutsideAnEpisode(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	startBlocking(t, m, h)
	if err := h.Send(memberInput("queued", "msg_q")); err != nil {
		t.Fatal(err)
	}
	res, err := h.Interrupt(agents.InterruptOpts{DiscardQueue: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopDiscard || res.Discard == nil || len(res.Discard.Items) != 1 {
		t.Fatalf("discard stop = %+v, want the queued input discarded", res)
	}
	waitPumpIdle(t, h)
	time.Sleep(100 * time.Millisecond)
	if got := fmt.Sprint(sentTurns(m)); got != "[long]" {
		t.Fatalf("turns = %s, want the discarded input never sent", got)
	}
	waitState(t, h, agents.TurnCancelled)
}

// Decision 5: an entry can be removed while it waits; once /message is out it is already
// started.
func TestRemoveQueued(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	startBlocking(t, m, h)
	for _, in := range []agents.TurnInput{memberInput("remove me", "msg_rm"), memberInput("keep me", "msg_keep")} {
		if err := h.Send(in); err != nil {
			t.Fatal(err)
		}
	}
	if it, err := h.RemoveQueued("msg_rm"); err != nil || it.ID != "msg_rm" {
		t.Fatalf("remove = %+v, %v", it, err)
	}
	endTurn(m)
	waitTurns(t, m, 2)
	if _, err := h.RemoveQueued("msg_keep"); !errors.Is(err, agents.ErrAlreadyStarted) {
		t.Fatalf("remove of the sent entry = %v, want already started", err)
	}
	endTurn(m)
	waitPumpIdle(t, h)
	if got := fmt.Sprint(sentTurns(m)); got != "[long keep me]" {
		t.Fatalf("turns = %s, want the removed entry never sent", got)
	}
}

// Decision 2: new member input ends the episode, a peer message does not — although accept
// rewrites the displayed state to queued for both.
func TestEpisodeEndsOnMemberInputOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    agents.TurnInput
		want  agents.StopKind
		turns string
	}{
		{"peer keeps the episode", peerInput("from a peer", "msg_peer"), agents.StopSecond, "[long first follow-up]"},
		{"member ends the episode", memberInput("new request", "msg_new"), agents.StopFirst, "[long first follow-up new request]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := newMockServe(t)
			h := newTestHandle(t, srv)
			startBlocking(t, m, h)
			if err := h.Send(memberInput("first follow-up", "msg_first")); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
				t.Fatal(err)
			}
			waitTurns(t, m, 2)
			waitState(t, h, agents.TurnRunning)
			if queued, err := h.SendQueued(tc.in); err != nil || !queued {
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
				waitTurns(t, m, 3)
				endTurn(m)
			}
			waitPumpIdle(t, h)
			time.Sleep(100 * time.Millisecond)
			if got := fmt.Sprint(sentTurns(m)); got != tc.turns {
				t.Fatalf("turns = %s, want %s", got, tc.turns)
			}
		})
	}
}

// Decision 3's hand-over: a stop that finds the input committed but not yet sent leaves the
// delivery to the pump, which then does not send it. Nothing is aborted on serve, where
// nothing of ours runs yet. The window is a few statements wide, so the pump's steps are
// driven directly.
func TestStopBetweenCommitAndSendIsNotSent(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	h.mu.Lock()
	h.tq().Accept(memberInput("committed", "msg_c"))
	tk := h.tq().Take()
	h.running = true
	h.mu.Unlock()
	if !h.commit(tk) {
		t.Fatal("commit refused an entry nothing had cancelled")
	}
	if _, err := h.RemoveQueued("msg_c"); !errors.Is(err, agents.ErrAlreadyStarted) {
		t.Fatalf("remove after commit = %v, want already started", err)
	}
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst {
		t.Fatalf("stop = %s, want first", res.Stop)
	}
	m.mu.Lock()
	aborts := m.aborts
	m.mu.Unlock()
	if aborts != 0 {
		t.Fatalf("the stop aborted serve %d times before the input was sent", aborts)
	}
	h.runTurn(tk)
	h.mu.Lock()
	h.tq().Settle(tk)
	h.running = false
	h.mu.Unlock()
	if got := sentTurns(m); len(got) != 0 {
		t.Fatalf("turns = %q, want the stopped input never sent", got)
	}
	waitState(t, h, agents.TurnCancelled)
}

// DropHandle is teardown (decision 8): the queue goes with it and nothing is kept for return.
func TestDropHandleDiscardsTheQueue(t *testing.T) {
	m, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	startBlocking(t, m, h)
	if err := h.Send(memberInput("queued", "msg_q")); err != nil {
		t.Fatal(err)
	}
	DropHandle(h.name)
	waitPumpIdle(t, h)
	h.mu.Lock()
	left, kept := h.tq().Len(), h.tq().Discards()
	h.mu.Unlock()
	if left != 0 || len(kept) != 0 {
		t.Fatalf("after DropHandle: %d queued, %d discards kept; want neither", left, len(kept))
	}
	if got := fmt.Sprint(sentTurns(m)); got != "[long]" {
		t.Fatalf("turns = %s, want nothing sent after the teardown", got)
	}
}

// Input the pump has not taken yet is the turn being started only when serve runs nothing
// (decision 1): then a first stop cancels it and keeps it for return (first_stop); behind
// another client's turn it is queued and continues.
func TestFirstStopOnInputNotYetTaken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		foreign bool
		left    int
	}{
		{"idle session: cancelled", false, 0},
		{"behind another client's turn: kept", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := newMockServe(t)
			h := newTestHandle(t, srv)
			m.mu.Lock()
			m.busy = tc.foreign
			m.mu.Unlock()
			h.mu.Lock()
			h.pumping = true // the pump goroutine accept starts has not run yet
			h.mu.Unlock()
			if err := h.Send(memberInput("about to start", "msg_soon")); err != nil {
				t.Fatal(err)
			}
			res, err := h.Interrupt(agents.InterruptOpts{})
			if err != nil {
				t.Fatal(err)
			}
			wantKept := 1 - tc.left
			if res.Stop != agents.StopFirst || (res.Discard != nil) != (wantKept == 1) {
				t.Fatalf("stop = %+v, want a first stop keeping %d unsent input", res, wantKept)
			}
			if res.Discard != nil && (res.Discard.Reason != agents.DiscardFirstStop || res.Discard.Items[0].ID != "msg_soon") {
				t.Fatalf("discard = %+v, want the unsent input as first_stop", res.Discard)
			}
			h.mu.Lock()
			left, kept := h.tq().Len(), h.tq().Discards()
			h.mu.Unlock()
			if left != tc.left || len(kept) != wantKept {
				t.Fatalf("after the stop: %d queued, %d kept; want %d queued and %d kept", left, len(kept), tc.left, wantKept)
			}
			endForeignTurn(m)
			h.pump()
			if got := len(sentTurns(m)); got != tc.left {
				t.Fatalf("%d turns sent, want %d", got, tc.left)
			}
		})
	}
}

// LiveHandle answers from the registry and starts nothing.
func TestLiveHandle(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	d := managedDriver{}
	if _, ok := d.LiveHandle(session.Meta{Name: h.name}); ok {
		t.Fatal("LiveHandle found a handle that was never registered")
	}
	registerTestHandle(t, h)
	got, ok := d.LiveHandle(session.Meta{Name: h.name})
	if !ok || got != agents.ThreadHandle(h) {
		t.Fatal("LiveHandle did not return the registered handle")
	}
}
