package acp

// Unit tests for the shared managed driver. The ACP server is stood in for by a fake over
// io.Pipe, and the tests cover the turn state machine (Send -> completed, queueing while
// running, interrupt -> cancelled, ledger idempotency) and the permission -> Interaction ->
// Respond round trip. The kinds' own tests cover what each kind wires in (its notifications,
// its transcript, its settings).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// fakeACP is a scriptable ACP peer on the other end of the client's pipes.
type fakeACP struct {
	writes    *gatedWriter // the client's stdin, which a test can hold to keep a write in flight
	toClient  *io.PipeWriter
	mu        sync.Mutex
	prompts   []string // text of each session/prompt, in arrival order
	gotPrompt chan int64
	gotCancel chan struct{}
	gotResp   chan json.RawMessage
}

func newFakeACP(t *testing.T) (*Client, *fakeACP) {
	t.Helper()
	cIn, sOut := io.Pipe()
	sIn, cOut := io.Pipe()
	f := &fakeACP{toClient: sOut,
		gotPrompt: make(chan int64, 8),
		gotCancel: make(chan struct{}, 8),
		gotResp:   make(chan json.RawMessage, 8),
	}
	go f.serve(sIn)
	gw := &gatedWriter{w: cOut}
	f.writes = gw
	cl := testKind.NewClient(gw, cIn)
	t.Cleanup(func() { sOut.Close(); cOut.Close() })
	return cl, f
}

func (f *fakeACP) serve(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.Method == "" && len(msg.ID) > 0 {
			f.gotResp <- append(json.RawMessage(nil), sc.Bytes()...)
			continue
		}
		var id int64
		_ = json.Unmarshal(msg.ID, &id)
		switch msg.Method {
		case "session/prompt":
			var p struct {
				Prompt []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			text := ""
			for _, b := range p.Prompt {
				if b.Type == "text" {
					text = b.Text
					break
				}
			}
			f.mu.Lock()
			f.prompts = append(f.prompts, text)
			f.mu.Unlock()
			f.gotPrompt <- id
		case "session/cancel":
			f.gotCancel <- struct{}{}
		case "session/set_mode":
			f.reply(id, map[string]any{})
		default:
			if len(msg.ID) > 0 {
				f.reply(id, map[string]any{})
			}
		}
	}
}

// promptTexts is the text of each session/prompt received, in order.
func (f *fakeACP) promptTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

func (f *fakeACP) reply(id int64, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_, _ = f.toClient.Write(append(b, '\n'))
}

// send emits a server-initiated request or notification to the client.
func (f *fakeACP) send(v any) {
	b, _ := json.Marshal(v)
	_, _ = f.toClient.Write(append(b, '\n'))
}

// update emits a session/update notification with the given update object.
func (f *fakeACP) update(u any) {
	f.send(map[string]any{"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{"sessionId": "sess-1", "update": u}})
}

func newTestHandle(t *testing.T) (*Handle, *fakeACP) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cl, f := newFakeACP(t)
	h := NewHandle(testKind, Hooks{}, "s1", t.TempDir(), "slot-1")
	h.sid, h.cl, h.alive, h.state = "sess-1", cl, true, agents.TurnCompleted
	h.cl.OnRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(h.cl, id, method, params)
	}
	// t.Cleanup is LIFO, so this wait, pushed after the t.Setenv("HOME", …) above, runs before
	// HOME is restored (pushed before it, it would run after the restore, which is too late).
	t.Cleanup(func() { endTurnsBeforeHomeRestore(t, h) })
	return h, f
}

// waitPumpIdle blocks until the handle's turn goroutine has drained (no queue and no running
// turn). Isolating HOME only holds once this wait has happened: if a test returns with a turn
// still running, MarkTurnEnd -> status.Persist runs after `t.Setenv` has restored HOME and
// writes to the real `~/.config/agent-fleet` (measured: a slot-1.json was left in the user's
// session-status/). Failing is the safe direction — leaving without the wait pollutes the
// real environment, so it is reported as a failure.
func waitPumpIdle(t *testing.T, h *Handle) {
	t.Helper()
	if !pumpIdleWithin(h, 30*time.Second) {
		t.Error("a turn is still running after the test ended: restoring HOME now would write to the real ~/.config/agent-fleet")
	}
}

func pumpIdleWithin(h *Handle, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		busy := h.pumping || h.running
		h.mu.Unlock()
		if !busy {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// endTurnsBeforeHomeRestore is the handle's cleanup. A test that failed with a turn still held
// by the fake (a mutation run, a real regression) would otherwise leave that turn to end after
// HOME is restored — measured: slot-1 completion-key and session-status files landed in the
// real ~/.local/state/agent-fleet. So it ends the turn itself by closing the client, while HOME
// is still the test's.
func endTurnsBeforeHomeRestore(t *testing.T, h *Handle) {
	t.Helper()
	if pumpIdleWithin(h, 10*time.Second) {
		return
	}
	t.Error("a turn is still running after the test: ending it by closing the client")
	h.mu.Lock()
	cl := h.cl
	h.mu.Unlock()
	if cl != nil {
		cl.MarkClosed()
	}
	if !pumpIdleWithin(h, 10*time.Second) {
		t.Error("a turn is still running after the test ended: restoring HOME now would write to the real ~/.config/agent-fleet")
	}
}

func waitState(t *testing.T, h *Handle, want agents.TurnState) {
	t.Helper()
	for deadline := time.Now().Add(hangGuard); time.Now().Before(deadline); {
		if h.TurnState() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state never reached %s (now %s)", want, h.TurnState())
}

func TestSendCompletesTurn(t *testing.T) {
	h, f := newTestHandle(t)
	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "m1"}); err != nil {
		t.Fatal(err)
	}
	id := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	f.reply(id, map[string]any{"stopReason": "end_turn"})
	waitState(t, h, agents.TurnCompleted)
	// Ledger: resending the same ClientMessageID is a no-op (it starts no new turn).
	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "m1"}); err != nil {
		t.Fatal(err)
	}
	expectNoPrompt(t, h, f) // a duplicate ClientMessageID must not start a turn
}

func TestSteerQueuesBehindRunning(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "one", ClientMessageID: "m1"})
	first := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	if err := h.Steer(agents.TurnInput{Prompt: "two", ClientMessageID: "m2"}); err != nil {
		t.Fatal(err)
	}
	if got := waitingTexts(h); len(got) != 1 || got[0] != "two" {
		t.Fatalf("queue wrong: %v", got)
	}
	f.reply(first, map[string]any{"stopReason": "end_turn"})
	second := <-f.gotPrompt
	f.reply(second, map[string]any{"stopReason": "end_turn"})
	waitState(t, h, agents.TurnCompleted)
}

func TestInterruptCancels(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "loop", ClientMessageID: "m1"})
	id := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	if _, err := h.Interrupt(agents.InterruptOpts{}); err != nil {
		t.Fatal(err)
	}
	<-f.gotCancel
	f.reply(id, map[string]any{"stopReason": "cancelled"})
	waitState(t, h, agents.TurnCancelled)
}

// A peer message queued behind a stuck turn is what the stop is pressed to free: it starts as
// the next turn. The member's own queued follow-up continues too (ADR 0105 decision 1), and a
// second stop discards it and hands it back (decisions 2 and 4).
func TestStopFreesPeerInputAndKeepsOwnForSecondStop(t *testing.T) {
	h, f := newTestHandle(t)
	queued, err := h.SendQueued(member("m1", "stuck"))
	if err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("input to an idle session was reported as queued")
	}
	first := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	queued, err = h.SendQueued(peer("m2", "from a peer"))
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("input behind a running turn was not reported as queued")
	}
	if err := h.Steer(member("m3", "own follow-up")); err != nil {
		t.Fatal(err)
	}
	mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopFirst)
	<-f.gotCancel
	f.reply(first, map[string]any{"stopReason": "cancelled"})
	var second int64
	select {
	case second = <-f.gotPrompt:
	case <-time.After(hangGuard):
		t.Fatal("the peer message did not start a turn after the stop")
	}
	// The continued turn is held by the fake, so the pump is parked in it: the own follow-up must
	// still be queued behind it.
	if got := waitingTexts(h); !equal(got, []string{"own follow-up"}) {
		t.Errorf("queue after the continued turn started = %v, want the own follow-up still queued", got)
	}
	if got := f.promptTexts(); len(got) != 2 || got[1] != "from a peer" {
		t.Errorf("prompts = %q, want the peer message as the turn after the stop", got)
	}
	res := mustInterrupt(t, h, agents.InterruptOpts{}, agents.StopSecond)
	if res.Discard == nil || !equal(ids(res.Discard.Items), []string{"m3"}) {
		t.Fatalf("second stop discarded %+v, want the own follow-up", res.Discard)
	}
	<-f.gotCancel
	f.reply(second, map[string]any{"stopReason": "cancelled"})
	expectNoPrompt(t, h, f)
	_, discards, _ := settled(t, h)
	if len(discards) != 1 || discards[0].Items[0].Text != "own follow-up" {
		t.Errorf("kept discards = %+v, want the own follow-up returned", discards)
	}
	waitState(t, h, agents.TurnCancelled)
}

// Agent shutdown interrupts through the teardown path (AbortManaged → Abort): anything queued
// would otherwise start on the way down.
func TestAbortDiscardsQueuedInput(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "long", ClientMessageID: "m1"})
	id := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	_ = h.Send(peer("m2", "from a peer"))
	h.Abort()
	<-f.gotCancel
	// The cancelled turn is still held by the fake, so the pump cannot have taken anything yet.
	if got := waitingTexts(h); len(got) != 0 {
		t.Errorf("queue after shutdown interrupt = %v", got)
	}
	f.reply(id, map[string]any{"stopReason": "cancelled"})
	expectNoPrompt(t, h, f)
	items, discards, ep := settled(t, h)
	if len(items) != 0 || len(discards) != 0 || ep {
		t.Errorf("after shutdown: items %v, discards %v, episode %v (teardown keeps nothing)", items, discards, ep)
	}
	waitState(t, h, agents.TurnCancelled)
}

func TestPermissionInteractionRoundTrip(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "run something", ClientMessageID: "m1"})
	id := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	// Server-initiated request, in the shape measured — its id lives in a space
	// independent of the turn's.
	f.send(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "session/request_permission", "params": map[string]any{
		"sessionId": "sess-1",
		"toolCall": map[string]any{"toolCallId": "t9", "title": "Run echo", "kind": "execute",
			"rawInput": map[string]any{"command": "echo hi"}},
		"options": []map[string]any{
			{"optionId": "allow_once", "kind": "allow_once", "name": "Allow once"},
			{"optionId": "allow_always", "kind": "allow_always", "name": "Always allow"},
			{"optionId": "reject_once", "kind": "reject_once", "name": "Deny"},
		},
	}})
	waitState(t, h, agents.TurnWaitingInteraction)
	snap, _ := h.Snapshot()
	if snap.Interaction == nil || snap.Interaction.ID != "t9" ||
		len(snap.Interaction.Questions) != 1 || len(snap.Interaction.Questions[0].Options) != 3 {
		t.Fatalf("interaction wrong: %+v", snap.Interaction)
	}
	// Free text while a question is pending is refused (question_pending).
	if err := h.Send(agents.TurnInput{Prompt: "x", ClientMessageID: "m2"}); err != agents.ErrQuestionPending {
		t.Fatalf("want ErrQuestionPending, got %v", err)
	}
	// Answer through the option index → optionId conversion.
	if err := h.Respond(agents.InteractionReply{ID: "t9", Decision: agents.DecisionAnswer,
		Answers: []agents.InteractionAnswer{{Options: []int{1}}}}); err != nil {
		t.Fatal(err)
	}
	raw := <-f.gotResp
	if !jsonContains(raw, "allow_always") {
		t.Fatalf("response missing optionId: %s", raw)
	}
	waitState(t, h, agents.TurnRunning)
	f.reply(id, map[string]any{"stopReason": "end_turn"})
	waitState(t, h, agents.TurnCompleted)
	if snap, _ := h.Snapshot(); snap.Interaction != nil {
		t.Fatalf("interaction not cleared: %+v", snap.Interaction)
	}
}

func TestRuntimeLostFailsInFlight(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "m1"})
	<-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	f.toClient.Close()
	waitState(t, h, agents.TurnUnknown)
}

func jsonContains(raw json.RawMessage, s string) bool {
	return json.Valid(raw) && strings.Contains(string(raw), s)
}

// testKind is a kind with the stream-built state (StreamState) and a dynamic mode, the superset
// of what copilot, cursor and kiro wire in. Nothing here spawns a child.
var testKind = &Kind{
	Name:             "test",
	StreamState:      true,
	DynamicMode:      true,
	ExitRecordWait:   5 * time.Second,
	LoadFailed:       "test セッションを読み込めませんでした",
	SettingsRejected: "test はモデルの稼働中変更に未対応です",
	Ledger:           agents.NewMsgLedger("acp-test-msgledger"),
	Sids:             agents.NewSidStore("acp-test-sid"),
	NewClient: func(stdin io.Writer, stdout io.Reader) *Client {
		return NewClient(Config{Runtime: "test"}, stdin, stdout)
	},
	StopChild:   func(*exec.Cmd, io.Closer) {},
	ModeID:      func(mode string) string { return mode },
	ModeFromACP: func(id string) string { return id },
	NormalizeMsgID: func(id string) string {
		if id != "" {
			return id
		}
		return fmt.Sprintf("af-%d", time.Now().UnixNano())
	},
	PermissionDetail: func(raw json.RawMessage) (string, bool) {
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(raw, &in)
		return in.Command, true
	},
}

// payload is what the messages payload gets from the handle's queue (the kinds' read layer
// goes through Enrich).
func payload(h *Handle) agents.TranscriptData {
	var td agents.TranscriptData
	h.Enrich(&td)
	return td
}
