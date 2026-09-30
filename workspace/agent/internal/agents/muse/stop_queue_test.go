package muse

// The two-stage stop (ADR 0105) on the muse driver: what a stop does to the queue, checked
// through the fake host down to the turn/start and turn/interrupt it ends up sending.

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func memberInput(prompt, id string) agents.TurnInput {
	return agents.TurnInput{Prompt: prompt, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginMember}}
}

func peerInput(prompt, id string) agents.TurnInput {
	return agents.TurnInput{Prompt: prompt, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginPeer, From: "other"}}
}

func acceptInterrupts(host *msptest.Host) {
	host.Handle(msp.MethodTurnInterrupt, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
}

// interrupts counts the turn/interrupt commands the client has sent.
func interrupts(host *msptest.Host) int {
	n := 0
	for _, m := range host.Received() {
		if m.Method == msp.MethodTurnInterrupt {
			n++
		}
	}
	return n
}

func interruptTarget(t *testing.T, m msptest.Message) string {
	t.Helper()
	var p msp.TurnInterruptParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.TurnID == nil {
		return ""
	}
	return *p.TurnID
}

// waitSent is the host's WaitFor with a deadline: a stop that is never delivered fails the test
// in seconds rather than hanging until the package timeout.
func waitSent(t *testing.T, host *msptest.Host, pred func(msptest.Message) bool) msptest.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, m := range host.Received() {
			if pred(m) {
				return m
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the expected message never reached the host")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isMethod(method string) func(msptest.Message) bool {
	return func(m msptest.Message) bool { return m.Method == method }
}

// started reports p's turn started the way the host does, and waits until the handle runs it.
func started(t *testing.T, h *threadHandle, host *msptest.Host, p msp.TurnStartParams) {
	t.Helper()
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
}

func completed(t *testing.T, h *threadHandle, host *msptest.Host, turnID string, how msp.TurnTerminal, want agents.TurnState) {
	t.Helper()
	host.Notify(msp.NotificationTurnCompleted, msp.TurnCompletedParams{TurnID: turnID, SessionID: h.sid, Terminal: how})
	waitEvent(t, h, want)
}

// settled reports the queue empty, nothing taken and no episode open.
func settled(h *threadHandle) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tq().Head() == nil && h.tq().Len() == 0 && !h.tq().Episode()
}

// Decision 1: after a first stop the member's own follow-up and a peer message both run, one
// turn each, in order; the episode closes once the last of them has settled.
func TestFirstStopContinuesTheQueueInOrder(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	acceptInterrupts(host)
	t1 := runTurn(t, h, host, starts, "long")
	for _, in := range []agents.TurnInput{memberInput("own follow-up", "cm-own"), peerInput("from a peer", "cm-peer")} {
		if err := h.Send(in); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := h.Interrupt(agents.InterruptOpts{}); err != nil || res.Stop != agents.StopFirst || res.Discard != nil {
		t.Fatalf("first stop = %+v, %v", res, err)
	}
	completed(t, h, host, t1, msp.TurnTerminalCancelled, agents.TurnCancelled)
	for _, want := range []string{"own follow-up", "from a peer"} {
		p := nextStart(t, starts)
		if got := *p.Input[0].Text; got != want {
			t.Fatalf("started %q, want %q", got, want)
		}
		started(t, h, host, p)
		completed(t, h, host, p.CommandID, msp.TurnTerminalCompleted, agents.TurnCompleted)
	}
	if n := interrupts(host); n != 1 {
		t.Fatalf("%d turn/interrupt sent, want 1", n)
	}
	if !settled(h) {
		t.Fatal("the queue or the episode outlived the turns it continued into")
	}
}

// Decision 3: discard_queue is the brake outside any episode too, and a queued entry it
// cancels before the pump commits it is never sent.
func TestDiscardQueueWorksOutsideAnEpisode(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	acceptInterrupts(host)
	t1 := runTurn(t, h, host, starts, "long")
	if err := h.Send(memberInput("queued", "cm-q")); err != nil {
		t.Fatal(err)
	}
	res, err := h.Interrupt(agents.InterruptOpts{DiscardQueue: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopDiscard || res.Discard == nil || len(res.Discard.Items) != 1 || res.Discard.Items[0].ID != "cm-q" {
		t.Fatalf("discard stop = %+v, want the queued input discarded", res)
	}
	completed(t, h, host, t1, msp.TurnTerminalCancelled, agents.TurnCancelled)
	noStart(t, starts, "the discarded input")
	if !settled(h) {
		t.Fatal("the queue is not settled after the discard")
	}
}

// Decision 5: an entry can be removed while it waits; once its turn/start is out it is
// already started.
func TestRemoveQueued(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	t1 := runTurn(t, h, host, starts, "long")
	for _, in := range []agents.TurnInput{memberInput("remove me", "cm-rm"), memberInput("keep me", "cm-keep")} {
		if err := h.Send(in); err != nil {
			t.Fatal(err)
		}
	}
	if it, err := h.RemoveQueued("cm-rm"); err != nil || it.Text != "remove me" {
		t.Fatalf("remove = %+v, %v", it, err)
	}
	completed(t, h, host, t1, msp.TurnTerminalCompleted, agents.TurnCompleted)
	p := nextStart(t, starts)
	if got := *p.Input[0].Text; got != "keep me" {
		t.Fatalf("started %q, want the entry that was not removed", got)
	}
	if _, err := h.RemoveQueued("cm-keep"); !errors.Is(err, agents.ErrAlreadyStarted) {
		t.Fatalf("remove while its turn/start is out = %v, want already started", err)
	}
	started(t, h, host, p)
	completed(t, h, host, p.CommandID, msp.TurnTerminalCompleted, agents.TurnCompleted)
	noStart(t, starts, "the removed entry")
}

// hostQueues answers every turn/start as queued in the host's own queue, behind a turn this
// handle did not start.
func hostQueues(host *msptest.Host) chan msp.TurnStartParams {
	starts := make(chan msp.TurnStartParams, 8)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		starts <- p
		return msp.TurnStartResult{Disposition: msp.TurnStartDispositionQueued, TurnID: p.CommandID}, nil
	})
	return starts
}

// Input the host queued behind a turn this handle did not start is queued, not the turn being
// stopped: a first stop ends what runs ahead of it and lets it continue; a second one stops it
// the moment the host starts it. Either way it is past removing.
func TestHostQueuedInputAndTheTwoStops(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stops    int
		want     agents.StopKind
		interOn  bool // whether the host-started turn is interrupted on arrival
		finalSt  msp.TurnTerminal
		finalEvt agents.TurnState
	}{
		{"first stop continues", 1, agents.StopFirst, false, msp.TurnTerminalCompleted, agents.TurnCompleted},
		{"second stop stops it on arrival", 2, agents.StopSecond, true, msp.TurnTerminalCancelled, agents.TurnCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &threadHandle{}
			host := newTestHandle(t, h)
			starts := hostQueues(host)
			acceptInterrupts(host)
			queued, err := h.SendQueued(peerInput("from a peer", "cm-peer"))
			if err != nil || !queued {
				t.Fatalf("send = queued %v, %v; want held by the host", queued, err)
			}
			p := nextStart(t, starts)
			h.mu.Lock()
			items := h.tq().Items()
			h.mu.Unlock()
			if len(items) != 1 || items[0].State != agents.EntrySent {
				t.Fatalf("items = %+v, want the input shown as sent", items)
			}
			if _, err := h.RemoveQueued("cm-peer"); !errors.Is(err, agents.ErrAlreadyStarted) {
				t.Fatalf("remove of host-held input = %v, want already started", err)
			}

			var res agents.InterruptResult
			for i := 0; i < tc.stops; i++ {
				if res, err = h.Interrupt(agents.InterruptOpts{}); err != nil {
					t.Fatal(err)
				}
			}
			if res.Stop != tc.want || res.Discard != nil {
				t.Fatalf("last stop = %+v, want %s with nothing cancellable to discard", res, tc.want)
			}
			// The first stop ends the turn the host runs ahead of it, which this handle cannot
			// name; a second stop adds nothing while no turn of ours exists.
			first := waitSent(t, host, isMethod(msp.MethodTurnInterrupt))
			if got := interruptTarget(t, first); got != "" {
				t.Fatalf("first stop targeted %q, want the host's running turn", got)
			}
			if n := interrupts(host); n != 1 {
				t.Fatalf("%d turn/interrupt before the host started the input, want 1", n)
			}

			host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
			if tc.interOn {
				waitEvent(t, h, agents.TurnInterrupting)
				waitSent(t, host, func(m msptest.Message) bool {
					return m.Method == msp.MethodTurnInterrupt && interruptTarget(t, m) == p.CommandID
				})
			} else {
				waitEvent(t, h, agents.TurnRunning)
			}
			completed(t, h, host, p.CommandID, tc.finalSt, tc.finalEvt)
			time.Sleep(100 * time.Millisecond)
			want := 1
			if tc.interOn {
				want = 2
			}
			if n := interrupts(host); n != want {
				t.Fatalf("%d turn/interrupt in all, want %d", n, want)
			}
			if !settled(h) {
				t.Fatal("the queue is not settled after the host-held turn ended")
			}
		})
	}
}

// Decision 2: new member input ends the episode; a peer message does not.
func TestEpisodeEndsOnMemberInputOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   agents.TurnInput
		want agents.StopKind
	}{
		{"peer keeps the episode", peerInput("from a peer", "cm-peer"), agents.StopSecond},
		{"member ends the episode", memberInput("new request", "cm-new"), agents.StopFirst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &threadHandle{}
			host := newTestHandle(t, h)
			starts := recordStarts(host)
			acceptInterrupts(host)
			t1 := runTurn(t, h, host, starts, "long")
			if err := h.Send(memberInput("first follow-up", "cm-first")); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
				t.Fatal(err)
			}
			completed(t, h, host, t1, msp.TurnTerminalCancelled, agents.TurnCancelled)
			p := nextStart(t, starts)
			started(t, h, host, p)

			if queued, err := h.SendQueued(tc.in); err != nil || !queued {
				t.Fatalf("send behind the continued turn: queued=%v err=%v", queued, err)
			}
			res, err := h.Interrupt(agents.InterruptOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Stop != tc.want {
				t.Fatalf("stop = %s, want %s", res.Stop, tc.want)
			}
			completed(t, h, host, p.CommandID, msp.TurnTerminalCancelled, agents.TurnCancelled)
			if tc.want == agents.StopFirst {
				next := nextStart(t, starts)
				started(t, h, host, next)
				completed(t, h, host, next.CommandID, msp.TurnTerminalCompleted, agents.TurnCompleted)
			} else {
				noStart(t, starts, "the input the second stop discarded")
			}
			if !settled(h) {
				t.Fatal("the queue is not settled")
			}
		})
	}
}

// A native steer of member input ends the episode as well: it is accepted without the queue.
func TestNativeSteerEndsTheEpisode(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	acceptInterrupts(host)
	host.Handle(msp.MethodTurnSteer, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	t1 := runTurn(t, h, host, starts, "long")
	if err := h.Send(peerInput("from a peer", "cm-peer")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
		t.Fatal(err)
	}
	completed(t, h, host, t1, msp.TurnTerminalCancelled, agents.TurnCancelled)
	p := nextStart(t, starts)
	started(t, h, host, p)
	h.mu.Lock()
	open := h.tq().Episode()
	h.mu.Unlock()
	if !open {
		t.Fatal("no episode while the continued turn runs")
	}
	if err := h.Steer(memberInput("also this", "cm-steer")); err != nil {
		t.Fatal(err)
	}
	host.WaitForMethod(msp.MethodTurnSteer)
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst {
		t.Fatalf("stop after a member steer = %s, want first", res.Stop)
	}
	completed(t, h, host, p.CommandID, msp.TurnTerminalCancelled, agents.TurnCancelled)
}

// An input the host died under goes back to the head of the queue, unless a stop was waiting
// for it: then it is not started again on the respawn.
func TestStopPendingInputIsNotRequeuedWhenTheHostDies(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	acceptInterrupts(host)
	release := make(chan struct{})
	n := 0
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		if n++; n == 1 {
			return msp.TurnStartResult{Disposition: msp.TurnStartDispositionStarted, TurnID: p.CommandID}, nil
		}
		<-release
		host.Close() // before the answer is written, so the call fails with ErrClosed
		return nil, nil
	})
	if err := h.Send(memberInput("long", "cm-long")); err != nil {
		t.Fatal(err)
	}
	first := host.WaitForMethod(msp.MethodTurnStart)
	var fp msp.TurnStartParams
	json.Unmarshal(first.Params, &fp)
	started(t, h, host, fp)
	if err := h.Send(memberInput("doomed", "cm-doomed")); err != nil {
		t.Fatal(err)
	}
	completed(t, h, host, fp.CommandID, msp.TurnTerminalCompleted, agents.TurnCompleted)
	waitSent(t, host, func(m msptest.Message) bool {
		var p msp.TurnStartParams
		return m.Method == msp.MethodTurnStart && json.Unmarshal(m.Params, &p) == nil && *p.Input[0].Text == "doomed"
	})
	if res, err := h.Interrupt(agents.InterruptOpts{}); err != nil || res.Stop != agents.StopFirst {
		t.Fatalf("stop = %+v, %v", res, err)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for !settled(h) {
		if time.Now().After(deadline) {
			h.mu.Lock()
			left := h.tq().Texts()
			h.mu.Unlock()
			t.Fatalf("queue = %q after the host died, want the stopped input dropped", left)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A host that dies after admitting input into its own queue (disposition "queued") never ran
// it: the input goes back to the front for the respawn. With a stop waiting for it, it does not.
func TestHostLossRequeuesInputTheHostHeld(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stops int
		want  []string
	}{
		{"no stop", 0, []string{"from a peer"}},
		{"second stop pending", 2, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &threadHandle{}
			host := newTestHandle(t, h)
			hostQueues(host)
			acceptInterrupts(host)
			if queued, err := h.SendQueued(peerInput("from a peer", "cm-peer")); err != nil || !queued {
				t.Fatalf("send = queued %v, %v; want held by the host", queued, err)
			}
			for i := 0; i < tc.stops; i++ {
				if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
					t.Fatal(err)
				}
			}
			cl := h.cl
			host.Close()
			h.hostLost(cl)
			h.mu.Lock()
			head, left := h.tq().Head(), h.tq().Texts()
			h.mu.Unlock()
			if head != nil || fmt.Sprint(left) != fmt.Sprint(tc.want) {
				t.Fatalf("after the host died: head %v, queue %q; want no head and %q", head != nil, left, tc.want)
			}
		})
	}
}

// DropHandle is teardown (decision 8): the queue goes with it, even one a lost host left with
// no turn running, and nothing is kept for return.
func TestDropHandleDiscardsTheQueue(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	acceptInterrupts(host)
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})
	runTurn(t, h, host, starts, "long")
	if err := h.Send(memberInput("queued", "cm-q")); err != nil {
		t.Fatal(err)
	}
	// A lost host leaves the queue behind with no turn running, so nothing but DropHandle's own
	// discard clears it.
	cl := h.cl
	host.Close()
	h.hostLost(cl)
	DropHandle(h.name)
	h.mu.Lock()
	left, kept := h.tq().Len(), h.tq().Discards()
	h.mu.Unlock()
	if left != 0 || len(kept) != 0 {
		t.Fatalf("after DropHandle: %d queued, %d discards kept; want neither", left, len(kept))
	}
}

// Input a lost host left queued, which the respawned host's pump has not taken yet, is the turn
// being started when nothing runs (decision 1): a first stop cancels it and keeps it for return
// (first_stop), and nothing is interrupted on the host.
func TestFirstStopCancelsInputNotYetTaken(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	runTurn(t, h, host, starts, "long")
	if err := h.Send(memberInput("left behind", "cm-left")); err != nil {
		t.Fatal(err)
	}
	cl := h.cl
	host.Close()
	h.hostLost(cl)

	host2, cl2 := msptest.New(t, msp.Handler{OnNotification: h.onNotify, OnRequest: h.onRequest})
	starts2 := recordStarts(host2)
	acceptInterrupts(host2)
	h.mu.Lock()
	h.cl, h.alive = cl2, true // respawned; its pump has not run yet
	h.mu.Unlock()
	res, err := h.Interrupt(agents.InterruptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != agents.StopFirst || res.Discard == nil || res.Discard.Reason != agents.DiscardFirstStop ||
		res.Discard.Items[0].ID != "cm-left" {
		t.Fatalf("stop = %+v, want a first stop keeping the unsent input", res)
	}
	h.pump()
	noStart(t, starts2, "the cancelled input")
	if n := interrupts(host2); n != 0 {
		t.Fatalf("%d turn/interrupt sent with nothing running", n)
	}
	if !settled(h) {
		t.Fatal("the queue is not settled after the stop")
	}
}

// A resent native steer is not delivered twice: the queue's resend check covers it.
func TestResentSteerIsDeliveredOnce(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	starts := recordStarts(host)
	host.Handle(msp.MethodTurnSteer, func(m msptest.Message) (any, *msp.Error) {
		return msp.CommandAcceptedResult{}, nil
	})
	runTurn(t, h, host, starts, "long")
	for i := 0; i < 2; i++ {
		if err := h.Steer(memberInput("also this", "cm-steer")); err != nil {
			t.Fatal(err)
		}
	}
	host.WaitForMethod(msp.MethodTurnSteer)
	time.Sleep(100 * time.Millisecond)
	n := 0
	for _, m := range host.Received() {
		if m.Method == msp.MethodTurnSteer {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("turn/steer count = %d, want 1", n)
	}
}

// LiveHandle answers from the registry and starts nothing.
func TestLiveHandle(t *testing.T) {
	h := &threadHandle{}
	newTestHandle(t, h)
	d := managedDriver{}
	if _, ok := d.LiveHandle(session.Meta{Name: h.name}); ok {
		t.Fatal("LiveHandle found a handle that was never registered")
	}
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})
	got, ok := d.LiveHandle(session.Meta{Name: h.name})
	if !ok || got != agents.ThreadHandle(h) {
		t.Fatal("LiveHandle did not return the registered handle")
	}
}

// A first stop that arrives while turn/start is out, before the host answers "queued", takes
// the input for the turn being started. Once the answer shows the input waits behind another
// client's turn, the stop belongs to that turn: it goes there, and the input starts later
// untouched (decision 1). A second stop after the answer, or a discard_queue before it, stays on
// the input and stops it at turn/started.
func TestFirstStopBeforeTheQueuedAnswerGoesToTheTurnAhead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		before     agents.InterruptOpts // the stop sent while the answer is held back
		secondStop bool                 // another stop after the answer
		stopsInput bool                 // the input is interrupted at its turn/started
	}{
		{"first stop is redirected", agents.InterruptOpts{}, false, false},
		{"second stop after the answer stops the input", agents.InterruptOpts{}, true, true},
		{"discard_queue stays on the input", agents.InterruptOpts{DiscardQueue: true}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &threadHandle{}
			host := newTestHandle(t, h)
			acceptInterrupts(host)
			release := make(chan struct{})
			starts := make(chan msp.TurnStartParams, 1)
			host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
				var p msp.TurnStartParams
				json.Unmarshal(m.Params, &p)
				starts <- p
				<-release
				return msp.TurnStartResult{Disposition: msp.TurnStartDispositionQueued, TurnID: p.CommandID}, nil
			})
			sent := make(chan bool, 1)
			go func() {
				queued, _ := h.SendQueued(peerInput("from a peer", "cm-peer"))
				sent <- queued
			}()
			p := nextStart(t, starts)
			if _, err := h.Interrupt(tc.before); err != nil {
				t.Fatal(err)
			}
			if n := interrupts(host); n != 0 {
				t.Fatalf("%d turn/interrupt before the host answered, want none", n)
			}
			close(release)
			if !<-sent {
				t.Fatal("the host's queued answer was not reported as queued")
			}

			// ① the stop reaches the turn ahead (unnamed: this handle never saw it start).
			wantAhead := 0
			if tc.before == (agents.InterruptOpts{}) {
				wantAhead = 1
				deadline := time.Now().Add(5 * time.Second)
				for interrupts(host) == 0 {
					if time.Now().After(deadline) {
						t.Fatal("the first stop never reached the turn running ahead of the input")
					}
					time.Sleep(10 * time.Millisecond)
				}
				for _, m := range host.Received() {
					if m.Method == msp.MethodTurnInterrupt {
						if got := interruptTarget(t, m); got != "" {
							t.Fatalf("redirected stop targeted %q, want the host's running turn", got)
						}
					}
				}
			}
			if tc.secondStop {
				res, err := h.Interrupt(agents.InterruptOpts{})
				if err != nil {
					t.Fatal(err)
				}
				if res.Stop != agents.StopSecond {
					t.Fatalf("stop after the answer = %s, want second (the redirect opened an episode)", res.Stop)
				}
			}
			time.Sleep(50 * time.Millisecond)
			if n := interrupts(host); n != wantAhead {
				t.Fatalf("%d turn/interrupt before the input started, want %d", n, wantAhead)
			}

			// ② the input starts; ③ a stop still on it lands the moment it does.
			host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
			final, finalSt := msp.TurnTerminalCompleted, agents.TurnCompleted
			if tc.stopsInput {
				waitEvent(t, h, agents.TurnInterrupting)
				waitSent(t, host, func(m msptest.Message) bool {
					return m.Method == msp.MethodTurnInterrupt && interruptTarget(t, m) == p.CommandID
				})
				final, finalSt = msp.TurnTerminalCancelled, agents.TurnCancelled
			} else {
				waitEvent(t, h, agents.TurnRunning)
			}
			completed(t, h, host, p.CommandID, final, finalSt)
			time.Sleep(50 * time.Millisecond)
			want := wantAhead
			if tc.stopsInput {
				want++
			}
			if n := interrupts(host); n != want {
				t.Fatalf("%d turn/interrupt in all, want %d", n, want)
			}
			if !settled(h) {
				t.Fatal("the queue is not settled after the input's turn ended")
			}
		})
	}
}

// One first stop is delivered once. With another client's turn already running when this
// handle's turn/start is still out, the host will queue the input behind that turn: the stop
// goes to the running turn at once, and neither the "queued" answer nor the input's own
// turn/started delivers it a second time.
func TestFirstStopWithAForeignTurnRunningIsDeliveredOnce(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	acceptInterrupts(host)
	release := make(chan struct{})
	starts := make(chan msp.TurnStartParams, 1)
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.TurnStartParams
		json.Unmarshal(m.Params, &p)
		starts <- p
		<-release
		return msp.TurnStartResult{Disposition: msp.TurnStartDispositionQueued, TurnID: p.CommandID}, nil
	})
	sent := make(chan bool, 1)
	go func() {
		queued, _ := h.SendQueued(peerInput("from a peer", "cm-peer"))
		sent <- queued
	}()
	p := nextStart(t, starts)
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: "other-client", TurnID: "t-foreign", SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)

	// The fake host answers nothing while it holds turn/start back, the turn/interrupt included,
	// so the stop runs aside; its decision is made under the lock before it sends anything, and
	// the state it sets there says the decision is made.
	type result struct {
		res agents.InterruptResult
		err error
	}
	stopped := make(chan result, 1)
	go func() {
		res, err := h.Interrupt(agents.InterruptOpts{})
		stopped <- result{res, err}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		h.mu.Lock()
		st := h.state
		h.mu.Unlock()
		if st == agents.TurnInterrupting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stop never took the lock")
		}
	}
	close(release)
	r := <-stopped
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.res.Stop != agents.StopFirst || r.res.Discard != nil {
		t.Fatalf("stop = %+v, want a first stop discarding nothing", r.res)
	}
	if got := interruptTarget(t, waitSent(t, host, isMethod(msp.MethodTurnInterrupt))); got != "t-foreign" {
		t.Fatalf("the stop targeted %q, want the running foreign turn", got)
	}
	if !<-sent {
		t.Fatal("the host's queued answer was not reported as queued")
	}
	completed(t, h, host, "t-foreign", msp.TurnTerminalCancelled, agents.TurnCancelled)
	host.Notify(msp.NotificationTurnStarted, msp.TurnStartedParams{CommandID: p.CommandID, TurnID: p.CommandID, SessionID: h.sid})
	waitEvent(t, h, agents.TurnRunning)
	completed(t, h, host, p.CommandID, msp.TurnTerminalCompleted, agents.TurnCompleted)
	time.Sleep(100 * time.Millisecond)
	if n := interrupts(host); n != 1 {
		t.Fatalf("%d turn/interrupt for one stop, want 1", n)
	}
	if !settled(h) {
		t.Fatal("the queue is not settled after the input's turn ended")
	}
}

// Input the host steers into a turn already running gets no turn/started of its own. A stop that
// was waiting for it is delivered when the "steered" answer arrives, to that running turn.
func TestStopPendingOnSteeredInputStopsTheRunningTurn(t *testing.T) {
	h := &threadHandle{}
	host := newTestHandle(t, h)
	acceptInterrupts(host)
	release := make(chan struct{})
	host.Handle(msp.MethodTurnStart, func(m msptest.Message) (any, *msp.Error) {
		<-release
		return msp.TurnStartResult{Disposition: msp.TurnStartDispositionSteered}, nil
	})
	done := make(chan struct{})
	go func() {
		_ = h.Send(memberInput("joins the running turn", "cm-steered"))
		close(done)
	}()
	waitSent(t, host, isMethod(msp.MethodTurnStart))
	if _, err := h.Interrupt(agents.InterruptOpts{DiscardQueue: true}); err != nil {
		t.Fatal(err)
	}
	if n := interrupts(host); n != 0 {
		t.Fatalf("%d turn/interrupt before the answer, want none", n)
	}
	close(release)
	<-done
	waitSent(t, host, isMethod(msp.MethodTurnInterrupt))
	time.Sleep(100 * time.Millisecond)
	if n := interrupts(host); n != 1 {
		t.Fatalf("%d turn/interrupt, want exactly 1", n)
	}
	if !settled(h) {
		t.Fatal("the steered input is still the head")
	}
}
