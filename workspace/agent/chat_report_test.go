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
	// The injection guard must ride at the data boundary: a report that instructs
	// command execution / shell sends must never be actionable.
	for _, want := range []string{"shell", "絶対にしない", "インジェクション"} {
		if !strings.Contains(reportPreamble, want) {
			t.Fatalf("reportPreamble missing injection guard %q", want)
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
		AutoTurns: defaultAutoTurns, // unattended budget already spent
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
	got := autoTurnPausedContent(25, 3)
	for _, want := range []string{"25", "3 件", "続け", "リセット"} {
		if !strings.Contains(got, want) {
			t.Fatalf("content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(autoTurnPausedContent(10, 0), "残っています") {
		t.Fatal("zero pending should omit the pending-count clause")
	}
}

// TestChatAutoTurnLimit pins the configurable ceiling: default 10 when unset, the
// stored value inside range, and a hard clamp to [1, 50] — no unlimited mode.
func TestChatAutoTurnLimit(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	prefs := filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json")
	if got := chatAutoTurnLimit(); got != defaultAutoTurns {
		t.Fatalf("default = %d, want %d", got, defaultAutoTurns)
	}
	for raw, want := range map[string]int{"30": 30, "0": 1, "-5": 1, "999": 50, "50": 50, "1": 1} {
		if err := os.WriteFile(prefs, []byte(`{"assistantAutoTurnLimit":`+raw+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := chatAutoTurnLimit(); got != want {
			t.Fatalf("limit(%s) = %d, want %d", raw, got, want)
		}
	}
	if err := os.WriteFile(prefs, []byte(`{"assistantAutoTurnLimit":"lots"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := chatAutoTurnLimit(); got != defaultAutoTurns {
		t.Fatalf("invalid type = %d, want default %d", got, defaultAutoTurns)
	}
}

func TestBuildReportContent(t *testing.T) {
	got := buildReportContent("リファクタ作業", "slot07", "answer-ready", "", 0)
	for _, want := range []string{"リファクタ作業", "slot07", "入力待ち"} {
		if !strings.Contains(got, want) {
			t.Fatalf("content missing %q:\n%s", want, got)
		}
	}
	exit := buildReportContent("x", "slot08", "exit", "oom", 0)
	if !strings.Contains(exit, "OOM") {
		t.Fatalf("exit content missing OOM label:\n%s", exit)
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
// → kickSessionReport → POST /chat/report（= リコンサイラの起床ヒント）→ tick の
// settle → the 【セッション報告】 card in the operator's conversation. Driven in the
// incident's exact shape — the pane heal wiped the "working" marker before Stop fired —
// which used to end in silence. docs/51 Phase 1 では kick が消えても次の tick が同じ
// 状態を見て拾う（ここではヒント有りの経路を通す）。
func TestSessionReportDeliveredAfterHealWipedMarker(t *testing.T) {
	home := withTempHome(t)
	withTestReconciler(t, 20*time.Millisecond)
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
	// A real turn leaves a FRESH main transcript behind (the answer was just written).
	// 転写の鮮度そのものを常設ゲートにすると、正常完了の報告が毎回 TTL(90s) ぶん遅れる —
	// 完了の判定は「Stop のマーカーより後にも転写が伸びているか」の相対比較で行う。
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "projects", "p1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "projects", "p1", sid+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status.Remove(sid) // …the pane heal wipes the marker mid-turn
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
	awaitDisarmed(t, m.Name)
}

// TestQuestionReportInterimKeepsArm pins the interim question report (docs/30):
// a pending AskUserQuestion is reported to the operator conversation so it can
// relay/answer, but the one-shot arm survives — the instruction's completion
// still gets its own report.
func TestQuestionReportInterimKeepsArm(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot44", Dir: t.TempDir(), Kind: session.KindClaude, Title: "質問検証"}
	session.WriteMeta(m)
	armSessionReport(m.Name, conv.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/report",
		strings.NewReader(`{"name":"slot44","kind":"question"}`))
	rec := httptest.NewRecorder()
	handleChatReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

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
		t.Fatal("no interim question report reached the conversation")
	}
	if !strings.Contains(got.Content, "質問") || !strings.Contains(got.Content, "answer_session_question") {
		t.Fatalf("question report card = %q", got.Content)
	}
	if !reportArmed(m.Name) {
		t.Fatal("interim question report must NOT consume the arm (完了報告は別途)")
	}
}

// TestReportHeadForAutoPilot pins the 自動走行 toggle: the interim question/plan
// report text carries the mode's marching orders when ON (auto-answer with the
// session's recommendation / drive the review-approve loop) and the confirm-first
// instructions when OFF (the default — the key is opt-in).
func TestReportHeadForAutoPilot(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	prefs := filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json")

	// Default (no key): confirm-first.
	q, pl := reportHeadFor("question", "", 0), reportHeadFor("plan-approval", "", 0)
	if strings.Contains(q, "自動走行") || strings.Contains(pl, "自動走行") {
		t.Fatalf("auto-pilot text without opt-in:\nq=%q\npl=%q", q, pl)
	}
	if !strings.Contains(q, "answer_session_question") || !strings.Contains(pl, "respond_session_plan") {
		t.Fatalf("tool guidance missing:\nq=%q\npl=%q", q, pl)
	}

	if err := os.WriteFile(prefs, []byte(`{"assistantAutoPilot":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	q, pl = reportHeadFor("question", "", 0), reportHeadFor("plan-approval", "", 0)
	if !strings.Contains(q, "自動走行") || !strings.Contains(q, "answer_session_question") {
		t.Fatalf("auto-pilot question head = %q", q)
	}
	if !strings.Contains(pl, "自動走行") || !strings.Contains(pl, "respond_session_plan") || !strings.Contains(pl, "レビュー") {
		t.Fatalf("auto-pilot plan head = %q", pl)
	}
	// The guardrails must survive in BOTH modes: destructive cases go to the user.
	if !strings.Contains(q, "破壊的") || !strings.Contains(pl, "破壊的") {
		t.Fatal("auto-pilot heads must keep the destructive-case guard")
	}
}

// TestPlanReportInterimKeepsArm mirrors the question test for plan-approval: the
// interim plan report reaches the conversation without consuming the arm.
func TestPlanReportInterimKeepsArm(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot45", Dir: t.TempDir(), Kind: session.KindClaude, Title: "プラン検証"}
	session.WriteMeta(m)
	armSessionReport(m.Name, conv.ID)

	req := httptest.NewRequest(http.MethodPost, "/chat/report",
		strings.NewReader(`{"name":"slot45","kind":"plan-approval"}`))
	rec := httptest.NewRecorder()
	handleChatReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	found := false
	for i := 0; i < 100 && !found; i++ {
		unlock := lockConv(conv.ID)
		c, err := loadConv(conv.ID)
		unlock()
		if err == nil {
			for j := range c.Messages {
				if c.Messages[j].Role == "report" && strings.Contains(c.Messages[j].Content, "プラン") {
					found = true
				}
			}
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("no interim plan report reached the conversation")
	}
	if !reportArmed(m.Name) {
		t.Fatal("interim plan report must NOT consume the arm")
	}
}

// TestSessionReportDeferredWhileSubagentBusy pins the premature-completion fix
// (docs/30, 実測 2026-07-24 saga5uc): claude launches background subagents and Stops
// minutes before the instruction is actually done. That early answer-ready kick must
// NOT consume the one-shot arm — delivery waits until the subagent transcripts go
// stale and the session sits at idle, then fires exactly once.
// docs/51 Phase 1 の読み替え: 「保留 waiter」という特例は消え、SubagentBusy は
// リコンサイラの **busy 証拠** になった（意味論は同じ — 判定が1か所に集まっただけ）。
func TestSessionReportDeferredWhileSubagentBusy(t *testing.T) {
	home := withTempHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg) // claude.SubagentBusy globs under this dir
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The delivery under test is the report card; pin the auto turn off (it would
	// call a real provider).
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", handleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot43", Dir: t.TempDir(), Kind: session.KindClaude, Title: "BG検証"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	// A live in-process background subagent: its per-agent transcript is fresh.
	agDir := filepath.Join(cfg, "projects", "p1", sid, "subagents")
	if err := os.MkdirAll(agDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logp := filepath.Join(agDir, "agent-1.jsonl")
	if err := os.WriteFile(logp, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	armSessionReport(m.Name, conv.ID)
	status.Persist(sid, "working")
	runSessionStatusHook([]string{"idle", sid}) // Stop right after the BG launch → kick

	countReports := func() int {
		unlock := lockConv(conv.ID)
		defer unlock()
		c, err := loadConv(conv.ID)
		if err != nil {
			return -1
		}
		n := 0
		for i := range c.Messages {
			if c.Messages[i].Role == "report" {
				n++
			}
		}
		return n
	}

	// Deferred: the arm survives the premature Stop and no report card lands.
	time.Sleep(100 * time.Millisecond)
	if !reportArmed(m.Name) {
		t.Fatal("premature Stop consumed the arm despite live background agents")
	}
	if n := countReports(); n != 0 {
		t.Fatalf("report delivered while background agents run (n=%d)", n)
	}

	// The agents go quiet (transcript stale) with the session at idle → the waiter
	// delivers exactly one report and consumes the arm.
	stale := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(logp, stale, stale); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for countReports() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := countReports(); n != 1 {
		t.Fatalf("deferred report count = %d, want 1", n)
	}
	awaitDisarmed(t, m.Name)
	unlock := lockConv(conv.ID)
	c, err := loadConv(conv.ID)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.Messages {
		if c.Messages[i].Role != "report" {
			continue
		}
		if !strings.Contains(c.Messages[i].Content, "入力待ち") || strings.Contains(c.Messages[i].Content, "直近の出力") {
			t.Fatalf("report card = %q", c.Messages[i].Content)
		}
	}
}

// TestSessionReportIgnoresFalseIdle pins the delivery gate against the false-idle
// window (実測 2026-07-28 sqmconc/azw7wys): mid-turn, a think gap fires no hooks and
// the pane-based self-heal can remove the status marker; the bare LiveState then
// defaults to idle and the old waiter spent the one-shot arm on a turn that was still
// running — the real completion 27 minutes later kicked into armed=false and was
// silently dropped.
// docs/51 Phase 1 の読み替え: waiter は消え、その配送条件は述語に畳まれた —
// **無マーカーは「不明」であって idle ではない**、そして transcript の鮮度は busy 証拠。
// （旧名 TestSessionReportWaiterIgnoresFalseIdle）
func TestSessionReportIgnoresFalseIdle(t *testing.T) {
	home := withTempHome(t)
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withTestReconciler(t, 20*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/report", handleChatReport)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("AGENT_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	conv := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot44", Dir: t.TempDir(), Kind: session.KindClaude, Title: "誤idle検証"}
	session.WriteMeta(m)
	sid := session.UUID(m.Dir, m.Name)

	proj := filepath.Join(cfg, "projects", "p1")
	agDir := filepath.Join(proj, sid, "subagents")
	if err := os.MkdirAll(agDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agLog := filepath.Join(agDir, "agent-1.jsonl")
	if err := os.WriteFile(agLog, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The main turn's transcript, freshly appended (the turn is still running).
	mainLog := filepath.Join(proj, sid+".jsonl")
	if err := os.WriteFile(mainLog, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	armSessionReport(m.Name, conv.ID)
	status.Persist(sid, "working")
	runSessionStatusHook([]string{"idle", sid}) // early Stop → kick → deferred (BG busy)

	countReports := func() int {
		unlock := lockConv(conv.ID)
		defer unlock()
		c, err := loadConv(conv.ID)
		if err != nil {
			return -1
		}
		n := 0
		for i := range c.Messages {
			if c.Messages[i].Role == "report" {
				n++
			}
		}
		return n
	}
	stale := time.Now().Add(-3 * time.Minute)
	settle := func() { time.Sleep(150 * time.Millisecond) } // several reconciler ticks

	// Phase 1 — BG quiet, but the heal removed the marker while the main transcript
	// is fresh: an absent marker must not read as idle, and a fresh transcript means
	// the turn is still running. No delivery, arm intact.
	if err := os.Chtimes(agLog, stale, stale); err != nil {
		t.Fatal(err)
	}
	status.Remove(sid)
	settle()
	if n := countReports(); n != 0 {
		t.Fatalf("delivered on a missing marker + fresh transcript (n=%d)", n)
	}
	if !reportArmed(m.Name) {
		t.Fatal("false idle consumed the arm")
	}

	// Phase 2 — transcript stale but the marker is still absent: absence alone
	// (LiveState's idle default) must not be trusted either.
	if err := os.Chtimes(mainLog, stale, stale); err != nil {
		t.Fatal(err)
	}
	settle()
	if n := countReports(); n != 0 {
		t.Fatalf("delivered on a missing marker (n=%d)", n)
	}

	// Phase 3 — an idle marker exists, but the main transcript KEPT GROWING after it
	// (the incident's shape: the marker is not the turn's end — the turn is still
	// appending during a think gap). No delivery.
	status.PersistTurnEnd(sid, "idle")
	after := time.Now().Add(10 * time.Second) // マーカーより後に伸びた転写
	if err := os.Chtimes(mainLog, after, after); err != nil {
		t.Fatal(err)
	}
	settle()
	if n := countReports(); n != 0 {
		t.Fatalf("delivered while the transcript grew past the idle marker (n=%d)", n)
	}

	// Phase 4 — explicit idle + a transcript that stopped growing before it: the real
	// completion. Exactly one report, arm consumed.
	if err := os.Chtimes(mainLog, stale, stale); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for countReports() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := countReports(); n != 1 {
		t.Fatalf("report count = %d, want 1", n)
	}
	awaitDisarmed(t, m.Name)
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

// TestReportHeadForTurnAborted covers the 中断時の自動再開 wording (docs/47): ON asks the
// operator to resume, OFF asks it to confirm with the user first, and past the cap the
// report escalates instead of asking for yet another resume. The language instruction is
// part of the contract — sending JA into a session working in EN (or the reverse) flips
// its output language for every following turn, and there is no per-session language to
// read, so the operator has to be told to match the session.
func TestReportHeadForTurnAborted(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	prefs := filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json")

	// Default (no key) = ON: nudge the session to continue.
	on := reportHeadFor(reportKindAnswerReady, reportReasonTurnAborted, 1)
	for _, want := range []string{"中断", "send_to_session", "言語", "破壊的"} {
		if !strings.Contains(on, want) {
			t.Fatalf("auto-resume head missing %q:\n%s", want, on)
		}
	}

	// OFF: confirm with the user before resuming, but still explain the interruption.
	if err := os.WriteFile(prefs, []byte(`{"assistantAutoResume":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	off := reportHeadFor(reportKindAnswerReady, reportReasonTurnAborted, 1)
	if !strings.Contains(off, "確認") || !strings.Contains(off, "言語") {
		t.Fatalf("auto-resume OFF head = %q", off)
	}
	if strings.Contains(off, "ON】") {
		t.Fatalf("OFF head advertises the ON mode:\n%s", off)
	}

	// Past the cap: escalate to the user, never ask for another resume — this is the
	// stop for an error that keeps recurring.
	capped := reportHeadFor(reportKindAnswerReady, reportReasonTurnAborted, maxAutoResumeAttempts+1)
	if !strings.Contains(capped, "上限") || strings.Contains(capped, "send_to_session") {
		t.Fatalf("capped head must escalate without asking for a resume:\n%s", capped)
	}

	// A clean completion keeps its old wording — the abort branch must not leak into it.
	clean := reportHeadFor(reportKindAnswerReady, "", 0)
	if strings.Contains(clean, "中断") {
		t.Fatalf("clean completion head changed:\n%s", clean)
	}
}

// TestAutoResumeCounter: consecutive aborts accumulate up to the cap and a clean
// completion resets the budget, so a session that recovers is not penalised later.
func TestAutoResumeCounter(t *testing.T) {
	withTempHome(t)
	if n := autoResumeAttempts("slot20"); n != 0 {
		t.Fatalf("fresh session count = %d, want 0", n)
	}
	if n := bumpAutoResume("slot20"); n != 1 {
		t.Fatalf("first bump = %d, want 1", n)
	}
	if n := bumpAutoResume("slot20"); n != 2 {
		t.Fatalf("second bump = %d, want 2", n)
	}
	if n := autoResumeAttempts("slot20"); n != 2 {
		t.Fatalf("read back = %d, want 2", n)
	}
	resetAutoResume("slot20")
	if n := autoResumeAttempts("slot20"); n != 0 {
		t.Fatalf("after reset = %d, want 0", n)
	}
	// Counters are per session.
	bumpAutoResume("slot21")
	if n := autoResumeAttempts("slot20"); n != 0 {
		t.Fatalf("slot20 picked up slot21's count: %d", n)
	}
}
