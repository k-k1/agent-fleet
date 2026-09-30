package lcpp

// The two-stage stop on the in-process pump (ADR 0105, docs/log/128 §1.2). The engine is a
// gateClient: every round trip waits until the test answers it or the turn's context is
// cancelled, so each test can run the turns it started to the end and assert the state after
// the answers were processed.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

type gateClient struct {
	started chan string   // the newest user message of each round trip, as it begins
	proceed chan struct{} // one send answers one round trip
	// holdCancel, when set, keeps a cancelled round trip from returning until it is closed.
	holdCancel chan struct{}

	mu      sync.Mutex
	prompts []string
}

func newGateClient() *gateClient {
	return &gateClient{started: make(chan string, 8), proceed: make(chan struct{})}
}

func (c *gateClient) Send(ctx context.Context, messages []harness.Message, _ []harness.ToolDef) (harness.Turn, error) {
	p := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == harness.RoleUser {
			p = messages[i].Content
			break
		}
	}
	c.mu.Lock()
	c.prompts = append(c.prompts, p)
	c.mu.Unlock()
	c.started <- p
	select {
	case <-c.proceed:
		return harness.Turn{Content: "ok"}, nil
	case <-ctx.Done():
		if c.holdCancel != nil {
			<-c.holdCancel
		}
		return harness.Turn{}, ctx.Err()
	}
}

func (c *gateClient) InputTokens(context.Context, []harness.Message, []harness.ToolDef) (int, error) {
	return 0, nil
}

func (c *gateClient) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.prompts...)
}

// answer lets the running round trip complete.
func (c *gateClient) answer(t *testing.T) {
	t.Helper()
	select {
	case c.proceed <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("no round trip to answer")
	}
}

func expectStarted(t *testing.T, c *gateClient, want string) {
	t.Helper()
	select {
	case got := <-c.started:
		if got != want {
			t.Fatalf("the engine got %q, want %q (all: %q)", got, want, c.seen())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no round trip for %q", want)
	}
}

func expectNoStart(t *testing.T, c *gateClient) {
	t.Helper()
	select {
	case got := <-c.started:
		t.Errorf("an unexpected turn started: %q", got)
		c.answer(t) // let the pump drain before cleanup
	case <-time.After(300 * time.Millisecond):
	}
}

func member(id, text string) agents.TurnInput {
	return agents.TurnInput{Prompt: text, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginMember}}
}

func peer(id, text string) agents.TurnInput {
	return agents.TurnInput{Prompt: text, ClientMessageID: id, Origin: agents.Origin{Kind: agents.OriginPeer, From: "s-peer"}}
}

// startHandle resumes a fresh session wired to a gateClient.
func startHandle(t *testing.T, name string) (*threadHandle, *gateClient, session.Meta) {
	t.Helper()
	testHome(t)
	c := newGateClient()
	wireEngine(t, c)
	m := testMeta(t, name)
	h, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return h.(*threadHandle), c, m
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

// settled waits for the pump to finish and returns what the queue shows then.
func settled(t *testing.T, h *threadHandle) ([]agents.QueueItem, []agents.Discard, bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.mu.Lock()
		busy := h.pumping || h.running
		items, discards, ep := h.q.Items(), h.q.Discards(), h.q.Episode()
		h.mu.Unlock()
		if !busy {
			return items, discards, ep
		}
		if time.Now().After(deadline) {
			t.Fatal("the pump did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
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

// userRecords is the user prompts the store holds: a turn that never started left none.
func userRecords(t *testing.T, h *threadHandle) []string {
	t.Helper()
	recs, _, err := Open(h.sid).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	var out []string
	for _, r := range recs {
		if r.Kind == KindUser {
			out = append(out, r.Content)
		}
	}
	return out
}

func TestFirstStopContinuesQueueInOrder(t *testing.T) {
	h, c, _ := startHandle(t, "sess-q-first")
	mustSend(t, h, member("m1", "one"))
	expectStarted(t, c, "one")
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))

	if res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst); res.Discard != nil {
		t.Errorf("a first stop discarded %+v", res.Discard)
	}
	if !episode(h) {
		t.Error("a first stop that left input queued opened no stop episode")
	}
	expectStarted(t, c, "two")
	c.answer(t)
	expectStarted(t, c, "three")
	c.answer(t)

	items, discards, ep := settled(t, h)
	if got := c.seen(); !equal(got, []string{"one", "two", "three"}) {
		t.Errorf("engine round trips = %q, want one, two, three", got)
	}
	if len(items) != 0 || len(discards) != 0 || ep {
		t.Errorf("after the queue drained: items %v, discards %v, episode %v", items, discards, ep)
	}
	waitState(t, h, agents.TurnCompleted)
}

func TestSecondStopDiscardsRestIncludingPeer(t *testing.T) {
	h, c, m := startHandle(t, "sess-q-second")
	mustSend(t, h, member("m1", "one"))
	expectStarted(t, c, "one")
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))
	mustSend(t, h, member("m4", "four"))

	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	expectStarted(t, c, "two")
	res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopSecond)
	if res.Discard == nil || !equal(ids(res.Discard.Items), []string{"m3", "m4"}) {
		t.Fatalf("second stop discarded %+v, want m3 and m4", res.Discard)
	}
	if res.Discard.Reason != agents.DiscardSecondStop || res.Discard.Items[0].Origin.Kind != agents.OriginPeer {
		t.Errorf("discard = %+v, want reason second_stop with the peer origin kept", res.Discard)
	}
	expectNoStart(t, c)

	items, discards, ep := settled(t, h)
	if len(items) != 0 || ep {
		t.Errorf("after the second stop: items %v, episode %v", items, ep)
	}
	if len(discards) != 1 || discards[0].ID != res.Discard.ID {
		t.Fatalf("kept discards = %+v, want the one the stop returned", discards)
	}
	waitState(t, h, agents.TurnCancelled)
	if got := userRecords(t, h); !equal(got, []string{"one", "two"}) {
		t.Errorf("stored user turns = %q, want one and two only", got)
	}
	if !h.DismissDiscard(res.Discard.ID) || h.DismissDiscard(res.Discard.ID) {
		t.Error("DismissDiscard: want true once, then false")
	}
	if td, ok := (agentImpl{}).Transcript(m); !ok || len(td.Discards) != 0 {
		t.Errorf("messages payload after dismissal: ok %v, discards %+v", ok, td.Discards)
	}
}

func TestDiscardQueueOutsideEpisode(t *testing.T) {
	h, c, m := startHandle(t, "sess-q-discard")
	mustSend(t, h, member("m1", "one"))
	expectStarted(t, c, "one")
	mustSend(t, h, member("m2", "two"))
	mustSend(t, h, peer("m3", "three"))

	res := mustInterrupt(t, h, agents.InterruptOpts{DiscardQueue: true}, agents.StopDiscard)
	if res.Discard == nil || !equal(ids(res.Discard.Items), []string{"m2", "m3"}) || res.Discard.Reason != agents.DiscardQueue {
		t.Fatalf("discard = %+v, want m2 and m3 for discard_queue", res.Discard)
	}
	expectNoStart(t, c)
	items, discards, ep := settled(t, h)
	if len(items) != 0 || len(discards) != 1 || ep {
		t.Errorf("after discard_queue: items %v, discards %v, episode %v", items, discards, ep)
	}
	waitState(t, h, agents.TurnCancelled)
	td, ok := agentImpl{}.Transcript(m)
	if !ok || len(td.Discards) != 1 || len(td.Discards[0].Items) != 2 {
		t.Errorf("messages payload: ok %v, discards %+v", ok, td.Discards)
	}

	if res := mustInterrupt(t, h, agents.InterruptOpts{DiscardQueue: true}, agents.StopDiscard); res.Discard != nil {
		t.Errorf("an idle discard_queue returned %+v", res.Discard)
	}
}

func holdAtCommit(h *threadHandle) (taken <-chan struct{}, release func()) {
	tk, rel := make(chan struct{}, 1), make(chan struct{})
	h.beforeCommit = func() {
		tk <- struct{}{}
		<-rel
	}
	return tk, func() { close(rel) }
}

func TestTakenBeforeCommitIsNeverSent(t *testing.T) {
	t.Run("discard_queue", func(t *testing.T) {
		h, c, _ := startHandle(t, "sess-q-take-discard")
		taken, release := holdAtCommit(h)
		mustSend(t, h, member("m1", "one"))
		<-taken
		res := mustInterrupt(t, h, agents.InterruptOpts{DiscardQueue: true}, agents.StopDiscard)
		if res.Discard == nil || !equal(ids(res.Discard.Items), []string{"m1"}) {
			t.Fatalf("discard = %+v, want the taken m1", res.Discard)
		}
		release()
		expectNoStart(t, c)
		items, discards, _ := settled(t, h)
		if len(items) != 0 || len(discards) != 1 {
			t.Errorf("items %v, discards %v", items, discards)
		}
		waitState(t, h, agents.TurnCancelled)
		if got := userRecords(t, h); len(got) != 0 {
			t.Errorf("stored user turns = %q, want none", got)
		}
	})
	t.Run("first stop", func(t *testing.T) {
		h, c, _ := startHandle(t, "sess-q-take-first")
		taken, release := holdAtCommit(h)
		mustSend(t, h, member("m1", "one"))
		<-taken
		if res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst); res.Discard != nil {
			t.Errorf("first stop discarded %+v", res.Discard)
		}
		release()
		expectNoStart(t, c)
		items, discards, ep := settled(t, h)
		if len(items) != 0 || len(discards) != 0 || ep {
			t.Errorf("items %v, discards %v, episode %v", items, discards, ep)
		}
		waitState(t, h, agents.TurnCancelled)
	})
	t.Run("remove", func(t *testing.T) {
		h, c, _ := startHandle(t, "sess-q-take-remove")
		taken, release := holdAtCommit(h)
		mustSend(t, h, member("m1", "one"))
		<-taken
		it, err := h.RemoveQueued("m1")
		if err != nil || it.ID != "m1" || it.Text != "one" {
			t.Fatalf("RemoveQueued = %+v, %v", it, err)
		}
		release()
		expectNoStart(t, c)
		if items, _, _ := settled(t, h); len(items) != 0 {
			t.Errorf("items %v", items)
		}
		if got := userRecords(t, h); len(got) != 0 {
			t.Errorf("stored user turns = %q, want none", got)
		}
	})
}

func TestRemoveQueued(t *testing.T) {
	h, c, _ := startHandle(t, "sess-q-remove")
	mustSend(t, h, member("m1", "one"))
	expectStarted(t, c, "one")
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
	c.answer(t)
	expectStarted(t, c, "three")
	c.answer(t)
	items, discards, _ := settled(t, h)
	if got := c.seen(); !equal(got, []string{"one", "three"}) {
		t.Errorf("engine round trips = %q, want the removed two skipped", got)
	}
	if len(items) != 0 || len(discards) != 0 {
		t.Errorf("items %v, discards %v", items, discards)
	}
	waitState(t, h, agents.TurnCompleted)
}

func TestEpisodeEndsOnlyOnNewMemberInput(t *testing.T) {
	h, c, _ := startHandle(t, "sess-q-episode")
	mustSend(t, h, member("m1", "one"))
	expectStarted(t, c, "one")
	mustSend(t, h, member("m2", "two"))
	// Park the next take so the queue stays put while the episode is probed.
	taken, release := holdAtCommit(h)
	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	<-taken // m2 is taken; the stop episode is open

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
	h.beforeCommit = nil
	release()
	for _, want := range []string{"two", "three", "four"} {
		expectStarted(t, c, want)
		c.answer(t)
	}
	expectNoStart(t, c) // the resent two is dropped by the ledger when taken
	items, discards, ep := settled(t, h)
	if len(items) != 0 || len(discards) != 0 || ep {
		t.Errorf("items %v, discards %v, episode %v", items, discards, ep)
	}
}

// A stopped turn lands as cancelled even when input accepted after the stop has already moved
// the displayed state to queued: the verdict comes from the stop, not from that state.
func TestStoppedTurnLandsCancelledAfterASend(t *testing.T) {
	h, c, _ := startHandle(t, "sess-q-verdict")
	c.holdCancel = make(chan struct{})
	mustSend(t, h, member("m1", "one"))
	expectStarted(t, c, "one")
	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	mustSend(t, h, member("m2", "two")) // accept sets TurnQueued
	var seen []agents.TurnState
	for len(h.events) > 0 { // what happened before the stopped turn returns is not the verdict
		<-h.events
	}
	close(c.holdCancel)
	expectStarted(t, c, "two")
	for len(h.events) > 0 {
		seen = append(seen, (<-h.events).TurnState)
	}
	c.answer(t)
	settled(t, h)
	var verdict agents.TurnState
	for _, st := range seen {
		switch st {
		case agents.TurnCancelled, agents.TurnFailed, agents.TurnCompleted, agents.TurnAborted:
			verdict = st
		}
		if verdict != "" {
			break
		}
	}
	if verdict != agents.TurnCancelled {
		t.Errorf("the stopped turn landed as %q (events %v), want cancelled", verdict, seen)
	}
}

var _ agents.LiveHandles = managedDriver{}

// Input accepted while nothing runs is the turn being started even before the pump takes it: a
// first stop in that window stops it, and what was queued behind it continues.
func TestFirstStopBeforeThePumpTakes(t *testing.T) {
	h, c, _ := startHandle(t, "sess-q-window")
	h.mu.Lock()
	h.pumping = true // hold the pump off: the window between accept and Take
	h.mu.Unlock()
	mustSend(t, h, member("m1", "one"))
	mustSend(t, h, member("m2", "two"))
	if res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst); res.Discard != nil {
		t.Errorf("first stop discarded %+v", res.Discard)
	}
	if snap, _ := h.Snapshot(); snap.TurnState != agents.TurnCancelled {
		t.Errorf("state after stopping the starting input = %s, want cancelled", snap.TurnState)
	}
	if n := queueLen(h); !episode(h) || n != 1 {
		t.Fatalf("after the stop: episode %v, queued %d; want two queued in an episode", episode(h), n)
	}
	go h.pump() // pumping is still true: this is the pump accept would have started
	expectStarted(t, c, "two")
	c.answer(t)
	items, discards, ep := settled(t, h)
	if got := c.seen(); !equal(got, []string{"two"}) {
		t.Errorf("engine round trips = %q, want the stopped one never run", got)
	}
	if len(items) != 0 || len(discards) != 0 || ep {
		t.Errorf("items %v, discards %v, episode %v", items, discards, ep)
	}
	if got := userRecords(t, h); !equal(got, []string{"two"}) {
		t.Errorf("stored user turns = %q, want two only", got)
	}
	waitState(t, h, agents.TurnCompleted)
}

// LiveHandle never starts anything: no handle, no answer.
func TestLiveHandle(t *testing.T) {
	testHome(t)
	m := testMeta(t, "sess-q-live")
	if got, ok := (managedDriver{}).LiveHandle(m); ok || got != nil {
		t.Fatalf("LiveHandle before Resume = %v, %v", got, ok)
	}
	if handleFor(m.Name) != nil {
		t.Fatal("LiveHandle created a handle")
	}
	h, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got, ok := (managedDriver{}).LiveHandle(m); !ok || got != h {
		t.Fatalf("LiveHandle = %v, %v, want the resumed handle", got, ok)
	}
}
