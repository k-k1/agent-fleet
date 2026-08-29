package copilot

// managed driver のユニットテスト。ACP サーバー（copilot --acp 相当）を
// io.Pipe 上のフェイクで模し、turn 状態機械（Send→completed / 実行中 queue /
// interrupt→cancelled / 台帳の冪等化）と permission→Interaction→Respond の
// 往復（JSON-RPC サーバー発リクエストへの応答）を検証する。

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
	t.Setenv("HOME", t.TempDir()) // status.Persist / ledger の書き先を隔離
	cl, f := newFakeACP(t)
	h := &threadHandle{
		name: "s1", dir: t.TempDir(), slotSid: "slot-1", sid: "sess-1",
		cl: cl, alive: true, state: agents.TurnCompleted,
		events: make(chan agents.Event, 64),
	}
	h.cl.onRequest = func(id json.RawMessage, method string, params json.RawMessage) {
		h.onServerRequest(h.cl, id, method, params)
	}
	return h, f
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
	second := <-f.gotPrompt // 完走後に次 turn として投入される
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
	// サーバー発 request（実測の形 — id は turn とは独立の空間）。
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
	// 質問待ち中の自由文はガード（question_pending — 69fde0b の教訓）。
	if err := h.Send(agents.TurnInput{Prompt: "x", ClientMessageID: "m2"}); err != agents.ErrQuestionPending {
		t.Fatalf("want ErrQuestionPending, got %v", err)
	}
	// 選択肢 index → optionId 変換で応答。
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
	f.toClient.Close() // 子プロセスの喪失
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
	// model/effort の動的変更は明示エラー（DynamicModel:false の防御実装）。
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "gpt-5.4"}); err == nil {
		t.Fatal("dynamic model change must be rejected")
	}
}

func jsonContains(raw json.RawMessage, s string) bool {
	return json.Valid(raw) && strings.Contains(string(raw), s)
}
