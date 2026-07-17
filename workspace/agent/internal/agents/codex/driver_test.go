package codex

// managed codex driver のユニットテスト。app-server は WebSocket JSON-RPC の
// 最小モックで置き換え、実 turn を消費せずに状態機械・native steer・interrupt・
// server→client 質問と ClientMessageID 台帳を検証する。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

type mockRPC struct {
	Method string
	Params json.RawMessage
}

type mockCodexServer struct {
	t *testing.T

	server *httptest.Server
	conn   *websocket.Conn
	wmu    sync.Mutex
	mu     sync.Mutex

	calls          []mockRPC
	turns          []string
	clientIDs      []string
	activeTurn     string
	nextTurn       int
	autoComplete   bool
	failNextSteer  bool
	experimental   bool
	clientResponse chan rpcMsg
}

func newMockCodexServer(t *testing.T) (*mockCodexServer, *appClient) {
	t.Helper()
	m := &mockCodexServer{t: t, clientResponse: make(chan rpcMsg, 8)}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		m.conn = conn
		m.serve(conn)
	}))
	t.Cleanup(m.server.Close)

	cl, err := newAppClient("ws" + strings.TrimPrefix(m.server.URL, "http"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.close)
	go cl.readLoop()
	return m, cl
}

func (m *mockCodexServer) serve(conn *websocket.Conn) {
	defer conn.Close()
	for {
		var msg rpcMsg
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg.Method == "initialize" {
			var p struct {
				Capabilities struct {
					Experimental bool `json:"experimentalApi"`
				} `json:"capabilities"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			m.mu.Lock()
			m.experimental = p.Capabilities.Experimental
			m.mu.Unlock()
			m.write(map[string]any{"id": json.RawMessage(msg.ID), "result": map[string]any{}})
			continue
		}
		if msg.Method == "initialized" {
			continue
		}
		if msg.Method == "" && len(msg.ID) > 0 {
			m.clientResponse <- msg
			continue
		}
		m.mu.Lock()
		m.calls = append(m.calls, mockRPC{Method: msg.Method, Params: append(json.RawMessage(nil), msg.Params...)})
		m.mu.Unlock()
		switch msg.Method {
		case "turn/start":
			var p struct {
				Input []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"input"`
				ClientID string `json:"clientUserMessageId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			text := ""
			for _, in := range p.Input {
				if in.Type == "text" {
					text = in.Text
				}
			}
			m.mu.Lock()
			m.nextTurn++
			turnID := "turn_" + string(rune('0'+m.nextTurn))
			m.activeTurn = turnID
			m.turns = append(m.turns, text)
			m.clientIDs = append(m.clientIDs, p.ClientID)
			auto := m.autoComplete
			m.mu.Unlock()
			m.result(msg.ID, map[string]any{"turn": map[string]any{"id": turnID}})
			m.notify("turn/started", map[string]any{"threadId": "thr_test", "turn": map[string]any{"id": turnID}})
			if auto {
				go func() {
					time.Sleep(20 * time.Millisecond)
					m.complete("completed")
				}()
			}
		case "turn/steer":
			m.mu.Lock()
			fail := m.failNextSteer
			m.failNextSteer = false
			m.mu.Unlock()
			if fail {
				m.write(map[string]any{"id": json.RawMessage(msg.ID), "error": map[string]any{"code": -32600, "message": "turn ended"}})
			} else {
				m.result(msg.ID, map[string]any{})
			}
		case "turn/interrupt":
			m.result(msg.ID, map[string]any{})
			m.complete("interrupted")
		default:
			m.result(msg.ID, map[string]any{})
		}
	}
}

func (m *mockCodexServer) write(v any) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if err := m.conn.WriteJSON(v); err != nil {
		// Cleanup races with the server goroutine; a closed connection is expected.
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			m.t.Logf("mock websocket write: %v", err)
		}
	}
}

func (m *mockCodexServer) result(id json.RawMessage, result any) {
	m.write(map[string]any{"id": json.RawMessage(id), "result": result})
}

func (m *mockCodexServer) notify(method string, params any) {
	m.write(map[string]any{"method": method, "params": params})
}

func (m *mockCodexServer) complete(status string) {
	m.mu.Lock()
	turnID := m.activeTurn
	m.mu.Unlock()
	if turnID != "" {
		m.notify("turn/completed", map[string]any{
			"threadId": "thr_test", "turn": map[string]any{"id": turnID, "status": status},
		})
	}
}

func (m *mockCodexServer) ask() {
	m.write(map[string]any{
		"id": 900, "method": "item/tool/requestUserInput", "params": map[string]any{
			"threadId": "thr_test", "turnId": m.activeTurn, "itemId": "item_q1",
			"questions": []map[string]any{{
				"id": "color", "header": "Color", "question": "Which color?", "isOther": true,
				"options": []map[string]any{{"label": "red", "description": "r"}, {"label": "blue", "description": "b"}},
			}},
		},
	})
}

func (m *mockCodexServer) callCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (m *mockCodexServer) lastCall(method string) (json.RawMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.calls) - 1; i >= 0; i-- {
		if m.calls[i].Method == method {
			return append(json.RawMessage(nil), m.calls[i].Params...), true
		}
	}
	return nil, false
}

func newCodexTestHandle(t *testing.T, cl *appClient, name string) *threadHandle {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ledger.Remove(name)
	h := &threadHandle{
		name: name, dir: t.TempDir(), slotSid: "sid-" + name,
		client: cl, gen: 1, tid: "thr_test", alive: true,
		events: make(chan agents.Event, 64),
	}
	t.Cleanup(func() { ledger.Remove(name) })
	return h
}

func registerCodexTestHandle(t *testing.T, h *threadHandle) {
	t.Helper()
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})
}

func waitCodexState(t *testing.T, h *threadHandle, want agents.TurnState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		got := h.state
		h.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t.Fatalf("state = %s, want %s", h.state, want)
}

func TestAppClientEnablesExperimentalAPI(t *testing.T) {
	m, _ := newMockCodexServer(t)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.experimental {
		t.Fatal("initialize must advertise capabilities.experimentalApi=true")
	}
}

func TestAppClientAutoApprovalShapes(t *testing.T) {
	m, cl := newMockCodexServer(t)
	tests := []struct {
		method string
		params json.RawMessage
		want   string
	}{
		{"execCommandApproval", nil, `"approved"`},
		{"item/commandExecution/requestApproval", nil, `"accept"`},
		{"item/permissions/requestApproval", json.RawMessage(`{"permissions":{"network":{"enabled":true}}}`), `"permissions"`},
	}
	for i, tc := range tests {
		cl.handleServerRequest(rpcMsg{ID: json.RawMessage(string(rune('1' + i))), Method: tc.method, Params: tc.params})
		select {
		case res := <-m.clientResponse:
			if !strings.Contains(string(res.Result), tc.want) {
				t.Fatalf("%s result = %s, want %s", tc.method, res.Result, tc.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s response not received", tc.method)
		}
	}
}

func TestSendCompletesTurn(t *testing.T) {
	m, cl := newMockCodexServer(t)
	m.autoComplete = true
	h := newCodexTestHandle(t, cl, "codex-send")
	registerCodexTestHandle(t, h)
	if err := h.Send(agents.TurnInput{Prompt: "hello", ClientMessageID: "af_one"}); err != nil {
		t.Fatal(err)
	}
	waitCodexState(t, h, agents.TurnCompleted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 || m.turns[0] != "hello" || m.clientIDs[0] != "af_one" {
		t.Fatalf("turns=%v clientIDs=%v", m.turns, m.clientIDs)
	}
}

func TestPersistentLedgerDedupesResend(t *testing.T) {
	m, cl := newMockCodexServer(t)
	m.autoComplete = true
	h := newCodexTestHandle(t, cl, "codex-dedupe")
	registerCodexTestHandle(t, h)
	for i := 0; i < 3; i++ {
		if err := h.Send(agents.TurnInput{Prompt: "once", ClientMessageID: "af_dup"}); err != nil {
			t.Fatal(err)
		}
	}
	waitCodexState(t, h, agents.TurnCompleted)
	// handle を作り直してもファイル台帳が同じ id を止める（Agent 再起動相当）。
	h2 := newCodexTestHandleWithoutLedgerReset(t, cl, h.name)
	if err := h2.Send(agents.TurnInput{Prompt: "once", ClientMessageID: "af_dup"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if got := m.callCount("turn/start"); got != 1 {
		t.Fatalf("turn/start count = %d, want 1", got)
	}
}

func newCodexTestHandleWithoutLedgerReset(t *testing.T, cl *appClient, name string) *threadHandle {
	return &threadHandle{
		name: name, dir: t.TempDir(), slotSid: "sid-" + name,
		client: cl, gen: 1, tid: "thr_test", alive: true,
		events: make(chan agents.Event, 64),
	}
}

func TestSteerUsesNativeInjectionAndQueuesOnRace(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-steer")
	registerCodexTestHandle(t, h)
	if err := h.Send(agents.TurnInput{Prompt: "first", ClientMessageID: "af_first"}); err != nil {
		t.Fatal(err)
	}
	waitCodexState(t, h, agents.TurnRunning)
	if err := h.Steer(agents.TurnInput{Prompt: "native", ClientMessageID: "af_native"}); err != nil {
		t.Fatal(err)
	}
	if got := m.callCount("turn/steer"); got != 1 {
		t.Fatalf("native turn/steer count = %d, want 1", got)
	}

	m.mu.Lock()
	m.failNextSteer = true
	m.mu.Unlock()
	if err := h.Steer(agents.TurnInput{Prompt: "queued", ClientMessageID: "af_queued"}); err != nil {
		t.Fatal(err)
	}
	if got := h.queuedPrompts(); len(got) != 1 || got[0] != "queued" {
		t.Fatalf("queuedPrompts = %v", got)
	}
	m.mu.Lock()
	m.autoComplete = true // queued turn is completed automatically once it starts
	m.mu.Unlock()
	m.complete("completed")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && m.callCount("turn/start") < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	waitCodexState(t, h, agents.TurnCompleted)
	if got := m.callCount("turn/start"); got != 2 {
		t.Fatalf("turn/start count = %d, want queued fallback as second turn", got)
	}
}

func TestResumedActiveTurnQueuesUntilCompletion(t *testing.T) {
	m, cl := newMockCodexServer(t)
	m.autoComplete = true
	h := newCodexTestHandle(t, cl, "codex-resumed-active")
	registerCodexTestHandle(t, h)
	h.mu.Lock()
	h.running = true // Agent 再起動後に Resume snapshot から引き継いだ turn
	h.turnID = "turn_external"
	h.state = agents.TurnRunning
	h.mu.Unlock()

	if err := h.Send(agents.TurnInput{Prompt: "after resume", ClientMessageID: "af_after_resume"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := m.callCount("turn/start"); got != 0 {
		t.Fatalf("queued input started alongside resumed active turn: %d calls", got)
	}
	dispatchNotification(rpcMsg{Method: "turn/completed", Params: json.RawMessage(
		`{"threadId":"thr_test","turn":{"id":"turn_external","status":"completed"}}`)})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && m.callCount("turn/start") < 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := m.callCount("turn/start"); got != 1 {
		t.Fatalf("queued input did not start after external completion: %d calls", got)
	}
	waitCodexState(t, h, agents.TurnCompleted)
}

func TestInterruptCancelsTurnAndClearsQueue(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-interrupt")
	registerCodexTestHandle(t, h)
	if err := h.Send(agents.TurnInput{Prompt: "long", ClientMessageID: "af_long"}); err != nil {
		t.Fatal(err)
	}
	waitCodexState(t, h, agents.TurnRunning)
	h.mu.Lock()
	h.queue = append(h.queue, agents.TurnInput{Prompt: "discard me"})
	h.mu.Unlock()
	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	waitCodexState(t, h, agents.TurnCancelled)
	if got := h.queuedPrompts(); len(got) != 0 {
		t.Fatalf("queue after interrupt = %v", got)
	}
	if got := m.callCount("turn/interrupt"); got != 1 {
		t.Fatalf("turn/interrupt count = %d", got)
	}
}

func TestUpdateSettingsAndNotification(t *testing.T) {
	m, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-settings")
	registerCodexTestHandle(t, h)
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "gpt-test", Effort: "high", Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	raw, ok := m.lastCall("thread/settings/update")
	if !ok || !strings.Contains(string(raw), `"collaborationMode":{"mode":"plan"`) ||
		!strings.Contains(string(raw), `"reasoning_effort":"high"`) {
		t.Fatalf("settings params = %s", raw)
	}
	snap, _ := h.Snapshot()
	if snap.Settings.Model != "gpt-test" || snap.Settings.Effort != "high" || snap.Settings.Mode != "plan" {
		t.Fatalf("settings snapshot = %+v", snap.Settings)
	}

	dispatchNotification(rpcMsg{Method: "thread/settings/updated", Params: json.RawMessage(
		`{"threadId":"thr_test","threadSettings":{"model":"gpt-next","effort":"low","collaborationMode":{"mode":"default"}}}`)})
	snap, _ = h.Snapshot()
	if snap.Settings.Model != "gpt-next" || snap.Settings.Effort != "low" || snap.Settings.Mode != "normal" {
		t.Fatalf("notification settings snapshot = %+v", snap.Settings)
	}

	if err := h.UpdateSettings(agents.ThreadSettings{ClearModel: true, ClearEffort: true}); err != nil {
		t.Fatal(err)
	}
	raw, ok = m.lastCall("thread/settings/update")
	if !ok || !strings.Contains(string(raw), `"model":null`) || !strings.Contains(string(raw), `"effort":null`) {
		t.Fatalf("clear settings params = %s", raw)
	}
	snap, _ = h.Snapshot()
	if snap.Settings.Model != "" || snap.Settings.Effort != "" || snap.Settings.Mode != "normal" {
		t.Fatalf("cleared settings snapshot = %+v", snap.Settings)
	}
}

func TestQuestionFlowAndCancel(t *testing.T) {
	t.Run("answer", func(t *testing.T) {
		m, cl := newMockCodexServer(t)
		h := newCodexTestHandle(t, cl, "codex-question-answer")
		registerCodexTestHandle(t, h)
		if err := h.Send(agents.TurnInput{Prompt: "ask", ClientMessageID: "af_ask"}); err != nil {
			t.Fatal(err)
		}
		waitCodexState(t, h, agents.TurnRunning)
		m.ask()
		waitCodexState(t, h, agents.TurnWaitingInteraction)
		if err := h.Send(agents.TurnInput{Prompt: "stray", ClientMessageID: "af_stray"}); !errorsIsQuestionPending(err) {
			t.Fatalf("send while waiting = %v", err)
		}
		snap, _ := h.Snapshot()
		if snap.Interaction == nil || snap.Interaction.ID != "item_q1" || len(snap.Interaction.Questions) != 1 {
			t.Fatalf("interaction = %+v", snap.Interaction)
		}
		if err := h.Respond(agents.InteractionReply{ID: "item_q1", Decision: agents.DecisionAnswer,
			Answers: []agents.InteractionAnswer{{Options: []int{1}}}}); err != nil {
			t.Fatal(err)
		}
		select {
		case res := <-m.clientResponse:
			if string(res.ID) != "900" || !strings.Contains(string(res.Result), `"blue"`) {
				t.Fatalf("question response = id:%s result:%s", res.ID, res.Result)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("question response not received")
		}
		waitCodexState(t, h, agents.TurnRunning)
		m.complete("completed")
		waitCodexState(t, h, agents.TurnCompleted)
	})

	t.Run("cancel interrupts", func(t *testing.T) {
		m, cl := newMockCodexServer(t)
		h := newCodexTestHandle(t, cl, "codex-question-cancel")
		registerCodexTestHandle(t, h)
		if err := h.Send(agents.TurnInput{Prompt: "ask", ClientMessageID: "af_cancel"}); err != nil {
			t.Fatal(err)
		}
		waitCodexState(t, h, agents.TurnRunning)
		m.ask()
		waitCodexState(t, h, agents.TurnWaitingInteraction)
		if err := h.Respond(agents.InteractionReply{ID: "item_q1", Decision: agents.DecisionCancel}); err != nil {
			t.Fatal(err)
		}
		waitCodexState(t, h, agents.TurnCancelled)
		if got := m.callCount("turn/interrupt"); got != 1 {
			t.Fatalf("cancel must map to turn/interrupt, got %d calls", got)
		}
	})
}

func errorsIsQuestionPending(err error) bool { return err == agents.ErrQuestionPending }

func TestManagedEnrich(t *testing.T) {
	_, cl := newMockCodexServer(t)
	h := newCodexTestHandle(t, cl, "codex-enrich")
	registerCodexTestHandle(t, h)
	h.mu.Lock()
	h.inter = &agents.Interaction{ID: "item_q9", Kind: "question",
		Questions: []transcript.Question{{Question: "q1"}, {Question: "q2"}}}
	h.queue = []agents.TurnInput{{Prompt: "queued one"}}
	h.settings.Mode = "plan"
	h.mu.Unlock()

	td := agents.TranscriptData{Pending: []transcript.Question{{Question: "old"}}, Queued: []string{"rollout queue"}, Mode: "normal"}
	m := session.Meta{Name: h.name, Dir: h.dir, Kind: session.KindCodex, Driver: session.DriverManaged}
	managedEnrich(m, &td)
	if len(td.Pending) != 2 || td.Pending[0].ID != "item_q9" || td.Pending[1].ID != "item_q9" {
		t.Fatalf("Pending = %+v", td.Pending)
	}
	if len(td.Queued) != 2 || td.Queued[1] != "queued one" || td.Mode != "plan" {
		t.Fatalf("Queued=%v Mode=%q", td.Queued, td.Mode)
	}

	td2 := agents.TranscriptData{Mode: "normal"}
	managedEnrich(session.Meta{Name: h.name, Kind: session.KindCodex}, &td2)
	if td2.Mode != "normal" || td2.Pending != nil {
		t.Fatalf("tui meta changed: %+v", td2)
	}
}

// managed セッションの完了が状態通知 seam（agents.SetStateNotifier → package main の
// recordSessionNotification）へ届くことの回帰テスト。docs/30 の報告は hook 経路にしか
// 配線されておらず、managed driver は status を直接書いて誰にも知らせなかったため、
// 完了しても【セッション報告】が構造的に飛ばなかった。ここでは実 turn をモック
// app-server で走らせ、driver が seam を通ることを端から確かめる。
func TestManagedTurnNotifiesCompletion(t *testing.T) {
	m, cl := newMockCodexServer(t)
	m.autoComplete = true
	h := newCodexTestHandle(t, cl, "codex-report")
	registerCodexTestHandle(t, h)

	got := make(chan [3]string, 8)
	agents.SetStateNotifier(func(sid, previous, state, excerpt string) {
		got <- [3]string{sid, previous, state}
	})
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	if err := h.Send(agents.TurnInput{Prompt: "hello", ClientMessageID: "af_report"}); err != nil {
		t.Fatal(err)
	}
	waitCodexState(t, h, agents.TurnCompleted)

	select {
	case tr := <-got:
		if tr[0] != h.slotSid || tr[2] != "idle" {
			t.Fatalf("transition = %v, want %s …→idle", tr, h.slotSid)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn completed without notifying the state seam — 報告が飛ばない")
	}
}
