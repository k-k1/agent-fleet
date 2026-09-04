package cursor

// managed driver のユニットテスト。ACP サーバー（cursor-agent acp 相当）を io.Pipe 上の
// フェイクで模し、turn 状態機械（Send→completed / 実行中 queue / interrupt→cancelled /
// 台帳の冪等化）と permission→Interaction→Respond の往復、そして cursor 固有の
// session/update → 転写メモリ構築（agent_message_chunk / tool_call / tool_call_update）
// を検証する。

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
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
	// t.Cleanup は LIFO なので、上の t.Setenv("HOME", …) より後に積んだこの待ちが
	// HOME 復帰より先に走る（前に積むと復帰の後＝手遅れ）。
	t.Cleanup(func() { waitPumpIdle(t, h) })
	h.cl.onNotify = h.onNotify
	return h, f
}

// waitPumpIdle blocks until the handle's turn goroutine has drained（キューも走行中の
// turn も無い状態）。**HOME の隔離は待って初めて成立する**: テストが turn を走らせたまま
// 返ると、`t.Setenv` の復帰後に MarkTurnEnd → status.Persist が走り、書き先が
// 実 `~/.config/agent-fleet` になる（実測: 利用者の session-status/ に slot-1.json が
// 残っていた）。落ちる方向は安全側 — 待てないまま抜けると実環境を汚すので失敗させる。
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
	t.Error("turn がテスト終了後も走っている: このまま HOME を戻すと実 ~/.config/agent-fleet へ書く")
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
	// 台帳: 同じ ClientMessageID の再送は no-op（新しい turn を始めない）。
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

func TestUpdateSettingsMode(t *testing.T) {
	h, _ := newTestHandle(t)
	if err := h.UpdateSettings(agents.ThreadSettings{Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	if snap, _ := h.Snapshot(); snap.Settings.Mode != "plan" {
		t.Fatalf("mode not applied: %+v", snap.Settings)
	}
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "gpt-5.4"}); err == nil {
		t.Fatal("dynamic model change must be rejected")
	}
}

// TestManagedTranscriptFromUpdates は cursor 固有の核心: ACP はローカル転写を書かないので、
// session/update 通知だけから user/assistant/tool の転写が正しく組み上がることを検証する。
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
	// Idx は単調増加
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

func jsonContains(raw json.RawMessage, s string) bool {
	return json.Valid(raw) && strings.Contains(string(raw), s)
}
