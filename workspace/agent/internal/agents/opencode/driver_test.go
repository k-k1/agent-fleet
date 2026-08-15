package opencode

// managed driver（docs/27 P2）のユニットテスト。serve は httptest でモックし、
// turn 状態機械（Send→completed / busy 中の queue / interrupt→cancelled / 台帳の
// 冪等化）と Interaction 応答（ラベル変換・reject）・SSE dispatch を検証する。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// --- pure mapping helpers ---

func TestNormalizeMsgID(t *testing.T) {
	if got := normalizeMsgID("msg_abc"); got != "msg_abc" {
		t.Errorf("msg_abc → %q", got)
	}
	if got := normalizeMsgID("abc-123"); got != "msg_af_abc-123" {
		t.Errorf("abc-123 → %q", got)
	}
	a, b := normalizeMsgID(""), normalizeMsgID("")
	if !strings.HasPrefix(a, "msg_af") || a == b {
		t.Errorf("generated ids must be msg_af-prefixed and unique: %q %q", a, b)
	}
}

func TestSplitModel(t *testing.T) {
	if p, m, ok := splitModel("anthropic/claude-sonnet-5"); !ok || p != "anthropic" || m != "claude-sonnet-5" {
		t.Errorf("got %q %q %v", p, m, ok)
	}
	// openrouter 形式はモデル id 側にも / を含む — 最初の / だけで割る。
	if p, m, ok := splitModel("openrouter/anthropic/claude"); !ok || p != "openrouter" || m != "anthropic/claude" {
		t.Errorf("got %q %q %v", p, m, ok)
	}
	for _, bad := range []string{"", "noslash", "/lead", "trail/"} {
		if _, _, ok := splitModel(bad); ok {
			t.Errorf("splitModel(%q) should not parse", bad)
		}
	}
}

func TestAgentForMode(t *testing.T) {
	if agentForMode("plan") != "plan" || agentForMode("normal") != "build" || agentForMode("") != "" {
		t.Error("mode mapping broken")
	}
}

func TestUpdateSettingsAndClear(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	if err := h.UpdateSettings(agents.ThreadSettings{Model: "openai/gpt-test", Effort: "high", Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := h.Snapshot()
	if snap.Settings.Model != "openai/gpt-test" || snap.Settings.Effort != "high" || snap.Settings.Mode != "plan" {
		t.Fatalf("settings snapshot = %+v", snap.Settings)
	}
	if err := h.UpdateSettings(agents.ThreadSettings{ClearModel: true, ClearEffort: true}); err != nil {
		t.Fatal(err)
	}
	snap, _ = h.Snapshot()
	if snap.Settings.Model != "" || snap.Settings.Effort != "" || snap.Settings.Mode != "plan" {
		t.Fatalf("cleared settings snapshot = %+v", snap.Settings)
	}
}

func TestBuildParts(t *testing.T) {
	parts := buildParts(agents.TurnInput{Prompt: "hello", Attachments: []string{"/tmp/x.png", "/tmp/no-ext"}})
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "hello" {
		t.Errorf("text part: %v", parts[0])
	}
	if parts[1]["type"] != "file" || parts[1]["url"] != "file:///tmp/x.png" || !strings.HasPrefix(parts[1]["mime"].(string), "image/png") {
		t.Errorf("file part: %v", parts[1])
	}
	if parts[2]["mime"] != "application/octet-stream" {
		t.Errorf("extension-less attachment should fall back to octet-stream: %v", parts[2])
	}
}

func TestAnswersToLabels(t *testing.T) {
	qs := []transcript.Question{
		{Question: "color?", Options: []transcript.Option{{Label: "red"}, {Label: "blue"}}},
		{Question: "free?", Options: []transcript.Option{{Label: "a"}}},
	}
	got, err := answersToLabels(qs, []agents.InteractionAnswer{
		{Options: []int{1}},
		{Text: "something"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0][0] != "blue" || got[1][0] != "something" {
		t.Errorf("got %v", got)
	}
	if _, err := answersToLabels(qs, []agents.InteractionAnswer{{Options: []int{0}}}); err == nil {
		t.Error("count mismatch must error")
	}
	if _, err := answersToLabels(qs, []agents.InteractionAnswer{{Options: []int{5}}, {Text: "x"}}); err == nil {
		t.Error("out-of-range option must error")
	}
	if _, err := answersToLabels(qs, []agents.InteractionAnswer{{Options: []int{0}}, {}}); err == nil {
		t.Error("empty answer must error")
	}
}

// --- turn state machine against a mock serve ---

// mockServe fakes the v1 endpoints the handle drives. turnDelay simulates model
// latency; abort unblocks a waiting turn like the real /abort does.
type mockServe struct {
	mu        sync.Mutex
	turns     []string // received prompt texts, in order
	clientIDs []string // messageIDs received (must stay empty — serve 採番に任せる)
	busy      bool
	turnGate  chan struct{} // closed per-turn by abort or timer
	replies   []string      // question reply bodies
	rejects   int
	turnDelay time.Duration
	// turnBody overrides the assistant message the blocking /message call answers with.
	// opencode reports a provider-side failure INSIDE a 200 response (errors.go), so a
	// failing turn is simulated by the body, not by the status code.
	turnBody string
	// turnBodies lets a test model a retryable failure followed by a healthy retry.
	turnBodies []string
}

func newMockServe(t *testing.T) (*mockServe, *httptest.Server) {
	m := &mockServe{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /session/status", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		busy := m.busy
		m.mu.Unlock()
		if busy {
			w.Write([]byte(`{"ses_test":{"type":"busy"}}`))
			return
		}
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /session/ses_test/message", func(w http.ResponseWriter, r *http.Request) {
		// messageID は送らない（serve 採番 — driver.go の実測コメント参照）ので、
		// turn の識別はプロンプト本文で行う。
		var body struct {
			MessageID string           `json:"messageID"`
			Parts     []map[string]any `json:"parts"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		text, _ := "", false
		if len(body.Parts) > 0 {
			t2, ok2 := body.Parts[0]["text"].(string)
			text, _ = t2, ok2
		}
		m.mu.Lock()
		if body.MessageID != "" {
			m.clientIDs = append(m.clientIDs, body.MessageID)
		}
		m.turns = append(m.turns, text)
		m.busy = true
		gate := make(chan struct{})
		m.turnGate = gate
		delay := m.turnDelay
		m.mu.Unlock()
		select {
		case <-gate:
		case <-time.After(delay):
		}
		m.mu.Lock()
		m.busy = false
		resp := m.turnBody
		if n := len(m.turns); n <= len(m.turnBodies) {
			resp = m.turnBodies[n-1]
		}
		m.mu.Unlock()
		if resp == "" {
			resp = `{"info":{"role":"assistant"},"parts":[]}`
		}
		w.Write([]byte(resp))
	})
	mux.HandleFunc("POST /session/ses_test/abort", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		if m.turnGate != nil {
			select {
			case <-m.turnGate:
			default:
				close(m.turnGate)
			}
		}
		m.mu.Unlock()
		w.Write([]byte(`true`))
	})
	mux.HandleFunc("POST /question/que_1/reply", func(w http.ResponseWriter, r *http.Request) {
		b := new(strings.Builder)
		if _, err := jsonCopy(b, r); err == nil {
			m.mu.Lock()
			m.replies = append(m.replies, b.String())
			m.mu.Unlock()
		}
		w.Write([]byte(`true`))
	})
	mux.HandleFunc("POST /question/que_1/reject", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.rejects++
		m.mu.Unlock()
		w.Write([]byte(`true`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return m, srv
}

func jsonCopy(dst *strings.Builder, r *http.Request) (int64, error) {
	buf := make([]byte, 4096)
	var n int64
	for {
		k, err := r.Body.Read(buf)
		dst.Write(buf[:k])
		n += int64(k)
		if err != nil {
			return n, nil
		}
	}
}

func newTestHandle(t *testing.T, srv *httptest.Server) *threadHandle {
	t.Setenv("HOME", t.TempDir()) // status.Persist の書き先をテスト内に隔離
	// sid はテスト毎に別にする。turn は pump の goroutine で走り、テストが終わっても
	// 止まらない（TestQuestionFlow / TestRespondRejectOnCancel は turnDelay=2s の turn を
	// 走らせたまま返る）ので、固定 sid だと**後続テストの中で前のテストの turn が終端**し、
	// プロセス共有の状態通知 seam（agents.SetStateNotifier）へ同じ sid の遷移を流し込む。
	// 実測: 負荷時に TestManagedTurnNotifiesCompletion が
	// `transition = [sid-test idle idle]` / `[sid-test  idle]` で落ちる（8 反復に 1 回程度）。
	return &threadHandle{
		name: "slot-test", dir: "/tmp", ocSid: "sid-" + t.Name(),
		addr: srv.URL, ses: "ses_test", alive: true, gen: 1,
		events: make(chan agents.Event, 64),
	}
}

func waitState(t *testing.T, h *threadHandle, want agents.TurnState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		st := h.state
		h.mu.Unlock()
		if st == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t.Fatalf("state = %s, want %s", h.state, want)
}

func TestSendCompletesTurn(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 50 * time.Millisecond
	h := newTestHandle(t, srv)
	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_one"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnCompleted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 || m.turns[0] != "hi" {
		t.Errorf("turns = %v", m.turns)
	}
	if len(m.clientIDs) != 0 {
		t.Errorf("messageID must not be sent to serve (sortable-id 制約) — got %v", m.clientIDs)
	}
}

func TestLedgerDedupesResend(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 30 * time.Millisecond
	h := newTestHandle(t, srv)
	for i := 0; i < 3; i++ {
		if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_dup"}); err != nil {
			t.Fatal(err)
		}
	}
	waitState(t, h, agents.TurnCompleted)
	time.Sleep(100 * time.Millisecond) // 追加 turn が走っていないことを確かめる猶予
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 {
		t.Errorf("resends must be idempotent — turns = %v", m.turns)
	}
}

func TestSteerQueuesBehindRunningTurn(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 300 * time.Millisecond
	h := newTestHandle(t, srv)
	if err := h.Send(agents.TurnInput{Prompt: "first", ClientMessageID: "msg_a"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnRunning)
	if err := h.Steer(agents.TurnInput{Prompt: "second", ClientMessageID: "msg_b"}); err != nil {
		t.Fatal(err)
	}
	if got := h.queuedPrompts(); len(got) != 1 || got[0] != "second" {
		t.Errorf("queuedPrompts = %v", got)
	}
	// 両 turn が順に流れる。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := len(m.turns)
		m.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitState(t, h, agents.TurnCompleted)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 2 || m.turns[0] != "first" || m.turns[1] != "second" {
		t.Errorf("turns = %v", m.turns)
	}
}

func TestInterruptCancelsAndClearsQueue(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 5 * time.Second // abort が来るまで返らない turn
	h := newTestHandle(t, srv)
	if err := h.Send(agents.TurnInput{Prompt: "long", ClientMessageID: "msg_long"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnRunning)
	if err := h.Steer(agents.TurnInput{Prompt: "queued", ClientMessageID: "msg_q"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Interrupt(); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnCancelled)
	if got := h.queuedPrompts(); len(got) != 0 {
		t.Errorf("interrupt must clear the queue, got %v", got)
	}
	time.Sleep(100 * time.Millisecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) != 1 {
		t.Errorf("queued追撃 must not run after interrupt — turns = %v", m.turns)
	}
}

func TestQuestionFlow(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 2 * time.Second
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)

	if err := h.Send(agents.TurnInput{Prompt: "ask me", ClientMessageID: "msg_ask"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnRunning)
	// SSE の question.asked が届いた体で dispatch する — /global/event の
	// {"payload": {...}} 包み形（実測の本番形）で。
	handleServeEvent([]byte(`{"payload":{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_test","questions":[{"question":"color?","header":"Color","options":[{"label":"red","description":"r"},{"label":"blue","description":"b"}],"multiple":false,"custom":true}]}}}`))
	waitState(t, h, agents.TurnWaitingInteraction)

	// 質問待ち中の自由文送信は question_pending で拒否される（/input のガードと同型）。
	if err := h.Send(agents.TurnInput{Prompt: "stray", ClientMessageID: "msg_stray"}); !ErrQuestionPending(err) {
		t.Errorf("send during question = %v, want question_pending", err)
	}

	snap, _ := h.Snapshot()
	if snap.Interaction == nil || snap.Interaction.ID != "que_1" || len(snap.Interaction.Questions) != 1 {
		t.Fatalf("snapshot interaction = %+v", snap.Interaction)
	}
	if err := h.Respond(agents.InteractionReply{ID: "que_1", Decision: agents.DecisionAnswer,
		Answers: []agents.InteractionAnswer{{Options: []int{1}}}}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	replies := append([]string(nil), m.replies...)
	m.mu.Unlock()
	if len(replies) != 1 || !strings.Contains(replies[0], `[["blue"]]`) {
		t.Errorf("replies = %v", replies)
	}
	waitState(t, h, agents.TurnRunning) // 回答後は turn が続く
}

func TestRespondRejectOnCancel(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 2 * time.Second
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	if err := h.Send(agents.TurnInput{Prompt: "ask", ClientMessageID: "msg_c"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnRunning)
	handleServeEvent([]byte(`{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_test","questions":[{"question":"q","header":"h","options":[{"label":"a","description":"d"}]}]}}`))
	waitState(t, h, agents.TurnWaitingInteraction)
	if err := h.Respond(agents.InteractionReply{ID: "que_1", Decision: agents.DecisionCancel}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rejects != 1 {
		t.Errorf("rejects = %d, want 1", m.rejects)
	}
}

// registerTestHandle puts h into the package registry (handleBySes の解決先) and
// removes it on cleanup so tests don't leak into each other.
func registerTestHandle(t *testing.T, h *threadHandle) {
	handlesMu.Lock()
	handles[h.name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, h.name)
		handlesMu.Unlock()
	})
}

// managedEnrich が driver の runtime 状態を read 層の TranscriptData に合流させる:
// pending 質問へ Interaction id（/respond の宛先）、driver 内キューを キュー済み へ、
// driver 設定のモード（＝次 turn が使う値）を mode chip（§10.2-5）へ。
func TestManagedEnrich(t *testing.T) {
	_, srv := newMockServe(t)
	h := newTestHandle(t, srv)
	registerTestHandle(t, h)
	h.mu.Lock()
	h.inter = &agents.Interaction{ID: "que_9", Kind: "question",
		Questions: []transcript.Question{{Question: "q1"}, {Question: "q2"}}}
	h.queue = []agents.TurnInput{{Prompt: "queued one"}}
	h.settings.Mode = "plan"
	h.mu.Unlock()

	m := session.Meta{Name: h.name, Dir: h.dir, Kind: session.KindOpencode, Driver: session.DriverManaged}
	td := agents.TranscriptData{
		Pending: []transcript.Question{{Question: "q1"}}, // db 由来（id なし）は上書きされる
		Queued:  []string{"store queued"},
		Mode:    "normal",
	}
	managedEnrich(m, &td)
	if len(td.Pending) != 2 || td.Pending[0].ID != "que_9" || td.Pending[1].ID != "que_9" {
		t.Errorf("Pending = %+v, want 2 questions carrying que_9", td.Pending)
	}
	if len(td.Queued) != 2 || td.Queued[1] != "queued one" {
		t.Errorf("Queued = %v, want store queued + driver queue", td.Queued)
	}
	if td.Mode != "plan" {
		t.Errorf("Mode = %q, want plan (driver 設定が db 値より優先)", td.Mode)
	}

	// tui メタ（driver なし）は無変更。
	td2 := agents.TranscriptData{Mode: "normal"}
	managedEnrich(session.Meta{Name: h.name, Kind: session.KindOpencode}, &td2)
	if td2.Mode != "normal" || td2.Pending != nil {
		t.Errorf("tui meta must be untouched: %+v", td2)
	}
}

// codex 側と同じ回帰: managed セッションの turn 完了が状態通知 seam
// （agents.SetStateNotifier → package main の recordSessionNotification）へ届くこと。
// hook を持たない managed driver は status を直接書いて誰にも知らせず、docs/30 の
// 【セッション報告】が構造的に飛ばなかった。
func TestManagedTurnNotifiesCompletion(t *testing.T) {
	m, srv := newMockServe(t)
	m.turnDelay = 20 * time.Millisecond
	h := newTestHandle(t, srv)

	got := make(chan [3]string, 8)
	// 通知 seam はプロセス共有。前のテストが走らせたままの turn（newTestHandle のコメント）
	// がこの窓で終端すると、その遷移がここへ届く — 自分の sid の分だけ拾う。
	agents.SetStateNotifier(func(sid, previous, state, excerpt string) {
		if sid != h.ocSid {
			return
		}
		got <- [3]string{sid, previous, state}
	})
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	if err := h.Send(agents.TurnInput{Prompt: "hi", ClientMessageID: "msg_report"}); err != nil {
		t.Fatal(err)
	}
	waitState(t, h, agents.TurnCompleted)

	select {
	case tr := <-got:
		if tr[0] != h.ocSid || tr[1] != "working" || tr[2] != "idle" {
			t.Fatalf("transition = %v, want %s working→idle", tr, h.ocSid)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn completed without notifying the state seam — 報告が飛ばない")
	}
}
