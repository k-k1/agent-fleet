package sessionx

// End-to-end completion reporting for managed drivers (docs/log/27: codex app-server /
// opencode serve).
//
// Reporting (docs/log/30) was wired into the hook route only, so a managed driver, which has no
// hook, wrote status directly and told nobody: a completed turn structurally never sent a
// session report (【セッション報告】) card at all - recorded as a known limitation in
// docs/log/30. That each driver goes through agents.MarkTurnEnd is covered by the drivers' own
// unit tests; here everything from the wiring main puts in place (agents.SetStateNotifier ->
// RecordSessionNotification) onwards runs over real HTTP, up to the report card arriving in the
// conversation.

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
	// Consumption is decided by the reconciler tick (docs/log/51 Phase 1); a managed
	// MarkTurnEnd travels the same route, as a wake hint plus the evidence for the level.
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

	// The same wiring main() does; its absence is exactly the "no report is ever sent" bug.
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

// The core regression: a managed turn completing must land a session report
// (【セッション報告】) card in the operator's conversation and consume the arm — the same
// outcome the claude hook route produces, reached without any hook.
func TestManagedTurnDeliversSessionReport(t *testing.T) {
	m, sid, convID := managedReportFixture(t)

	agents.MarkTurnStart(sid)                     // driver: turn/start (the operator instruction starts running)
	agents.MarkTurnEnd(sid, agents.TurnCompleted) // driver: turn/completed

	got := awaitReportCard(t, convID)
	if got == nil {
		t.Fatal("the completion of a managed session was not reported to the operator conversation")
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
// Level determination (docs/log/51) turns on this: TurnUnknown writes idle to status too, so a
// reconciler that looks only at the state string reads it as a completion. Leaving the one bit
// that marks a write as the end of a turn (status.TurnEnd) unset keeps unknown treated as
// unknown.
func TestManagedRuntimeLossDoesNotReport(t *testing.T) {
	m, sid, convID := managedReportFixture(t)

	agents.MarkTurnStart(sid)
	agents.MarkTurnEnd(sid, agents.TurnUnknown)

	time.Sleep(200 * time.Millisecond) // grace: what is being checked is that no report goes out
	unlock := chatx.LockConv(convID)
	c, err := chatx.LoadConv(convID)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range c.Messages {
		if msg.Role == "report" {
			t.Fatalf("a lost runtime was reported as a completion: %+v", msg)
		}
	}
	if !chatx.SessionReportPending(m.Name) {
		t.Fatal("arm must survive an unknown outcome - otherwise the real completion is never reported")
	}
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("status = %q, want idle (must not stick at in progress)", st.State)
	}
}

// A turn that FAILED (provider error) is terminal too — the report must fire and consume
// the arm exactly like a completion — but it must say the turn errored. Reporting a
// completed response ("応答が完了") for a turn that produced nothing is what let an
// exhausted opencode Zen balance look like a finished task to the operator.
func TestManagedTurnFailureReportsAsError(t *testing.T) {
	m, sid, convID := managedReportFixture(t)

	agents.MarkTurnStart(sid)
	agents.MarkTurnEndErr(sid, agents.TurnFailed, "[error] APIError (HTTP 401): Insufficient balance.")

	got := awaitReportCard(t, convID)
	if got == nil {
		t.Fatal("a failed turn must be reported to the operator as well")
	}
	if strings.Contains(got.Content, "応答が完了") {
		t.Fatalf("a failure was reported as a completion: %+v", got)
	}
	if !strings.Contains(got.Content, "エラー") {
		t.Fatalf("report card = %+v", got)
	}
	awaitReported(t, m.Name) // a failure ends the instruction just as a completion does
	if st, _ := status.Read(sid); st.State != "idle" {
		t.Fatalf("status = %q, want idle (the session really is awaiting input)", st.State)
	}
}
