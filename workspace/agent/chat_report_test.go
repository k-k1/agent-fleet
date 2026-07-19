package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// withTempHome points HOME at a temp dir so the fstore/conversation stores write
// under the test's own tree (mirrors the other handler tests' pattern).
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestArmSessionReportRoundTrip(t *testing.T) {
	withTempHome(t)
	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}

	// Unknown conversation id → not armed.
	armSessionReport("slot01", randUUID())
	if reportArmed("slot01") {
		t.Fatal("armed against a dangling conversation id")
	}
	// Invalid session name → not armed.
	armSessionReport("bad/../name", conv.ID)
	if reportArmed("bad/../name") {
		t.Fatal("armed for an invalid session name")
	}

	armSessionReport("slot01", conv.ID)
	if !reportArmed("slot01") {
		t.Fatal("expected armed after a valid arm")
	}
	l, ok := reportLinks.Read("slot01")
	if !ok || l.Conv != conv.ID || !l.Armed {
		t.Fatalf("link = %+v ok=%v", l, ok)
	}
	// Disarm (what handleChatReport does before delivering).
	l.Armed = false
	_ = reportLinks.Write("slot01", l)
	if reportArmed("slot01") {
		t.Fatal("still armed after disarm")
	}
	// Re-arm re-enables exactly one more report (指示1件=報告1回).
	armSessionReport("slot01", conv.ID)
	if !reportArmed("slot01") {
		t.Fatal("expected re-armed")
	}
}

func TestInjectPendingReports(t *testing.T) {
	c := &chatConversation{Messages: []chatMessage{
		{Role: "user", Content: "hi"},
		{Role: "report", Content: "レポートA", Session: "slot01"},
		{Role: "assistant", Content: "ok"},
		{Role: "report", Content: "レポートB", Session: "slot02", Delivered: true},
		{Role: "report", Content: "レポートC", Session: "slot03"},
	}}
	prompt, pending := injectPendingReports(c, "続けて")
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2 (undelivered only)", len(pending))
	}
	for _, want := range []string{"レポートA", "レポートC", "続けて", reportPreamble} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "レポートB") {
		t.Fatal("delivered report re-injected")
	}
	markReportsDelivered(pending)
	if p := undeliveredReports(c); len(p) != 0 {
		t.Fatalf("still undelivered after mark: %d", len(p))
	}
	// No pending reports → the prompt passes through untouched.
	prompt2, pending2 := injectPendingReports(c, "next")
	if prompt2 != "next" || pending2 != nil {
		t.Fatalf("expected pass-through, got %q (%d pending)", prompt2, len(pending2))
	}
}

// TestRunReportAutoTurnCapNotifiesOnce covers the docs/30 auto-turn cap: at the cap
// the operator can't run another turn, so instead of a silent stop the conversation
// gets a one-time system notice asking the user whether to continue, and the report
// stays undelivered to ride the user's next message.
func TestRunReportAutoTurnCapNotifiesOnce(t *testing.T) {
	withTempHome(t)
	conv := &chatConversation{
		ID: randUUID(), Agent: "claude", Tools: toolsAFWrite,
		AutoTurns: maxAutoTurns, // unattended budget already spent
		Messages:  []chatMessage{{Role: "report", Content: "レポートA", Session: "slot01"}},
	}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}

	runReportAutoTurn(conv.ID) // cap reached → append pause notice, run no provider turn

	countNotices := func() (int, *chatMessage) {
		c, err := loadConv(conv.ID)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		var last *chatMessage
		for i := range c.Messages {
			if c.Messages[i].Role == "notice" {
				n++
				last = &c.Messages[i]
			}
		}
		return n, last
	}

	n, notice := countNotices()
	if n != 1 || notice == nil {
		t.Fatalf("notice count = %d, want 1", n)
	}
	if !strings.Contains(notice.Content, "続け") || !strings.Contains(notice.Content, "上限") {
		t.Fatalf("notice missing continue/limit wording: %q", notice.Content)
	}
	c, _ := loadConv(conv.ID)
	if !c.AutoPausedNotified {
		t.Fatal("AutoPausedNotified not set after the pause notice")
	}
	if len(undeliveredReports(c)) != 1 {
		t.Fatal("report must stay undelivered while paused (rides the next user message)")
	}

	// A further report while still capped must NOT append a second notice.
	runReportAutoTurn(conv.ID)
	if n2, _ := countNotices(); n2 != 1 {
		t.Fatalf("notice re-appended while capped: %d", n2)
	}
}

func TestAutoTurnPausedContent(t *testing.T) {
	got := autoTurnPausedContent(3)
	for _, want := range []string{"10", "3 件", "続け", "リセット"} {
		if !strings.Contains(got, want) {
			t.Fatalf("content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(autoTurnPausedContent(0), "残っています") {
		t.Fatal("zero pending should omit the pending-count clause")
	}
}

func TestBuildReportContent(t *testing.T) {
	got := buildReportContent("リファクタ作業", "slot07", "answer-ready", "", "最後の出力です")
	for _, want := range []string{"リファクタ作業", "slot07", "入力待ち", "最後の出力です"} {
		if !strings.Contains(got, want) {
			t.Fatalf("content missing %q:\n%s", want, got)
		}
	}
	exit := buildReportContent("x", "slot08", "exit", "oom", "")
	if !strings.Contains(exit, "OOM") {
		t.Fatalf("exit content missing OOM label:\n%s", exit)
	}
	if strings.Contains(exit, "直近の出力") {
		t.Fatal("empty excerpt should omit the excerpt section")
	}
}

func TestTailRunes(t *testing.T) {
	if got := tailRunes("  abc  ", 10); got != "abc" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("あ", 30)
	got := tailRunes(long, 10)
	if !strings.HasPrefix(got, "…") || len([]rune(got)) != 11 {
		t.Fatalf("got %q (%d runes)", got, len([]rune(got)))
	}
}

// TestChatReportKickStoresLink exercises the mcp --conv plumbing shape: runMCPStdio's
// arg parsing must accept --write --conv <id> in any order.
func TestMCPConvArgParsing(t *testing.T) {
	mcpWriteEnabled, mcpConvID = false, ""
	t.Cleanup(func() { mcpWriteEnabled, mcpConvID = false, "" })
	// Parse only — feed EOF stdin so the loop exits immediately.
	r, w, _ := os.Pipe()
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	runMCPStdio([]string{"--write", "--conv", "abc-123"})
	if !mcpWriteEnabled || mcpConvID != "abc-123" {
		t.Fatalf("write=%v conv=%q", mcpWriteEnabled, mcpConvID)
	}
}

// End-to-end over real HTTP: the claude Stop hook entrypoint → recordSessionNotification
// → kickSessionReport → POST /chat/report → deliverSessionReport → the 【セッション報告】
// card in the operator's conversation. Driven in the incident's exact shape — the pane
// heal wiped the "working" marker before Stop fired — which used to end in silence.
func TestSessionReportDeliveredAfterHealWipedMarker(t *testing.T) {
	home := withTempHome(t)
	// The report's auto turn would call a real provider; the delivery under test is the
	// report card itself, so pin the toggle off (設定 > エージェント「報告への自動応答」).
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", handleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot42", Dir: t.TempDir(), Kind: session.KindClaude, Title: "検証タスク"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	armSessionReport(m.Name, conv.ID) // create_session / send_to_session with report_to

	status.Persist(sid, "working") // the operator's instruction starts a turn
	status.Remove(sid)             // …the pane heal wipes the marker mid-turn
	runSessionStatusHook([]string{"idle", sid})

	// deliverSessionReport finishes in a goroutine off the handler. Read under the
	// conversation lock like every real reader does: saveConv is a plain (non-atomic)
	// os.WriteFile, so an unlocked poll can catch the file mid-truncate.
	var got *chatMessage
	for i := 0; i < 100 && got == nil; i++ {
		unlock := lockConv(conv.ID)
		c, err := loadConv(conv.ID)
		unlock()
		if err == nil {
			for j := range c.Messages {
				if c.Messages[j].Role == "report" {
					got = &c.Messages[j]
				}
			}
		}
		if got == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if got == nil {
		t.Fatal("no session report reached the operator conversation")
	}
	if got.Session != m.Name || !strings.Contains(got.Content, "検証タスク") || !strings.Contains(got.Content, "入力待ち") {
		t.Fatalf("report card = %+v", got)
	}
	if reportArmed(m.Name) {
		t.Fatal("arm must be consumed by the delivered report (指示1件=報告1回)")
	}
}

// TestHaltDisarmsReportOnlyWhenFlagged pins the halt/disarm contract: the MCP
// stop_session sends {"disarm_report":true} and must cancel the pending one-shot
// report (stop = instruction cancelled), while the Console's bodyless halt keeps the
// arm (a later resume + completion is still the instruction's completion). The
// sessions have no live tmux, exercising the already-stopped path — the disarm must
// not depend on liveness.
func TestHaltDisarmsReportOnlyWhenFlagged(t *testing.T) {
	withTempHome(t)
	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}

	halt := func(name, body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/sessions/"+name+"/halt", strings.NewReader(body))
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		handleHaltSession(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("halt %s: status = %d, body = %s", name, rec.Code, rec.Body.String())
		}
	}

	for _, name := range []string{"slot11", "slot12"} {
		session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindClaude})
		armSessionReport(name, conv.ID)
	}

	halt("slot11", `{"disarm_report":true}`)
	if reportArmed("slot11") {
		t.Fatal("stop_session halt must disarm the pending report")
	}
	halt("slot12", "")
	if !reportArmed("slot12") {
		t.Fatal("Console halt (no body) must keep the arm")
	}
}

func TestReportLinkFileLocation(t *testing.T) {
	home := withTempHome(t)
	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	armSessionReport("slot09", conv.ID)
	p := filepath.Join(home, ".config", "agent-fleet", "session-report", "slot09.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected link at %s: %v", p, err)
	}
}
