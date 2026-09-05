package kiro

// Unit tests for the managed driver. The ACP server (what kiro-cli acp provides) is faked
// over io.Pipe, and the tests cover the turn state machine (Send to completed, queueing while
// running, interrupt to cancelled, ledger idempotency), the permission -> Interaction ->
// Respond round trip, and building the in-memory transcript from session/update
// (agent_message_chunk / tool_call / tool_call_update). Same shape as cursor's
// driver_test.go, plus what is specific to kiro: every Dynamic* is false, so settings changes
// are refused, and the mode/lock mappings are pure functions.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeACP is a scriptable ACP peer on the other end of the client's pipes.
type fakeACP struct {
	toClient  *io.PipeWriter
	gotPrompt chan int64
	gotCancel chan struct{}
	gotResp   chan json.RawMessage

	mu       sync.Mutex
	loadBusy int // reply session/load with a lock-busy error this many more times, then succeed
}

func newFakeACP(t *testing.T) (*acpClient, *fakeACP) {
	t.Helper()
	cIn, sOut := io.Pipe()
	sIn, cOut := io.Pipe()
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
			f.gotPrompt <- id
		case "session/cancel":
			f.gotCancel <- struct{}{}
		case "session/set_mode":
			f.reply(id, map[string]any{})
		case "session/load":
			f.mu.Lock()
			busy := f.loadBusy > 0
			if busy {
				f.loadBusy--
			}
			f.mu.Unlock()
			if busy {
				f.replyErr(id, -32603, "Internal error", `"Failed to start session: Session is active in another process (PID 1)"`)
			} else {
				f.reply(id, map[string]any{"modes": map[string]any{"currentModeId": "kiro_default"}})
			}
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

// replyErr emits a JSON-RPC error (data is a raw JSON fragment, e.g. a quoted string).
func (f *fakeACP) replyErr(id int64, code int, msg, data string) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg, "data": json.RawMessage(data)}})
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

func newTestHandle(t *testing.T) (*threadHandle, *fakeACP) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cl, f := newFakeACP(t)
	h := &threadHandle{
		name: "s1", dir: t.TempDir(), slotSid: "slot-1", sid: "sess-1",
		cl: cl, alive: true, state: agents.TurnCompleted,
		events: make(chan agents.Event, 64),
	}
	h.cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(h.cl, id, method, params)
	}
	// t.Cleanup is LIFO, so this wait, pushed after the t.Setenv("HOME", ...) above, runs
	// before HOME is restored. Pushed before it, it would run after — too late.
	t.Cleanup(func() { waitPumpIdle(t, h) })
	h.cl.onNotify = h.onNotify
	return h, f
}

// waitPumpIdle blocks until the handle's turn goroutine has drained: nothing queued and no
// turn running. The HOME isolation only holds once that wait completes. If a test returns
// with a turn still running, MarkTurnEnd -> status.Persist runs after `t.Setenv` restores
// HOME and writes to the real `~/.config/agent-fleet` (measured: a slot-1.json was left in a
// user's session-status/). Failing is the safe direction, so leaving without the drain is
// reported as a failure rather than allowed to pollute the real environment.
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
	t.Error("a turn is still running after the test finished: restoring HOME now writes to the real ~/.config/agent-fleet")
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
	// Ledger: resending the same ClientMessageID is a no-op and starts no new turn.
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
	second := <-f.gotPrompt
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
	if err := h.Send(agents.TurnInput{Prompt: "x", ClientMessageID: "m2"}); err != agents.ErrQuestionPending {
		t.Fatalf("want ErrQuestionPending, got %v", err)
	}
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
}

func TestRuntimeLostFailsInFlight(t *testing.T) {
	h, f := newTestHandle(t)
	_ = h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "m1"})
	<-f.gotPrompt
	waitState(t, h, agents.TurnRunning)
	f.toClient.Close()
	waitState(t, h, agents.TurnUnknown)
}

// Unlike cursor, kiro accepts no live change of mode or model: every Dynamic* is false and
// the registry offers no UI for it. Pins that an explicit error is returned defensively.
func TestUpdateSettingsRejectsDynamic(t *testing.T) {
	h, _ := newTestHandle(t)
	for _, s := range []agents.ThreadSettings{{Mode: "plan"}, {Model: "claude-sonnet-4.5"}, {Effort: "high"}} {
		if err := h.UpdateSettings(s); err == nil {
			t.Fatalf("dynamic change must be rejected: %+v", s)
		}
	}
	// An empty update changes nothing and succeeds as a no-op.
	if err := h.UpdateSettings(agents.ThreadSettings{}); err != nil {
		t.Fatalf("empty update should be a no-op, got %v", err)
	}
}

// TestManagedTranscriptFromUpdates checks that the user/assistant/tool transcript is built
// correctly from session/update notifications alone: a live handle returns its buf and never
// falls through to fileTranscript.
func TestManagedTranscriptFromUpdates(t *testing.T) {
	h, f := newTestHandle(t)
	handlesMu.Lock()
	handles["s1"] = h
	handlesMu.Unlock()
	t.Cleanup(func() { handlesMu.Lock(); delete(handles, "s1"); handlesMu.Unlock() })

	_ = h.Send(agents.TurnInput{Prompt: "run echo hi and report", ClientMessageID: "m1"})
	id := <-f.gotPrompt
	waitState(t, h, agents.TurnRunning)

	// stream: thought, assistant text (token by token), tool_call, tool output, final text
	f.update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Thinking"}})
	f.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "I'll "}})
	f.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "run it."}})
	f.update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tc1", "title": "`echo hi`", "kind": "execute", "rawInput": map[string]any{"command": "echo hi"}})
	f.update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tc1", "status": "completed", "rawOutput": map[string]any{"exitCode": 0, "stdout": "hi\n"}})
	f.update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Done"}})

	// give the readLoop time to drain notifications before completing the turn
	time.Sleep(100 * time.Millisecond)
	f.reply(id, map[string]any{"stopReason": "end_turn"})
	waitState(t, h, agents.TurnCompleted)

	td := managedTranscript(session.Meta{Name: "s1"})
	if len(td.Turns) != 2 {
		t.Fatalf("want 2 turns (user+assistant), got %d: %+v", len(td.Turns), td.Turns)
	}
	if td.Turns[0].Role != "user" || td.Turns[0].Text != "run echo hi and report" {
		t.Fatalf("user turn wrong: %+v", td.Turns[0])
	}
	a := td.Turns[1]
	if a.Role != "assistant" {
		t.Fatalf("want assistant turn, got %s", a.Role)
	}
	if a.Text != "I'll run it.\n\nDone" {
		t.Fatalf("coalesced assistant text wrong: %q", a.Text)
	}
	if td.Turns[0].Idx >= td.Turns[1].Idx {
		t.Fatalf("Idx not monotonic: %d, %d", td.Turns[0].Idx, td.Turns[1].Idx)
	}
	var sawThought, sawTool bool
	for _, p := range a.Parts {
		switch p.Kind {
		case "thinking":
			sawThought = p.Text == "Thinking"
		case "tool":
			if p.Tool == "`echo hi`" && strings.Contains(p.Output, "hi") {
				sawTool = true
			}
		}
	}
	if !sawThought || !sawTool {
		t.Fatalf("thought/tool part missing: %+v", a.Parts)
	}
}

// meta emits a _kiro.dev/metadata notification.
func (f *fakeACP) meta(params map[string]any) {
	f.send(map[string]any{"jsonrpc": "2.0", "method": "_kiro.dev/metadata", "params": params})
}

// TestManagedContextFromMetadata checks that contextUsagePercentage from _kiro.dev/metadata
// is kept as the latest value (including when it shrinks), that meteringUsage credits
// accumulate, and that ManagedContext / ContextFill round-trip the pct-to-token conversion
// exactly (Track D).
func TestManagedContextFromMetadata(t *testing.T) {
	h, f := newTestHandle(t)
	handlesMu.Lock()
	handles["s1"] = h
	handlesMu.Unlock()
	t.Cleanup(func() { handlesMu.Lock(); delete(handles, "s1"); handlesMu.Unlock() })

	// window is normally filled from ModelWindow at spawn; a unit test does not go through
	// spawn, so inject it directly.
	h.usageMu.Lock()
	h.ctxWindow = 200_000
	h.usageMu.Unlock()

	// No metadata received yet: ok=false, so no context bar is drawn.
	if _, _, _, _, ok := ManagedContext("s1"); ok {
		t.Fatalf("no metadata yet must be ok=false")
	}

	f.meta(map[string]any{"contextUsagePercentage": 3.39})
	f.meta(map[string]any{
		"contextUsagePercentage": 1.25,
		"meteringUsage":          []any{map[string]any{"value": 0.02, "unit": "credit"}},
	})
	f.meta(map[string]any{ // credits only, pct unchanged, credits accumulate
		"meteringUsage": []any{map[string]any{"value": 0.03, "unit": "credit"}},
	})
	// Wait for readLoop to drain.
	deadline := time.Now().Add(2 * time.Second)
	var pct, credits float64
	var window int
	var ok bool
	for time.Now().Before(deadline) {
		pct, window, credits, _, ok = ManagedContext("s1")
		if ok && credits > 0.049 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("ManagedContext ok=false after metadata")
	}
	if pct != 1.25 { // the latest value: 3.39 -> 1.25, a shrink is reflected
		t.Errorf("pct = %v, want latest 1.25", pct)
	}
	if window != 200_000 {
		t.Errorf("window = %d, want 200000", window)
	}
	if credits < 0.049 || credits > 0.051 { // 0.02 + 0.03 accumulated
		t.Errorf("credits = %v, want ~0.05", credits)
	}

	// ContextFill: pct(1.25%) x window(200k) -> tokens, an exact round trip with window given.
	c := (agentImpl{}).ContextFill(session.Meta{Name: "s1"})
	if c == nil {
		t.Fatalf("ContextFill returned nil with live metadata")
	}
	if c.Window != 200_000 {
		t.Errorf("ContextFill window = %d, want 200000", c.Window)
	}
	wantTok := int(1.25 / 100 * 200_000) // 2500
	if c.Tokens != wantTok {
		t.Errorf("ContextFill tokens = %d, want %d", c.Tokens, wantTok)
	}
	// The frontend recomputes pct from tokens/window, and must land on the original pct
	// within rounding.
	back := float64(c.Tokens) / float64(c.Window) * 100
	if back < 1.24 || back > 1.26 {
		t.Errorf("pct round-trip = %v, want ~1.25", back)
	}

	// A stopped handle is ok=false: no context is shown for the TUI route or while stopped.
	h.mu.Lock()
	h.alive = false
	h.mu.Unlock()
	if _, _, _, _, ok := ManagedContext("s1"); ok {
		t.Errorf("stopped handle must be ok=false")
	}
	if (agentImpl{}).ContextFill(session.Meta{Name: "s1"}) != nil {
		t.Errorf("ContextFill must be nil for a stopped handle")
	}
}

func TestModeMapping(t *testing.T) {
	if acpModeID("plan") != "kiro_planner" || acpModeID("normal") != "kiro_default" || acpModeID("") != "kiro_default" {
		t.Fatal("acpModeID wrong")
	}
	cases := map[string]string{"kiro_planner": "plan", "kiro_default": "normal", "kiro_guide": "normal", "": ""}
	for in, want := range cases {
		if got := modeFromACP(in); got != want {
			t.Fatalf("modeFromACP(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsLockBusy(t *testing.T) {
	busy := &rpcError{Code: -32603, Message: "Internal error",
		Data: json.RawMessage(`"Failed to start session: Session is active in another process (PID 14561)"`)}
	if !isLockBusy(busy) {
		t.Fatalf("lock-busy error not detected: %v", busy)
	}
	// wrapped rpcError is still detected (errors.As unwraps).
	if !isLockBusy(fmt.Errorf("load failed: %w", busy)) {
		t.Fatal("wrapped lock-busy error not detected")
	}
	// same message but a DIFFERENT code must NOT read as lock-busy (code is ANDed).
	wrongCode := &rpcError{Code: -32000, Message: "x",
		Data: json.RawMessage(`"Session is active in another process (PID 1)"`)}
	if isLockBusy(wrongCode) {
		t.Fatal("wrong code must not read as lock-busy")
	}
	// -32603 but an unrelated message must NOT match.
	if isLockBusy(&rpcError{Code: -32603, Message: "Internal error", Data: json.RawMessage(`"No session found with id"`)}) {
		t.Fatal("unrelated -32603 must not read as lock-busy")
	}
	// a plain (non-rpcError) error is never lock-busy — the fail-safe default (A2-1).
	if isLockBusy(errors.New("active in another process")) {
		t.Fatal("plain error must not read as lock-busy (fail-safe)")
	}
	if isLockBusy(nil) {
		t.Fatal("nil is not lock-busy")
	}
}

func TestLoadWithLockRetryExhausts(t *testing.T) {
	oldA, oldD := lockRetryAttempts, lockRetryDelay
	lockRetryAttempts, lockRetryDelay = 3, time.Millisecond
	t.Cleanup(func() { lockRetryAttempts, lockRetryDelay = oldA, oldD })

	h, f := newTestHandle(t)
	f.mu.Lock()
	f.loadBusy = 100 // always lock-busy
	f.mu.Unlock()
	_, err := h.loadWithLockRetry(h.cl, "sess-1")
	if err == nil || !isLockBusy(err) {
		t.Fatalf("exhausted retry must return the lock-busy error, got %v", err)
	}
}

func TestLoadWithLockRetrySucceedsAfterLockClears(t *testing.T) {
	oldA, oldD := lockRetryAttempts, lockRetryDelay
	lockRetryAttempts, lockRetryDelay = 6, time.Millisecond
	t.Cleanup(func() { lockRetryAttempts, lockRetryDelay = oldA, oldD })

	h, f := newTestHandle(t)
	f.mu.Lock()
	f.loadBusy = 2 // lock releases after two rejects
	f.mu.Unlock()
	res, err := h.loadWithLockRetry(h.cl, "sess-1")
	if err != nil {
		t.Fatalf("load should succeed once the lock clears, got %v", err)
	}
	if currentModeOf(res) != "kiro_default" {
		t.Fatalf("load result not parsed: %s", res)
	}
}

func jsonContains(raw json.RawMessage, s string) bool {
	return json.Valid(raw) && strings.Contains(string(raw), s)
}
