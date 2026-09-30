package cursor

// The two-stage stop on the ACP pump (ADR 0105, docs/log/128 §1.2). Every test drives the fake
// runtime to the end of the turns it started and asserts the state after the answers were
// processed, not just that a request reached the fake.

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// gatedWriter is the client's stdin in tests. hold() keeps every write from reaching the fake
// until release(), so a session/prompt can sit committed but not yet written.
type gatedWriter struct {
	w    io.Writer
	mu   sync.Mutex
	gate chan struct{}
	err  error
}

func (g *gatedWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	gate := g.gate
	g.mu.Unlock()
	if gate != nil {
		<-gate
		g.mu.Lock()
		err := g.err
		g.err = nil
		g.mu.Unlock()
		if err != nil {
			return 0, err
		}
	}
	return g.w.Write(p)
}

// releaseWith ends the hold with the held write failing, as a write to a dead child does.
func (g *gatedWriter) releaseWith(err error) {
	g.mu.Lock()
	g.err = err
	close(g.gate)
	g.gate = nil
	g.mu.Unlock()
}

func (g *gatedWriter) hold() {
	g.mu.Lock()
	g.gate = make(chan struct{})
	g.mu.Unlock()
}

func (g *gatedWriter) release() {
	g.mu.Lock()
	close(g.gate)
	g.gate = nil
	g.mu.Unlock()
}

func member(id, text string) agents.TurnInput {
	return agents.TurnInput{Prompt: text, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginMember}}
}

func peer(id, text string) agents.TurnInput {
	return agents.TurnInput{Prompt: text, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginPeer, From: "s-peer"}}
}

func mustSend(t *testing.T, h *threadHandle, in agents.TurnInput) {
	t.Helper()
	if err := h.Send(in); err != nil {
		t.Fatalf("Send %s: %v", in.ClientMessageID, err)
	}
}

func mustInterrupt(t *testing.T, h *threadHandle, opts agents.InterruptOpts, want agents.StopKind) agents.InterruptResult {
	t.Helper()
	res, err := h.Interrupt(opts)
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if res.Stop != want {
		t.Fatalf("stop = %q, want %q", res.Stop, want)
	}
	return res
}

func expectPrompt(t *testing.T, f *fakeACP, want string) int64 {
	t.Helper()
	select {
	case id := <-f.gotPrompt:
		got := f.promptTexts()
		if got[len(got)-1] != want {
			t.Fatalf("session/prompt carried %q, want %q (all: %q)", got[len(got)-1], want, got)
		}
		return id
	case <-time.After(5 * time.Second):
		t.Fatalf("no session/prompt for %q", want)
	}
	return 0
}

func expectNoPrompt(t *testing.T, f *fakeACP) {
	t.Helper()
	select {
	case id := <-f.gotPrompt:
		t.Errorf("an unexpected turn started: %q", f.promptTexts())
		f.reply(id, map[string]any{"stopReason": "end_turn"}) // let the pump drain before cleanup
	case <-time.After(300 * time.Millisecond):
	}
}

func expectCancel(t *testing.T, f *fakeACP) {
	t.Helper()
	select {
	case <-f.gotCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("no session/cancel")
	}
}

func expectNoCancel(t *testing.T, f *fakeACP) {
	t.Helper()
	select {
	case <-f.gotCancel:
		t.Error("an unexpected session/cancel")
	case <-time.After(200 * time.Millisecond):
	}
}

// settled waits for the pump to finish and returns what the queue shows then.
func settled(t *testing.T, h *threadHandle) ([]agents.QueueItem, []agents.Discard, bool) {
	t.Helper()
	waitPumpIdle(t, h)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Items(), h.q.Discards(), h.q.Episode()
}

func episode(h *threadHandle) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.q.Episode()
}

func ids(items []agents.QueueItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A first stop ends the running turn only: what is queued, the member's own input and a peer
// message alike, starts afterwards, one turn each, in order.
func TestFirstStopContinuesQueueInOrder(t *testing.T) {
	h, f := newTestHandle(t)
	mustSend(t, h, member("m1", "one"))
	first := expectPrompt(t, f, "one")
	waitState(t, h, agents.TurnRunning)
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))

	res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	if res.Discard != nil {
		t.Errorf("a first stop discarded %+v", res.Discard)
	}
	expectCancel(t, f)
	if !episode(h) {
		t.Error("a first stop that left input queued opened no stop episode")
	}
	if td := payload(t, h); !equal(ids(td.QueuedItems), []string{"m2", "m3"}) || !equal(td.Queued, []string{"two", "three"}) ||
		td.QueuedItems[1].State != agents.EntryQueued || td.QueuedItems[1].Origin.Kind != agents.OriginPeer {
		t.Errorf("messages payload while stopped: queued %q, items %+v", td.Queued, td.QueuedItems)
	}
	f.reply(first, map[string]any{"stopReason": "cancelled"})
	second := expectPrompt(t, f, "two")
	f.reply(second, map[string]any{"stopReason": "end_turn"})
	third := expectPrompt(t, f, "three")
	f.reply(third, map[string]any{"stopReason": "end_turn"})

	items, discards, ep := settled(t, h)
	if got := f.promptTexts(); !equal(got, []string{"one", "two", "three"}) {
		t.Errorf("prompts = %q, want one, two, three", got)
	}
	if len(items) != 0 || len(discards) != 0 || ep {
		t.Errorf("after the queue drained: items %v, discards %v, episode %v", items, discards, ep)
	}
	if st := h.currentState(); st != agents.TurnCompleted {
		t.Errorf("state = %s, want completed", st)
	}
}

// A stop inside the episode stops the continued turn and discards the rest, the peer message
// included, and keeps it for return.
func TestSecondStopDiscardsRestIncludingPeer(t *testing.T) {
	h, f := newTestHandle(t)
	mustSend(t, h, member("m1", "one"))
	first := expectPrompt(t, f, "one")
	waitState(t, h, agents.TurnRunning)
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))
	mustSend(t, h, member("m4", "four"))

	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	expectCancel(t, f)
	f.reply(first, map[string]any{"stopReason": "cancelled"})
	second := expectPrompt(t, f, "two")

	res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopSecond)
	if res.Discard == nil || !equal(ids(res.Discard.Items), []string{"m3", "m4"}) {
		t.Fatalf("second stop discarded %+v, want m3 and m4", res.Discard)
	}
	if res.Discard.Reason != agents.DiscardSecondStop || res.Discard.Items[0].Origin.Kind != agents.OriginPeer {
		t.Errorf("discard = %+v, want reason second_stop with the peer origin kept", res.Discard)
	}
	expectCancel(t, f)
	f.reply(second, map[string]any{"stopReason": "cancelled"})
	expectNoPrompt(t, f)

	items, discards, ep := settled(t, h)
	if len(items) != 0 || ep {
		t.Errorf("after the second stop: items %v, episode %v", items, ep)
	}
	if len(discards) != 1 || discards[0].ID != res.Discard.ID {
		t.Fatalf("kept discards = %+v, want the one the stop returned", discards)
	}
	if st := h.currentState(); st != agents.TurnCancelled {
		t.Errorf("state = %s, want cancelled", st)
	}
	if td := payload(t, h); len(td.Discards) != 1 || td.Discards[0].ID != res.Discard.ID || len(td.QueuedItems) != 0 {
		t.Errorf("messages payload: discards %+v, items %+v", td.Discards, td.QueuedItems)
	}
	if !h.DismissDiscard(res.Discard.ID) || h.DismissDiscard(res.Discard.ID) {
		t.Error("DismissDiscard: want true once, then false")
	}
	if _, discards, _ := settled(t, h); len(discards) != 0 {
		t.Errorf("discards after dismissal = %+v", discards)
	}
}

// discard_queue is the brake outside any episode: it stops the turn and discards everything.
func TestDiscardQueueOutsideEpisode(t *testing.T) {
	h, f := newTestHandle(t)
	mustSend(t, h, member("m1", "one"))
	first := expectPrompt(t, f, "one")
	waitState(t, h, agents.TurnRunning)
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))

	res := mustInterrupt(t, h, agents.InterruptOpts{DiscardQueue: true}, agents.StopDiscard)
	if res.Discard == nil || !equal(ids(res.Discard.Items), []string{"m2", "m3"}) || res.Discard.Reason != agents.DiscardQueue {
		t.Fatalf("discard = %+v, want m2 and m3 for discard_queue", res.Discard)
	}
	expectCancel(t, f)
	f.reply(first, map[string]any{"stopReason": "cancelled"})
	expectNoPrompt(t, f)
	items, discards, ep := settled(t, h)
	if len(items) != 0 || len(discards) != 1 || ep {
		t.Errorf("after discard_queue: items %v, discards %v, episode %v", items, discards, ep)
	}
	if st := h.currentState(); st != agents.TurnCancelled {
		t.Errorf("state = %s, want cancelled", st)
	}

	// Idle, with nothing queued: nothing to stop and nothing to keep.
	res = mustInterrupt(t, h, agents.InterruptOpts{DiscardQueue: true}, agents.StopDiscard)
	if res.Discard != nil {
		t.Errorf("an idle discard_queue returned %+v", res.Discard)
	}
	expectNoCancel(t, f)
}

// holdAtCommit parks the pump between Take and Commit and reports when it got there.
func holdAtCommit(h *threadHandle) (taken <-chan struct{}, release func()) {
	tk, rel := make(chan struct{}, 1), make(chan struct{})
	h.beforeCommit = func() {
		tk <- struct{}{}
		<-rel
	}
	return tk, func() { close(rel) }
}

// A taken entry is still cancellable until the pump commits it: a stop or a removal that takes
// the lock first means it is never sent.
func TestTakenBeforeCommitIsNeverSent(t *testing.T) {
	t.Run("discard_queue", func(t *testing.T) {
		h, f := newTestHandle(t)
		taken, release := holdAtCommit(h)
		mustSend(t, h, member("m1", "one"))
		<-taken
		res := mustInterrupt(t, h, agents.InterruptOpts{DiscardQueue: true}, agents.StopDiscard)
		if res.Discard == nil || !equal(ids(res.Discard.Items), []string{"m1"}) {
			t.Fatalf("discard = %+v, want the taken m1", res.Discard)
		}
		release()
		expectNoPrompt(t, f)
		items, discards, _ := settled(t, h)
		if len(items) != 0 || len(discards) != 1 {
			t.Errorf("items %v, discards %v", items, discards)
		}
		if st := h.currentState(); st != agents.TurnCancelled {
			t.Errorf("state = %s, want cancelled", st)
		}
		expectNoCancel(t, f)
	})
	t.Run("first stop", func(t *testing.T) {
		// With no other turn running, the taken input is the turn being stopped: it does not
		// start, and it is not kept as discarded input either (docs/log/128 §1.2).
		h, f := newTestHandle(t)
		taken, release := holdAtCommit(h)
		mustSend(t, h, member("m1", "one"))
		<-taken
		if res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst); res.Discard != nil {
			t.Errorf("first stop discarded %+v", res.Discard)
		}
		release()
		expectNoPrompt(t, f)
		items, discards, ep := settled(t, h)
		if len(items) != 0 || len(discards) != 0 || ep {
			t.Errorf("items %v, discards %v, episode %v", items, discards, ep)
		}
		if st := h.currentState(); st != agents.TurnCancelled {
			t.Errorf("state = %s, want cancelled", st)
		}
	})
	t.Run("remove", func(t *testing.T) {
		h, f := newTestHandle(t)
		taken, release := holdAtCommit(h)
		mustSend(t, h, member("m1", "one"))
		<-taken
		it, err := h.RemoveQueued("m1")
		if err != nil || it.ID != "m1" || it.Text != "one" {
			t.Fatalf("RemoveQueued = %+v, %v", it, err)
		}
		release()
		expectNoPrompt(t, f)
		if items, _, _ := settled(t, h); len(items) != 0 {
			t.Errorf("items %v", items)
		}
	})
}

// A stop that finds the prompt committed but not yet written leaves the cancel to the pump,
// which sends it once the prompt is on the child's stdin, never before.
func TestStopPendingIsDeliveredAfterTheWrite(t *testing.T) {
	h, f := newTestHandle(t)
	f.writes.hold()
	mustSend(t, h, member("m1", "one"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		items := h.q.Items()
		h.mu.Unlock()
		if len(items) == 1 && items[0].State == agents.EntryCommitted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the entry never became committed: %+v", items)
		}
		time.Sleep(5 * time.Millisecond)
	}
	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	f.writes.release()
	id := expectPrompt(t, f, "one")
	expectCancel(t, f)
	f.reply(id, map[string]any{"stopReason": "cancelled"})
	if items, _, _ := settled(t, h); len(items) != 0 {
		t.Errorf("items %v", items)
	}
	if st := h.currentState(); st != agents.TurnCancelled {
		t.Errorf("state = %s, want cancelled", st)
	}
}

// Removal works while the entry is cancellable, and answers already_started once it is not.
func TestRemoveQueued(t *testing.T) {
	h, f := newTestHandle(t)
	mustSend(t, h, member("m1", "one"))
	first := expectPrompt(t, f, "one")
	waitState(t, h, agents.TurnRunning)
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))

	it, err := h.RemoveQueued("m2")
	if err != nil || it.Text != "two" || it.Origin.Kind != agents.OriginMember {
		t.Fatalf("RemoveQueued(m2) = %+v, %v", it, err)
	}
	if _, err := h.RemoveQueued("m2"); !errors.Is(err, agents.ErrNotQueued) {
		t.Errorf("second RemoveQueued(m2) = %v, want ErrNotQueued", err)
	}
	if _, err := h.RemoveQueued("m1"); !errors.Is(err, agents.ErrAlreadyStarted) {
		t.Errorf("RemoveQueued(m1) = %v, want ErrAlreadyStarted", err)
	}
	f.reply(first, map[string]any{"stopReason": "end_turn"})
	third := expectPrompt(t, f, "three")
	if _, err := h.RemoveQueued("m3"); !errors.Is(err, agents.ErrAlreadyStarted) {
		t.Errorf("RemoveQueued(m3) once sent = %v, want ErrAlreadyStarted", err)
	}
	f.reply(third, map[string]any{"stopReason": "end_turn"})
	items, discards, _ := settled(t, h)
	if got := f.promptTexts(); !equal(got, []string{"one", "three"}) {
		t.Errorf("prompts = %q, want the removed two skipped", got)
	}
	if len(items) != 0 || len(discards) != 0 {
		t.Errorf("items %v, discards %v", items, discards)
	}
}

// Only new member input ends the episode: a resend of queued input and a peer message do not.
func TestEpisodeEndsOnlyOnNewMemberInput(t *testing.T) {
	h, f := newTestHandle(t)
	mustSend(t, h, member("m1", "one"))
	first := expectPrompt(t, f, "one")
	waitState(t, h, agents.TurnRunning)
	mustSend(t, h, member("m2", "two"))
	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	expectCancel(t, f)

	mustSend(t, h, member("m2", "two")) // the Console resending after a lost answer
	if !episode(h) {
		t.Fatal("a resend ended the stop episode")
	}
	mustSend(t, h, peer("m3", "three"))
	if !episode(h) {
		t.Fatal("a peer message ended the stop episode")
	}
	mustSend(t, h, member("m4", "four"))
	if episode(h) {
		t.Fatal("new member input did not end the stop episode")
	}
	// So the next stop is a first stop again, and the queue still continues.
	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	expectCancel(t, f)
	f.reply(first, map[string]any{"stopReason": "cancelled"})
	for _, want := range []string{"two", "three", "four"} {
		f.reply(expectPrompt(t, f, want), map[string]any{"stopReason": "end_turn"})
	}
	expectNoPrompt(t, f) // the resent two is dropped by the ledger when taken
	items, discards, ep := settled(t, h)
	if len(items) != 0 || len(discards) != 0 || ep {
		t.Errorf("items %v, discards %v, episode %v", items, discards, ep)
	}
}

// A child that dies with input queued leaves it queued — not taken off by the pump, recorded
// in the ledger and lost on the dead pipe — and the respawn starts it, in order, without
// waiting for another send.
func TestQueueSurvivesChildDeathAndStartsAfterRespawn(t *testing.T) {
	h, f := newTestHandle(t)
	mustSend(t, h, member("m1", "one"))
	<-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))

	// The child dies. watch has not marked the handle yet (it waits for the process to be
	// reaped), which is the window the pump used to take the next input in.
	f.toClient.Close()
	waitState(t, h, agents.TurnUnknown)
	waitPumpIdle(t, h)
	if got := h.queuedPrompts(); !equal(got, []string{"two", "three"}) {
		t.Fatalf("queue after the child died = %q, want [two three]", got)
	}

	// What spawn does once the new child is up.
	cl2, f2 := newFakeACP(t)
	h.mu.Lock()
	h.cl, h.alive = cl2, true
	h.mu.Unlock()
	h.cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(h.cl, id, method, params)
	}
	h.cl.onNotify = h.onNotify
	h.resumePump()
	f2.reply(expectPrompt(t, f2, "two"), map[string]any{"stopReason": "end_turn"})
	f2.reply(expectPrompt(t, f2, "three"), map[string]any{"stopReason": "end_turn"})
	items, _, _ := settled(t, h)
	if got := f2.promptTexts(); !equal(got, []string{"two", "three"}) {
		t.Errorf("the respawned child got %q, want [two three]", got)
	}
	if len(items) != 0 {
		t.Errorf("items %v", items)
	}
	if st := h.currentState(); st != agents.TurnCompleted {
		t.Errorf("state = %s, want completed", st)
	}
}

// waitingTexts is the prompts still waiting in the queue. The head the pump committed shows in
// Items as committed until its write completes, which can land just after the fake has read it.
func waitingTexts(h *threadHandle) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, it := range h.q.Items() {
		if it.State == agents.EntryQueued {
			out = append(out, it.Text)
		}
	}
	return out
}

var _ agents.LiveHandles = managedDriver{}

// Input accepted while nothing runs is the turn being started even before the pump takes it: a
// first stop in that window stops it, and what was queued behind it continues.
func TestFirstStopBeforeThePumpTakes(t *testing.T) {
	h, f := newTestHandle(t)
	h.mu.Lock()
	h.pumping = true // hold the pump off: the window between accept and Take
	h.mu.Unlock()
	mustSend(t, h, member("m1", "one"))
	mustSend(t, h, member("m2", "two"))
	if res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst); res.Discard != nil {
		t.Errorf("first stop discarded %+v", res.Discard)
	}
	if st := h.currentState(); st != agents.TurnCancelled {
		t.Errorf("state after stopping the starting input = %s, want cancelled", st)
	}
	if got := waitingTexts(h); !equal(got, []string{"two"}) || !episode(h) {
		t.Fatalf("after the stop: waiting %q, episode %v; want two queued in an episode", got, episode(h))
	}
	h.mu.Lock()
	h.pumping = false
	h.mu.Unlock()
	h.resumePump()
	f.reply(expectPrompt(t, f, "two"), map[string]any{"stopReason": "end_turn"})
	items, discards, ep := settled(t, h)
	if got := f.promptTexts(); !equal(got, []string{"two"}) {
		t.Errorf("prompts = %q, want the stopped one never sent", got)
	}
	if len(items) != 0 || len(discards) != 0 || ep {
		t.Errorf("items %v, discards %v, episode %v", items, discards, ep)
	}
	if st := h.currentState(); st != agents.TurnCompleted {
		t.Errorf("state = %s, want completed", st)
	}
}

// A prompt whose write fails because the child died goes back to the queue and reaches the
// respawned child: the ledger entry Take made does not turn it into a resend.
func TestPromptLostOnADeadChildIsRequeued(t *testing.T) {
	h, f := newTestHandle(t)
	f.writes.hold()
	mustSend(t, h, member("m1", "one"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		items := h.q.Items()
		h.mu.Unlock()
		if len(items) == 1 && items[0].State == agents.EntryCommitted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the entry never became committed: %+v", items)
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.toClient.Close() // the child dies with the prompt still unwritten
	h.mu.Lock()
	cl := h.cl
	h.mu.Unlock()
	for !cl.dead() {
		time.Sleep(5 * time.Millisecond)
	}
	f.writes.releaseWith(io.ErrClosedPipe)
	waitPumpIdle(t, h)
	if got := h.queuedPrompts(); !equal(got, []string{"one"}) {
		t.Fatalf("queue after the lost write = %q, want [one]", got)
	}

	cl2, f2 := newFakeACP(t)
	h.mu.Lock()
	h.cl, h.alive = cl2, true
	h.mu.Unlock()
	h.cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(h.cl, id, method, params)
	}
	h.cl.onNotify = h.onNotify
	h.resumePump()
	f2.reply(expectPrompt(t, f2, "one"), map[string]any{"stopReason": "end_turn"})
	if items, _, _ := settled(t, h); len(items) != 0 {
		t.Errorf("items %v", items)
	}
	if st := h.currentState(); st != agents.TurnCompleted {
		t.Errorf("state = %s, want completed", st)
	}
}

// LiveHandle never starts anything: no handle, no answer.
func TestLiveHandle(t *testing.T) {
	h, _ := newTestHandle(t)
	m := session.Meta{Name: h.name, Driver: session.DriverManaged}
	if got, ok := (managedDriver{}).LiveHandle(m); ok || got != nil {
		t.Fatalf("LiveHandle with no handle = %v, %v", got, ok)
	}
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	defer func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	}()
	if got, ok := (managedDriver{}).LiveHandle(m); !ok || got != agents.ThreadHandle(h) {
		t.Fatalf("LiveHandle = %v, %v, want the registered handle", got, ok)
	}
}
