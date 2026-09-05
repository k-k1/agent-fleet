package copilot

// Unit tests for the managed driver. The ACP server (what `copilot --acp` is) is faked over
// io.Pipe, covering the turn state machine (Send → completed, queueing while running,
// interrupt → cancelled, ledger idempotency) and the permission → Interaction → Respond
// round trip (answering a server-initiated JSON-RPC request).

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// fakeACP is a scriptable ACP peer on the other end of the client's pipes.
type fakeACP struct {
	toClient *io.PipeWriter // server → client stdout
	mu       sync.Mutex
	requests []struct {
		ID     int64
		Method string
		Params json.RawMessage
	}
	gotPrompt chan int64           // session/prompt request ids as they arrive
	gotCancel chan struct{}        // session/cancel notifications
	gotResp   chan json.RawMessage // responses to server-initiated requests
}

func newFakeACP(t *testing.T) (*acpClient, *fakeACP) {
	t.Helper()
	cIn, sOut := io.Pipe() // server writes → client reads
	sIn, cOut := io.Pipe() // client writes → server reads
	f := &fakeACP{toClient: sOut,
		gotPrompt: make(chan int64, 8),
		gotCancel: make(chan struct{}, 8),
		gotResp:   make(chan json.RawMessage, 8),
	}
	go f.serve(sIn)
	cl := newACPClient(cOut, cIn)
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
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.Method == "" && len(msg.ID) > 0 { // response to OUR request
			f.gotResp <- append(json.RawMessage(nil), sc.Bytes()...)
			continue
		}
		var id int64
		_ = json.Unmarshal(msg.ID, &id)
		switch msg.Method {
		case "session/prompt":
			f.gotPrompt <- id // held: test decides when/how to answer
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

func (f *fakeACP) reply(id int64, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_, _ = f.toClient.Write(append(b, '\n'))
}

// send emits a server-initiated request or notification to the client.
func (f *fakeACP) send(v any) {
	b, _ := json.Marshal(v)
	_, _ = f.toClient.Write(append(b, '\n'))
}

func newTestHandle(t *testing.T) (*threadHandle, *fakeACP) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // isolate where status.Persist / the ledger write
	cl, f := newFakeACP(t)
	h := &threadHandle{
		name: "s1", dir: t.TempDir(), slotSid: "slot-1", sid: "sess-1",
		cl: cl, alive: true, state: agents.TurnCompleted,
		events: make(chan agents.Event, 64),
	}
	h.cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(h.cl, id, method, params)
	}
	// t.Cleanup is LIFO, so this wait — registered after the t.Setenv("HOME", …) above —
	// runs before HOME is restored. Registered before it, it would run after, too late.
	t.Cleanup(func() { waitPumpIdle(t, h) })
	return h, f
}

// waitPumpIdle blocks until the handle's turn goroutine has drained (no queue, no running
// turn). The HOME isolation only holds if we wait: a test returning with a turn still
// running lets MarkTurnEnd → status.Persist run after `t.Setenv` restores HOME, writing into
// the real `~/.config/agent-fleet` (measured: slot-1.json was left in the user's
// session-status/). Failing is the safe direction — giving up on the wait would dirty the
// real environment, so it fails instead.
func waitPumpIdle(t *testing.T, h *threadHandle) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		busy := h.pumping || h.running
		h.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("a turn is still running after the test: restoring HOME now would write into the real ~/.config/agent-fleet")
}

func waitState(t *testing.T, h *threadHandle, want agents.TurnState) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if h.currentState() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state never reached %s (now %s)", want, h.currentState())
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
	select {
	case <-f.gotPrompt:
		t.Fatal("duplicate ClientMessageID must not start a turn")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSteerQueuesBehindRunning(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "one", ClientMessageID: "m1"})
	first := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	if err := h.Steer(agents.TurnInput{Prompt: "two", ClientMessageID: "m2"}); err != nil {
		t.Fatal(err)
	}
	if got := h.queuedPrompts(); len(got) != 1 || got[0] != "two" {
		t.Fatalf("queue wrong: %v", got)
	}
	f.reply(first, map[string]any{"stopReason": "end_turn"})
	second := <-f.gotPrompt // submitted as the next turn once the first completes
	f.reply(second, map[string]any{"stopReason": "end_turn"})
	waitState(t, h, agents.TurnCompleted)
}

func TestInterruptCancels(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "loop", ClientMessageID: "m1"})
	id := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	<-f.gotCancel
	f.reply(id, map[string]any{"stopReason": "cancelled"})
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
	f.toClient.Close() // the child process is lost
	waitState(t, h, agents.TurnUnknown)
}

func TestUpdateSettingsMode(t *testing.T) {
	h, _ := newTestHandle(t)
	if err := h.UpdateSettings(agents.ThreadSettings{Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	if snap, _ := h.Snapshot(); snap.Settings.Mode != "plan" {
		t.Fatalf("mode not applied: %+v", snap.Settings)
	}
	// Changing model/effort dynamically is an explicit error (the guard behind
	// DynamicModel:false).
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "gpt-5.4"}); err == nil {
		t.Fatal("dynamic model change must be rejected")
	}
}

func jsonContains(raw json.RawMessage, s string) bool {
	return json.Valid(raw) && strings.Contains(string(raw), s)
}
