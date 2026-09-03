package sessionx

// managed driver（docs/log/27: codex app-server / opencode serve）の完了報告 E2E。
//
// 報告（docs/log/30）は hook 経路にしか配線されておらず、hook を持たない managed driver は
// status を直接書いて誰にも知らせなかった — 完了しても【セッション報告】が構造的に
// 一切飛ばない、という穴があった（docs/log/30 に既知制限として記載）。driver 側が
// agents.MarkTurnEnd を通ること自体は各 driver のユニットテストで押さえ、ここでは
// main が張る配線（agents.SetStateNotifier → RecordSessionNotification）から先を
// 実 HTTP で通し、報告カードが会話に届くところまでを確かめる。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// managedReportFixture stands up the operator conversation + an armed managed session
// and the real /chat/report endpoint, with the notifier wired exactly as main() does.
func managedReportFixture(t *testing.T) (session.Meta, string, string) {
	t.Helper()
	home := withTempHome(t)
	// The auto turn would call a real provider; what's under test is the report card.
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// 消費判定はリコンサイラの tick（docs/log/51 Phase 1）— managed の MarkTurnEnd も
	// 「起床ヒント＋レベルの証拠」として同じ経路を通る。
	withTestReconciler(t, 20*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", chatx.HandleChatReport)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &chatx.ChatConversation{ID: chatx.RandUUID(), Agent: "claude", Messages: []chatx.ChatMessage{}}
	if err := chatx.SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{
		Name: "slot77", Dir: t.TempDir(), Kind: session.KindCodex,
		Title: "managed検証タスク", Driver: session.DriverManaged,
	}
	session.WriteMeta(m)

	// main() の配線と同じ — これが無い状態が「報告が飛ばない」バグそのもの。
	agents.SetStateNotifier(RecordSessionNotification)
	t.Cleanup(func() { agents.SetStateNotifier(nil) })

	chatx.AddInstruction(m.Name, conv.ID, TurnSourceOperator) // create_session / send_to_session with report_to
	return m, session.UUID(m.Dir, m.Name), conv.ID
}

// awaitReportCard polls for the report message. deliverSessionReport finishes in a
// goroutine off the handler, and saveConv is a plain os.WriteFile — so read under the
// conversation lock like every real reader does (an unlocked poll catches mid-truncate).
func awaitReportCard(t *testing.T, convID string) *chatx.ChatMessage {
	t.Helper()
	for i := 0; i < 100; i++ {
		unlock := chatx.LockConv(convID)
		c, err := chatx.LoadConv(convID)
		unlock()
		if err == nil {
			for j := range c.Messages {
				if c.Messages[j].Role == "report" {
					return &c.Messages[j]
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// The core regression: a managed turn completing must land a 【セッション報告】 card in
// the operator's conversation and consume the arm — the same outcome the claude hook
// route produces, reached without any hook.
func TestManagedTurnDeliversSessionReport(t *testing.T) {
	m, sid, convID := managedReportFixture(t)

	agents.MarkTurnStart(sid)                     // driver: turn/start（オペレーターの指示が走り出す）
	agents.MarkTurnEnd(sid, agents.TurnCompleted) // driver: turn/completed

	got := awaitReportCard(t, convID)
	if got == nil {
		t.Fatal("managed セッションの完了がオペレーター会話へ報告されなかった")
	}
	if got.Session != m.Name || !strings.Contains(got.Content, "managed検証タスク") ||
		!strings.Contains(got.Content, "入力待ち") {
		t.Fatalf("report card = %+v", got)
	}
	awaitReported(t, m.Name)
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("status = %q, want idle", st.State)
	}
}

// Losing the runtime is NOT a completion: the turn may still be running on the other
// side, so no report may go out and the arm must survive for the real completion
// (§6 reconcile resolves it; process death is record-exit's story).
// レベル判定（docs/log/51）ではここが効く: TurnUnknown も status には idle を書くので、
// 状態文字列だけを見るリコンサイラは「完了」と読んでしまう。書込みが「ターンの終端」
// かどうかの 1bit（status.TurnEnd）を立てないことで、不明は不明のまま扱われる。
func TestManagedRuntimeLossDoesNotReport(t *testing.T) {
	m, sid, convID := managedReportFixture(t)

	agents.MarkTurnStart(sid)
	agents.MarkTurnEnd(sid, agents.TurnUnknown)

	time.Sleep(200 * time.Millisecond) // 報告が飛ばないことの確認なので猶予を置く
	unlock := chatx.LockConv(convID)
	c, err := chatx.LoadConv(convID)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range c.Messages {
		if msg.Role == "report" {
			t.Fatalf("runtime 喪失を完了として報告した: %+v", msg)
		}
	}
	if !chatx.SessionReportPending(m.Name) {
		t.Fatal("arm must survive an unknown outcome — 本当の完了が報告されなくなる")
	}
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("status = %q, want idle (進行中 に張り付かせない)", st.State)
	}
}

// A turn that FAILED (provider error) is terminal too — the report must fire and consume
// the arm exactly like a completion — but it must say the turn errored. Reporting
// 応答が完了 for a turn that produced nothing is what let an exhausted opencode Zen
// balance look like a finished task to the operator.
func TestManagedTurnFailureReportsAsError(t *testing.T) {
	m, sid, convID := managedReportFixture(t)

	agents.MarkTurnStart(sid)
	agents.MarkTurnEndErr(sid, agents.TurnFailed, "[error] APIError (HTTP 401): Insufficient balance.")

	got := awaitReportCard(t, convID)
	if got == nil {
		t.Fatal("失敗したターンもオペレーターへ報告されなければならない")
	}
	if strings.Contains(got.Content, "応答が完了") {
		t.Fatalf("失敗が完了として報告された: %+v", got)
	}
	if !strings.Contains(got.Content, "エラー") {
		t.Fatalf("report card = %+v", got)
	}
	awaitReported(t, m.Name) // a failure ends the instruction just as a completion does
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("status = %q, want idle (the session really is awaiting input)", st.State)
	}
}
