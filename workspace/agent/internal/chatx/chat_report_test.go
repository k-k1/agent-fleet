package chatx

import (
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
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// TestInstrLedgerRoundTrip is the v1 TestArmSessionReportRoundTrip read through the
// Phase 2 ledger (docs/log/51): 投入は行の**追加**で、宛先の無い指示は行にならない。
// 決定的な違いが最後の2行 — v1 の re-arm は前の指示の bit を上書きしたが、行は増える。
func TestInstrLedgerRoundTrip(t *testing.T) {
	withTempHome(t)
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	// Unknown conversation id → no row (宛先の無い行は作らない).
	if id := AddInstruction("slot01", RandUUID(), "operator"); id != "" {
		t.Fatal("dangling conversation id へ行を立てた")
	}
	// Invalid session name → no row.
	if id := AddInstruction("bad/../name", conv.ID, "operator"); id != "" {
		t.Fatal("不正なセッション名で行を立てた")
	}
	if SessionReportPending("slot01") {
		t.Fatal("行が無いのに未報告扱い")
	}

	id1 := AddInstruction("slot01", conv.ID, "operator")
	if id1 == "" || !SessionReportPending("slot01") {
		t.Fatalf("投入で pending 行ができていない (id=%q)", id1)
	}
	rows := openInstrRows("slot01")
	if len(rows) != 1 || rows[0].Conv != conv.ID || rows[0].State != instrPending ||
		rows[0].Source != "operator" || rows[0].Cursor.At == "" {
		t.Fatalf("row = %+v", rows)
	}

	// 2件目の指示は**追加**される（v1 の re-arm は1bitの上書きだった＝穴A）。
	id2 := AddInstruction("slot01", conv.ID, "operator")
	if rows := openInstrRows("slot01"); len(rows) != 2 {
		t.Fatalf("2件目の指示で行が %d 件（上書きされた）", len(rows))
	}

	// 1件目だけを報告 → その行だけ閉じ、2件目は open のまま。
	markInstrReported("slot01", []string{id1}, time.Now())
	rows = openInstrRows("slot01")
	if len(rows) != 1 || rows[0].ID != id2 {
		t.Fatalf("先行指示の報告が後行指示を巻き添えにした: %+v", rows)
	}
	if !SessionReportPending("slot01") {
		t.Fatal("後行指示が残っているのに未報告なしと判定された")
	}
}

// TestInstrLedgerStateMachine pins the row's state machine (docs/log/51 §データモデル):
// pending → interim_reported（非消費）→ reported → reopened → reported、および
// stop_session の cancelled。閉じた行が勝手に開かない・上限で reopen が止まることも。
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
		t.Fatalf("初期状態 = %q", got)
	}

	// interim（質問）は**非消費**: 状態は進むが行は open のまま — 完了報告の義務は残る。
	markInstrInterim("slot02", "question", time.Now())
	if got := stateOf("slot02", id); got != instrInterim {
		t.Fatalf("interim 後 = %q", got)
	}
	if !SessionReportPending("slot02") {
		t.Fatal("interim 報告が完了のワンショットを食った（v1 の実測不具合）")
	}
	markInstrInterim("slot02", "plan-approval", time.Now())
	rows := ReadInstrRows("slot02")
	if rows[0].Interim.QuestionAt == "" || rows[0].Interim.PlanAt == "" {
		t.Fatalf("interim の既報記録が無い: %+v", rows[0])
	}

	markInstrReported("slot02", []string{id}, time.Now())
	if got := stateOf("slot02", id); got != instrReported {
		t.Fatalf("完了報告後 = %q", got)
	}
	if SessionReportPending("slot02") {
		t.Fatal("reported の行がまだ未報告扱い")
	}

	// 補償（§Phase 3 が引く遷移）: reported → reopened → reported。
	if !reopenInstrRow("slot02", id) {
		t.Fatal("reported 行を reopen できない")
	}
	if got := stateOf("slot02", id); got != instrReopened || !SessionReportPending("slot02") {
		t.Fatalf("reopen 後 = %q pending=%v", stateOf("slot02", id), SessionReportPending("slot02"))
	}
	markInstrReported("slot02", []string{id}, time.Now())
	if got := stateOf("slot02", id); got != instrReported {
		t.Fatalf("再報告後 = %q", got)
	}
	// reopen は行あたり instrReopenMax 回まで（判定が振動している行を打ち切る）。
	for i := 1; i < instrReopenMax; i++ {
		if !reopenInstrRow("slot02", id) {
			t.Fatalf("%d 回目の reopen が拒否された", i+1)
		}
		markInstrReported("slot02", []string{id}, time.Now())
	}
	if reopenInstrRow("slot02", id) {
		t.Fatalf("reopen 上限（%d）を超えて開き直した", instrReopenMax)
	}

	// stop_session（disarm）は open な行を cancelled にする。cancelled は開き直らない。
	id2 := AddInstruction("slot02", conv.ID, "operator")
	if n := cancelInstructions("slot02"); n != 1 {
		t.Fatalf("cancel した行数 = %d, want 1", n)
	}
	if got := stateOf("slot02", id2); got != instrCancelled {
		t.Fatalf("cancel 後 = %q", got)
	}
	if SessionReportPending("slot02") {
		t.Fatal("cancelled の行がまだ報告義務を持っている")
	}
	markInstrReported("slot02", []string{id2}, time.Now())
	if got := stateOf("slot02", id2); got != instrCancelled {
		t.Fatalf("cancelled が報告で上書きされた: %q", got)
	}
}

// TestMigrateReportArms covers the Phase 2 migration (docs/log/51 §移行): 起動時に v1 の
// armed=true を1行へ変換し、変換元のファイルは消す（再起動のたびに行が増えないこと）。
func TestMigrateReportArms(t *testing.T) {
	withTempHome(t)
	conv := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
	_ = reportLinks.Write("slot03", reportLink{Conv: conv.ID, Armed: true, At: at})
	_ = reportLinks.Write("slot04", reportLink{Conv: conv.ID, Armed: false, At: at}) // 消費済み

	MigrateReportArms()

	rows := openInstrRows("slot03")
	if len(rows) != 1 || rows[0].Conv != conv.ID || rows[0].Cursor.At != at {
		t.Fatalf("移行された行 = %+v", rows)
	}
	if SessionReportPending("slot04") {
		t.Fatal("armed でない v1 レコードから行を作った")
	}
	if _, ok := reportLinks.Read("slot03"); ok {
		t.Fatal("移行元の v1 ファイルが残っている（再起動のたびに行が増える）")
	}
	MigrateReportArms() // 2回目は何もしない
	if n := len(openInstrRows("slot03")); n != 1 {
		t.Fatalf("再移行で行が %d 件に増えた", n)
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
// → kickSessionReport → POST /chat/report（= リコンサイラの起床ヒント）→ tick の
// settle → the 【セッション報告】 card in the operator's conversation. Driven in the
// incident's exact shape — the pane heal wiped the "working" marker before Stop fired —
// which used to end in silence. docs/log/51 Phase 1 では kick が消えても次の tick が同じ
// 状態を見て拾う（ここではヒント有りの経路を通す）。
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
	// docs/log/28 P6: カードは**事実だけ**（利用者が読む面）。オペレーターへの指示
	// （answer_session_question で答えろ）は保存されず、プロンプトを組む瞬間に足される。
	if !strings.Contains(got.Content, "質問") || strings.Contains(got.Content, "answer_session_question") {
		t.Fatalf("question report card = %q", got.Content)
	}
	if prompt := ReportPromptFor(*got, "ja"); !strings.Contains(prompt, "answer_session_question") {
		t.Fatalf("オペレーターへの指示がプロンプトに乗っていない: %q", prompt)
	}
	if !SessionReportPending(m.Name) {
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

// TestSessionReportDeferredWhileSubagentBusy pins the premature-completion fix
// (docs/log/30, 実測 2026-07-24 saga5uc): claude launches background subagents and Stops
// minutes before the instruction is actually done. That early answer-ready kick must
// NOT consume the one-shot arm — delivery waits until the subagent transcripts go
// stale and the session sits at idle, then fires exactly once.
// docs/log/51 Phase 1 の読み替え: 「保留 waiter」という特例は消え、SubagentBusy は
// リコンサイラの **busy 証拠** になった（意味論は同じ — 判定が1か所に集まっただけ）。
// TestSessionReportIgnoresFalseIdle pins the delivery gate against the false-idle
// window (実測 2026-07-28 sqmconc/azw7wys): mid-turn, a think gap fires no hooks and
// the pane-based self-heal can remove the status marker; the bare LiveState then
// defaults to idle and the old waiter spent the one-shot arm on a turn that was still
// running — the real completion 27 minutes later kicked into armed=false and was
// silently dropped.
// docs/log/51 Phase 1 の読み替え: waiter は消え、その配送条件は述語に畳まれた —
// **無マーカーは「不明」であって idle ではない**、そして transcript の鮮度は busy 証拠。
// （旧名 TestSessionReportWaiterIgnoresFalseIdle）
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

// TestReportHeadForTurnAborted covers the 中断時の自動再開 wording (docs/log/47): ON asks the
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
