package chatx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// withTempHome points HOME at a temp dir so the fstore/conversation stores write
// under the test's own tree (mirrors the other handler tests' pattern).
//
// Where the waiter is pushed is everything. `t.Cleanup` is LIFO, so this wait has to be
// pushed AFTER `t.Setenv` in order to run BEFORE HOME is restored (push it before and it
// runs after the restore, which is too late). Returning without waiting lets the delivery
// goroutine HandleChatReport spawned write its notification into the RESTORED, REAL HOME,
// which puts a ghost notification in the user's Console (see interimDeliveries in
// chat_report.go).
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Cleanup(WaitInterimDeliveries)
	return dir
}

// TestInstrLedgerRoundTrip is the v1 TestArmSessionReportRoundTrip read through the
// Phase 2 ledger (docs/log/51): delivery APPENDS a row, and an instruction with no target
// never becomes one. The decisive difference is the last two lines — v1's re-arm
// overwrote the previous instruction's bit, whereas rows accumulate.
func TestInstrLedgerRoundTrip(t *testing.T) {
	withTempHome(t)
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	// Unknown conversation id → no row (a row with no target is never created).
	if id := AddInstruction("slot01", RandUUID(), "operator"); id != "" {
		t.Fatal("created a row for a dangling conversation id")
	}
	// Invalid session name → no row.
	if id := AddInstruction("bad/../name", conv.ID, "operator"); id != "" {
		t.Fatal("created a row for an invalid session name")
	}
	if SessionReportPending("slot01") {
		t.Fatal("no rows exist, yet the session counts as owing a report")
	}

	id1 := AddInstruction("slot01", conv.ID, "operator")
	if id1 == "" || !SessionReportPending("slot01") {
		t.Fatalf("delivery did not create a pending row (id=%q)", id1)
	}
	rows := openInstrRows("slot01")
	if len(rows) != 1 || rows[0].Conv != conv.ID || rows[0].State != instrPending ||
		rows[0].Source != "operator" || rows[0].Cursor.At == "" {
		t.Fatalf("row = %+v", rows)
	}

	// A second instruction is APPENDED (v1's re-arm overwrote the single bit = gap A).
	id2 := AddInstruction("slot01", conv.ID, "operator")
	if rows := openInstrRows("slot01"); len(rows) != 2 {
		t.Fatalf("second instruction left %d rows (overwritten)", len(rows))
	}

	// Report only the first → only that row closes, the second stays open.
	markInstrReported("slot01", []string{id1}, time.Now())
	rows = openInstrRows("slot01")
	if len(rows) != 1 || rows[0].ID != id2 {
		t.Fatalf("reporting the earlier instruction caught the later one in the crossfire: %+v", rows)
	}
	if !SessionReportPending("slot01") {
		t.Fatal("the later instruction is still open, yet nothing was judged pending")
	}
}

// TestInstrLedgerStateMachine pins the row's state machine (docs/log/51 §data model):
// pending → interim_reported (non-consuming) → reported → reopened → reported, plus
// stop_session's cancelled. Also that a closed row never opens by itself and that the cap
// stops reopen.
func TestInstrLedgerStateMachine(t *testing.T) {
	withTempHome(t)
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	stateOf := func(name, id string) string {
		for _, r := range ReadInstrRows(name) {
			if r.ID == id {
				return r.State
			}
		}
		return "<missing>"
	}

	id := AddInstruction("slot02", conv.ID, "operator")
	if got := stateOf("slot02", id); got != instrPending {
		t.Fatalf("initial state = %q", got)
	}

	// interim (a question) is NON-CONSUMING: the state advances but the row stays open
	// — the obligation to report completion remains.
	markInstrInterim("slot02", "question", time.Now())
	if got := stateOf("slot02", id); got != instrInterim {
		t.Fatalf("after interim = %q", got)
	}
	if !SessionReportPending("slot02") {
		t.Fatal("the interim report ate the completion one-shot (v1's measured defect)")
	}
	markInstrInterim("slot02", "plan-approval", time.Now())
	rows := ReadInstrRows("slot02")
	if rows[0].Interim.QuestionAt == "" || rows[0].Interim.PlanAt == "" {
		t.Fatalf("no record that the interim was already reported: %+v", rows[0])
	}

	markInstrReported("slot02", []string{id}, time.Now())
	if got := stateOf("slot02", id); got != instrReported {
		t.Fatalf("after the completion report = %q", got)
	}
	if SessionReportPending("slot02") {
		t.Fatal("a reported row is still counted as owing a report")
	}

	// Compensation (the transition Phase 3 drives): reported → reopened → reported.
	if !reopenInstrRow("slot02", id) {
		t.Fatal("cannot reopen a reported row")
	}
	if got := stateOf("slot02", id); got != instrReopened || !SessionReportPending("slot02") {
		t.Fatalf("after reopen = %q pending=%v", stateOf("slot02", id), SessionReportPending("slot02"))
	}
	markInstrReported("slot02", []string{id}, time.Now())
	if got := stateOf("slot02", id); got != instrReported {
		t.Fatalf("after re-reporting = %q", got)
	}
	// reopen is capped at instrReopenMax per row (cutting off a row whose decision
	// keeps oscillating).
	for i := 1; i < instrReopenMax; i++ {
		if !reopenInstrRow("slot02", id) {
			t.Fatalf("reopen number %d was refused", i+1)
		}
		markInstrReported("slot02", []string{id}, time.Now())
	}
	if reopenInstrRow("slot02", id) {
		t.Fatalf("reopened past the cap (%d)", instrReopenMax)
	}

	// stop_session (disarm) turns open rows cancelled. A cancelled row never re-opens.
	id2 := AddInstruction("slot02", conv.ID, "operator")
	if n := cancelInstructions("slot02"); n != 1 {
		t.Fatalf("cancelled rows = %d, want 1", n)
	}
	if got := stateOf("slot02", id2); got != instrCancelled {
		t.Fatalf("after cancel = %q", got)
	}
	if SessionReportPending("slot02") {
		t.Fatal("a cancelled row still owes a report")
	}
	markInstrReported("slot02", []string{id2}, time.Now())
	if got := stateOf("slot02", id2); got != instrCancelled {
		t.Fatalf("a report overwrote cancelled: %q", got)
	}
}

// TestMigrateReportArms covers the Phase 2 migration (docs/log/51 §migration): at startup
// a v1 armed=true becomes one row and the source file is deleted, so the rows do not grow
// on every restart.
func TestMigrateReportArms(t *testing.T) {
	withTempHome(t)
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
	_ = reportLinks.Write("slot03", reportLink{Conv: conv.ID, Armed: true, At: at})
	_ = reportLinks.Write("slot04", reportLink{Conv: conv.ID, Armed: false, At: at}) // consumed

	MigrateReportArms()

	rows := openInstrRows("slot03")
	if len(rows) != 1 || rows[0].Conv != conv.ID || rows[0].Cursor.At != at {
		t.Fatalf("migrated row = %+v", rows)
	}
	if SessionReportPending("slot04") {
		t.Fatal("built a row out of a v1 record that was not armed")
	}
	if _, ok := reportLinks.Read("slot03"); ok {
		t.Fatal("the v1 source file survived (rows would grow on every restart)")
	}
	MigrateReportArms() // a second run must do nothing
	if n := len(openInstrRows("slot03")); n != 1 {
		t.Fatalf("re-migration grew the rows to %d", n)
	}
}

func TestInjectPendingReports(t *testing.T) {
	c := &ChatConversation{Messages: []ChatMessage{
		{Role: "user", Content: "hi"},
		{Role: "report", Content: "レポートA", Session: "slot01"},
		{Role: "assistant", Content: "ok"},
		{Role: "report", Content: "レポートB", Session: "slot02", Delivered: true},
		{Role: "report", Content: "レポートC", Session: "slot03"},
	}}
	prompt, pending := InjectPendingReports(c, "続けて")
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2 (undelivered only)", len(pending))
	}
	for _, want := range []string{"レポートA", "レポートC", "続けて", reportPreambleFor("ja")} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	// The injection guard must ride at the data boundary: a report that instructs
	// command execution / shell sends must never be actionable.
	for _, want := range []string{"shell", "絶対にしない", "インジェクション"} {
		if !strings.Contains(reportPreambleFor("ja"), want) {
			t.Fatalf("reportPreamble missing injection guard %q", want)
		}
	}
	if strings.Contains(prompt, "レポートB") {
		t.Fatal("delivered report re-injected")
	}
	MarkReportsDelivered(pending)
	if p := undeliveredReports(c); len(p) != 0 {
		t.Fatalf("still undelivered after mark: %d", len(p))
	}
	// No pending reports → the prompt passes through untouched.
	prompt2, pending2 := InjectPendingReports(c, "next")
	if prompt2 != "next" || pending2 != nil {
		t.Fatalf("expected pass-through, got %q (%d pending)", prompt2, len(pending2))
	}
}

// TestRunReportAutoTurnCapNotifiesOnce covers the docs/log/30 auto-turn cap: at the cap
// the operator can't run another turn, so instead of a silent stop the conversation
// gets a one-time system notice asking the user whether to continue, and the report
// stays undelivered to ride the user's next message.
func TestRunReportAutoTurnCapNotifiesOnce(t *testing.T) {
	withTempHome(t)
	conv := &ChatConversation{
		ID: RandUUID(), Agent: "claude", Tools: assistants.ToolsAFWrite,
		AutoTurns: DefaultAutoTurns,
		Messages:  []ChatMessage{{Role: "report", Content: "レポートA", Session: "slot01"}},
	}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	runReportAutoTurn(conv.ID) // cap reached → append pause notice, run no provider turn

	countNotices := func() (int, *ChatMessage) {
		c, err := LoadConv(conv.ID)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		var last *ChatMessage
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
	c, _ := LoadConv(conv.ID)
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
	if got := chatAutoTurnLimit(); got != DefaultAutoTurns {
		t.Fatalf("default = %d, want %d", got, DefaultAutoTurns)
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
	if got := chatAutoTurnLimit(); got != DefaultAutoTurns {
		t.Fatalf("invalid type = %d, want default %d", got, DefaultAutoTurns)
	}
}

// TestChatReportKickStoresLink exercises the mcp --conv plumbing shape: runMCPStdio's
// arg parsing must accept --write --conv <id> in any order.
// End-to-end over real HTTP: the claude Stop hook entrypoint → recordSessionNotification
// → kickSessionReport → POST /chat/report (= a wake-up hint for the reconciler) → the
// tick's settle → the session-report (【セッション報告】) card in the operator's
// conversation. Driven in the incident's exact shape — the pane heal wiped the "working"
// marker before Stop fired — which used to end in silence. Under docs/log/51 Phase 1 a
// lost kick is picked up by the next tick seeing the same state; this exercises the path
// with the hint present.
// TestQuestionReportInterimKeepsArm pins the interim question report (docs/log/30):
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
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot44", Dir: t.TempDir(), Kind: session.KindClaude, Title: "質問検証"}
	session.WriteMeta(m)
	AddInstruction(m.Name, conv.ID, "operator")

	req := httptest.NewRequest(http.MethodPost, "/chat/report",
		strings.NewReader(`{"name":"slot44","kind":"question"}`))
	rec := httptest.NewRecorder()
	HandleChatReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got *ChatMessage
	for i := 0; i < 100 && got == nil; i++ {
		unlock := LockConv(conv.ID)
		c, err := LoadConv(conv.ID)
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
	// docs/log/28 P6: the card carries FACTS ONLY (it is a surface the user reads).
	// The instructions to the operator (answer with answer_session_question) are not
	// stored; they are added at the moment the prompt is assembled.
	if !strings.Contains(got.Content, "質問") || strings.Contains(got.Content, "answer_session_question") {
		t.Fatalf("question report card = %q", got.Content)
	}
	if prompt := ReportPromptFor(*got, "ja"); !strings.Contains(prompt, "answer_session_question") {
		t.Fatalf("the operator instructions are missing from the prompt: %q", prompt)
	}
	if !SessionReportPending(m.Name) {
		t.Fatal("interim question report must NOT consume the arm (the completion report is separate)")
	}
}

// TestReportHeadForAutoPilot pins the auto-pilot (自動走行) toggle: the interim question/plan
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
	q, pl := reportHeadFor("question", "", 0, "ja"), reportHeadFor("plan-approval", "", 0, "ja")
	if strings.Contains(q, "自動走行") || strings.Contains(pl, "自動走行") {
		t.Fatalf("auto-pilot text without opt-in:\nq=%q\npl=%q", q, pl)
	}
	if !strings.Contains(q, "answer_session_question") || !strings.Contains(pl, "respond_session_plan") {
		t.Fatalf("tool guidance missing:\nq=%q\npl=%q", q, pl)
	}

	if err := os.WriteFile(prefs, []byte(`{"assistantAutoPilot":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	q, pl = reportHeadFor("question", "", 0, "ja"), reportHeadFor("plan-approval", "", 0, "ja")
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
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot45", Dir: t.TempDir(), Kind: session.KindClaude, Title: "プラン検証"}
	session.WriteMeta(m)
	AddInstruction(m.Name, conv.ID, "operator")

	req := httptest.NewRequest(http.MethodPost, "/chat/report",
		strings.NewReader(`{"name":"slot45","kind":"plan-approval"}`))
	rec := httptest.NewRecorder()
	HandleChatReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	found := false
	for i := 0; i < 100 && !found; i++ {
		unlock := LockConv(conv.ID)
		c, err := LoadConv(conv.ID)
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
	if !SessionReportPending(m.Name) {
		t.Fatal("interim plan report must NOT consume the arm")
	}
}

// TestInterimDeliveryIsAwaitable pins the seam that keeps a test from writing into the
// USER'S real state: HandleChatReport delivers on a goroutine, so a test that returns
// without waiting lets `notice.Put` run after `t.Setenv("HOME")` has been restored and the
// notification lands in the REAL HOME's notification-outbox. Measured: a ghost
// notification for "プラン検証" showed up in the user's Console, and tapping it pointed at
// a conversation in a temp HOME that was already gone ("conversation not found").
//
// So the check does NOT poll: by the time WaitInterimDeliveries returns, both the
// conversation append and the notification write must be finished on the temp HOME side.
// Take the waiting seam out (delete the Add/Done) and this fails immediately, which is
// what makes the mutation detectable.
func TestInterimDeliveryIsAwaitable(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoTurn":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "slot46", Dir: t.TempDir(), Kind: session.KindClaude, Title: "配送待ち"}
	session.WriteMeta(m)
	AddInstruction(m.Name, conv.ID, "operator")

	req := httptest.NewRequest(http.MethodPost, "/chat/report",
		strings.NewReader(`{"name":"slot46","kind":"question"}`))
	rec := httptest.NewRecorder()
	HandleChatReport(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	WaitInterimDeliveries()

	// (1) The notification sits in the temp HOME's outbox (= it did not leak to the
	// real HOME).
	outbox := filepath.Join(home, ".config", "agent-fleet", "notification-outbox")
	ents, err := os.ReadDir(outbox)
	if err != nil || len(ents) != 1 {
		t.Fatalf("no notification in %s by the time the wait returned (err=%v, count=%d)", outbox, err, len(ents))
	}
	b, err := os.ReadFile(filepath.Join(outbox, ents[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Kind        string            `json:"kind"`
		DisplayName string            `json:"displayName"`
		Payload     map[string]string `json:"payload"`
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Kind != "session-report" || ev.DisplayName != m.Title || ev.Payload["conversation_id"] != conv.ID {
		t.Fatalf("notification body = %+v (does not point at the target conversation %s)", ev, conv.ID)
	}
	// (2) The append to the conversation has finished too.
	c, err := LoadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	for i := range c.Messages {
		if c.Messages[i].Role == "report" {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("report messages present when the wait returned = %d, want 1", reports)
	}
}

// TestSessionReportDeferredWhileSubagentBusy pins the premature-completion fix
// (docs/log/30, measured 2026-07-24 saga5uc): claude launches background subagents and Stops
// minutes before the instruction is actually done. That early answer-ready kick must
// NOT consume the one-shot arm — delivery waits until the subagent transcripts go
// stale and the session sits at idle, then fires exactly once.
// Re-read under docs/log/51 Phase 1: the "deferred waiter" special case is gone and
// SubagentBusy became BUSY EVIDENCE for the reconciler — the same semantics, with the
// decision gathered into one place.
// TestSessionReportIgnoresFalseIdle pins the delivery gate against the false-idle
// window (measured 2026-07-28 sqmconc/azw7wys): mid-turn, a think gap fires no hooks and
// the pane-based self-heal can remove the status marker; the bare LiveState then
// defaults to idle and the old waiter spent the one-shot arm on a turn that was still
// running — the real completion 27 minutes later kicked into armed=false and was
// silently dropped.
// Re-read under docs/log/51 Phase 1: the waiter is gone and its delivery condition folded
// into the predicate — no marker means UNKNOWN, not idle, and transcript freshness is busy
// evidence.
// TestHaltDisarmsReportOnlyWhenFlagged pins the halt/disarm contract: the MCP
// stop_session sends {"disarm_report":true} and must cancel the pending one-shot
// report (stop = instruction cancelled), while the Console's bodyless halt keeps the
// arm (a later resume + completion is still the instruction's completion). The
// sessions have no live tmux, exercising the already-stopped path — the disarm must
// not depend on liveness.
func TestInstrLedgerFileLocation(t *testing.T) {
	home := withTempHome(t)
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	AddInstruction("slot09", conv.ID, "operator")
	p := filepath.Join(home, ".config", "agent-fleet", "instr-ledger", "slot09.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected ledger at %s: %v", p, err)
	}
}

// TestReportHeadForTurnAborted covers the auto-resume-after-abort (中断時の自動再開)
// wording (docs/log/47): ON asks the operator to resume, OFF asks it to confirm with the
// user first, and past the cap the report escalates instead of asking for yet another
// resume. The language instruction is
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
	on := reportHeadFor(ReportKindAnswerReady, ReportReasonTurnAborted, 1, "ja")
	for _, want := range []string{"中断", "send_to_session", "言語", "破壊的"} {
		if !strings.Contains(on, want) {
			t.Fatalf("auto-resume head missing %q:\n%s", want, on)
		}
	}

	// OFF: confirm with the user before resuming, but still explain the interruption.
	if err := os.WriteFile(prefs, []byte(`{"assistantAutoResume":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	off := reportHeadFor(ReportKindAnswerReady, ReportReasonTurnAborted, 1, "ja")
	if !strings.Contains(off, "確認") || !strings.Contains(off, "言語") {
		t.Fatalf("auto-resume OFF head = %q", off)
	}
	if strings.Contains(off, "ON】") {
		t.Fatalf("OFF head advertises the ON mode:\n%s", off)
	}

	// Past the cap: escalate to the user, never ask for another resume — this is the
	// stop for an error that keeps recurring.
	capped := reportHeadFor(ReportKindAnswerReady, ReportReasonTurnAborted, MaxAutoResumeAttempts+1, "ja")
	if !strings.Contains(capped, "上限") || strings.Contains(capped, "send_to_session") {
		t.Fatalf("capped head must escalate without asking for a resume:\n%s", capped)
	}

	// A clean completion keeps its old wording — the abort branch must not leak into it.
	clean := reportHeadFor(ReportKindAnswerReady, "", 0, "ja")
	if strings.Contains(clean, "中断") {
		t.Fatalf("clean completion head changed:\n%s", clean)
	}
}

// TestAutoResumeCounter: consecutive aborts accumulate up to the cap and a clean
// completion resets the budget, so a session that recovers is not penalised later.
func TestAutoResumeCounter(t *testing.T) {
	withTempHome(t)
	if n := AutoResumeAttempts("slot20"); n != 0 {
		t.Fatalf("fresh session count = %d, want 0", n)
	}
	if n := bumpAutoResume("slot20"); n != 1 {
		t.Fatalf("first bump = %d, want 1", n)
	}
	if n := bumpAutoResume("slot20"); n != 2 {
		t.Fatalf("second bump = %d, want 2", n)
	}
	if n := AutoResumeAttempts("slot20"); n != 2 {
		t.Fatalf("read back = %d, want 2", n)
	}
	ResetAutoResume("slot20")
	if n := AutoResumeAttempts("slot20"); n != 0 {
		t.Fatalf("after reset = %d, want 0", n)
	}
	// Counters are per session.
	bumpAutoResume("slot21")
	if n := AutoResumeAttempts("slot20"); n != 0 {
		t.Fatalf("slot20 picked up slot21's count: %d", n)
	}
}
