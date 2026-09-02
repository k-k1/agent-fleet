package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func TestBuildReportContent(t *testing.T) {
	got := reportBodyForTest("リファクタ作業", "slot07", "answer-ready", "")
	for _, want := range []string{"リファクタ作業", "slot07", "入力待ち"} {
		if !strings.Contains(got, want) {
			t.Fatalf("content missing %q:\n%s", want, got)
		}
	}
	exit := reportBodyForTest("x", "slot08", "exit", "oom")
	if !strings.Contains(exit, "OOM") {
		t.Fatalf("exit content missing OOM label:\n%s", exit)
	}
}

func TestMCPConvArgParsing(t *testing.T) {
	mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled, mcpConvID = false, false, false, ""
	t.Cleanup(func() {
		mcpWriteEnabled, mcpSelfReportOnly, mcpSessionChromiumEnabled, mcpConvID = false, false, false, ""
	})
	// Parse only — feed EOF stdin so the loop exits immediately.
	r, w, _ := os.Pipe()
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	mcpx.RunStdio([]string{"--write", "--conv", "abc-123"})
	if !mcpWriteEnabled || mcpConvID != "abc-123" {
		t.Fatalf("write=%v conv=%q", mcpWriteEnabled, mcpConvID)
	}

	// --chromium-attach is additive only to --self-report. Alone it must not widen
	// the assistant server; together it selects the session's narrow browser scope.
	mcpx.RunStdio([]string{"--chromium-attach"})
	if mcpSelfReportOnly || mcpSessionChromiumEnabled {
		t.Fatalf("standalone chromium flag widened scope: self=%v chromium=%v", mcpSelfReportOnly, mcpSessionChromiumEnabled)
	}
	mcpx.RunStdio([]string{"--chromium-attach", "--self-report"})
	if !mcpSelfReportOnly || !mcpSessionChromiumEnabled || mcpWriteEnabled {
		t.Fatalf("session flags: self=%v chromium=%v write=%v", mcpSelfReportOnly, mcpSessionChromiumEnabled, mcpWriteEnabled)
	}
}

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

	addInstruction(m.Name, conv.ID, turnSourceOperator) // create_session / send_to_session with report_to

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
	awaitReported(t, m.Name)
}

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

	addInstruction(m.Name, conv.ID, turnSourceOperator)
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
	if !sessionReportPending(m.Name) {
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
	awaitReported(t, m.Name)
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
	// The main turn's transcript. 鮮度はファイルの mtime ではなく **実レコードの
	// timestamp** で決まるので（記帳行の追記を「実行中」と誤読しないため）、テストも
	// 時刻を持つ user/assistant 行を書いて動かす。
	mainLog := filepath.Join(proj, sid+".jsonl")
	writeMainAt := func(t *testing.T, at time.Time) {
		t.Helper()
		line := `{"type":"assistant","timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `"}` + "\n"
		if err := os.WriteFile(mainLog, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeMainAt(t, time.Now()) // freshly appended (the turn is still running)

	addInstruction(m.Name, conv.ID, turnSourceOperator)
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
	if !sessionReportPending(m.Name) {
		t.Fatal("false idle consumed the arm")
	}

	// Phase 2 — transcript stale but the marker is still absent: absence alone
	// (LiveState's idle default) must not be trusted either.
	writeMainAt(t, stale)
	settle()
	if n := countReports(); n != 0 {
		t.Fatalf("delivered on a missing marker (n=%d)", n)
	}

	// Phase 3 — an idle marker exists, but the main transcript KEPT GROWING after it
	// (the incident's shape: the marker is not the turn's end — the turn is still
	// appending during a think gap). No delivery.
	status.PersistTurnEnd(sid, "idle")
	writeMainAt(t, time.Now().Add(10*time.Second)) // マーカーより後に伸びた実レコード
	settle()
	if n := countReports(); n != 0 {
		t.Fatalf("delivered while the transcript grew past the idle marker (n=%d)", n)
	}

	// Phase 4 — explicit idle + a transcript that stopped growing before it: the real
	// completion. Exactly one report, arm consumed.
	writeMainAt(t, stale)
	deadline := time.Now().Add(3 * time.Second)
	for countReports() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := countReports(); n != 1 {
		t.Fatalf("report count = %d, want 1", n)
	}
	awaitReported(t, m.Name)
}

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
		addInstruction(name, conv.ID, turnSourceOperator)
	}

	halt("slot11", `{"disarm_report":true}`)
	if sessionReportPending("slot11") {
		t.Fatal("stop_session halt must disarm the pending report")
	}
	halt("slot12", "")
	if !sessionReportPending("slot12") {
		t.Fatal("Console halt (no body) must keep the arm")
	}
}
