package kiro

// read 層のユニットテスト: 起動コマンド組み立て・v2 JSONL 転写のパース（tool 出力の
// toolUseId 突合・Turn.Idx 単調 — agy 1ccb63e の教訓で必須）・TUI 文字列の状態分類・
// models JSON パース・cwd による sid 発見。フィクスチャは 2.14.1 の実測（docs/43）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildProgram(t *testing.T) {
	sid := "32595b50-8232-496c-8c30-e5669f5911cb"
	got := buildProgram("", "", "", sid)
	for _, want := range []string{"kiro-cli ", "chat", "--agent-engine v2", "--trust-all-tools", "--resume-id", sid} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("program %q lacks %q", got, want)
		}
	}
	// plan は bypass（--trust-all-tools）を外す。engine ピンと chat は残す。
	got = buildProgram("", "", "plan", sid)
	if strings.Contains(got, "--trust-all-tools") || !strings.Contains(got, "--agent-engine v2") ||
		!strings.Contains(got, "chat") {
		t.Errorf("plan program wrong: %q", got)
	}
	// model / effort を渡す。"auto" は無指定と同義（--model を付けない）。
	got = buildProgram("claude-sonnet-4.5", "high", "", sid)
	if !strings.Contains(got, "--model") || !strings.Contains(got, "claude-sonnet-4.5") ||
		!strings.Contains(got, "--effort") || !strings.Contains(got, "high") {
		t.Errorf("model/effort program wrong: %q", got)
	}
	if got := buildProgram("auto", "", "", sid); strings.Contains(got, "--model") {
		t.Errorf("auto must not emit --model: %q", got)
	}
	// fresh（resumeID 無し）は --resume-id を付けない。
	if got := buildProgram("", "", "", ""); strings.Contains(got, "--resume-id") {
		t.Errorf("fresh launch must not resume: %q", got)
	}
	t.Setenv("AGENT_KIRO_CMD", "echo override")
	if got := buildProgram("", "", "", sid); got != "echo override" {
		t.Errorf("override ignored: %q", got)
	}
}

// TestBuildProgramPinGuard: 既定バイナリの起動には毎回 `install-kiro --if-needed` が
// 前置される。未導入ユーザーの初回導入だけでなく、**versions.json のピンが上がった
// 時に home の ~/.local 版を追従させる**のがこのガードの役目（旧 `command -v ||` 形は
// 「不在」しか見ないため、一度入った kiro が永久に古いままだった）。`;` 連結なので
// 導入/更新に失敗しても既存バイナリでの起動は続く。AGENT_KIRO_BIN 差し替え時は付けない。
func TestBuildProgramPinGuard(t *testing.T) {
	got := buildProgram("", "", "", "")
	want := "workspace-agent install-kiro --if-needed; "
	if !strings.HasPrefix(got, want) {
		t.Errorf("program %q must start with %q", got, want)
	}
	if strings.Contains(got, "command -v kiro-cli") {
		t.Errorf("guard must not be conditional on presence (pin drift would never be fixed): %q", got)
	}
	t.Setenv("AGENT_KIRO_BIN", "/tmp/fake-kiro")
	if got := buildProgram("", "", "", ""); strings.Contains(got, "install-kiro") {
		t.Errorf("AGENT_KIRO_BIN override must skip the bootstrap: %q", got)
	}
}

// fixture: 実測 v2 JSONL（2.14.1）。PONG → 1..40 → shell tool（sleep 30 && echo done）で
// toolUse＋ToolResults（stdout="done\n"）→ 最終 text。末尾に走行中の Prompt を足す。
const transcriptFixture = `{"version":"v1","kind":"Prompt","data":{"message_id":"a1","content":[{"kind":"text","data":"Reply with exactly: PONG"}],"meta":{"timestamp":1784869360}}}
{"version":"v1","kind":"AssistantMessage","data":{"message_id":"a2","content":[{"kind":"text","data":"PONG"}]}}
{"version":"v1","kind":"Prompt","data":{"message_id":"a3","content":[{"kind":"text","data":"Run the shell command: sleep 30 && echo done"}],"meta":{"timestamp":1784869421}}}
{"version":"v1","kind":"AssistantMessage","data":{"message_id":"a4","content":[{"kind":"text","data":""},{"kind":"toolUse","data":{"toolUseId":"tooluse_X","name":"shell","input":{"command":"sleep 30 && echo done","__tool_use_purpose":"Run the sleep command"}}}]}}
{"version":"v1","kind":"ToolResults","data":{"message_id":"a5","content":[{"kind":"toolResult","data":{"toolUseId":"tooluse_X","content":[{"kind":"json","data":{"exit_status":"exit status: 0","stdout":"done\n","stderr":""}}],"status":"success"}}]}}
{"version":"v1","kind":"AssistantMessage","data":{"message_id":"a6","content":[{"kind":"text","data":"Command completed successfully."}]}}
{"version":"v1","kind":"Prompt","data":{"message_id":"a7","content":[{"kind":"text","data":"second"}],"meta":{"timestamp":1784869500}}}
`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "chat.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseTranscript(t *testing.T) {
	turns := parseTranscript(writeFixture(t, transcriptFixture))
	// user(PONG) / assistant(PONG) / user(shell) / assistant(tool+text 畳み) / user(second)
	if len(turns) != 5 {
		t.Fatalf("want 5 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "Reply with exactly: PONG" {
		t.Errorf("first user turn wrong: %+v", turns[0])
	}
	if turns[0].TS == "" {
		t.Errorf("user turn should carry a timestamp: %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Text != "PONG" {
		t.Errorf("first assistant turn wrong: %+v", turns[1])
	}
	// The tool turn folds toolUse + final text into ONE assistant turn, with the
	// ToolResults stdout attached to the tool part by toolUseId.
	a := turns[3]
	if a.Role != "assistant" {
		t.Fatalf("want assistant tool turn, got %+v", a)
	}
	var tool *struct{ Tool, Info, Output string }
	for _, p := range a.Parts {
		if p.Kind == "tool" {
			tool = &struct{ Tool, Info, Output string }{p.Tool, p.Info, p.Output}
		}
	}
	if tool == nil || tool.Tool != "shell" || tool.Info != "sleep 30 && echo done" {
		t.Errorf("tool part wrong: %+v", a.Parts)
	}
	if tool == nil || tool.Output != "done\n" {
		t.Errorf("tool output not attached from ToolResults: %+v", a.Parts)
	}
	if a.Text != "Command completed successfully." {
		t.Errorf("assistant text concat wrong: %q", a.Text)
	}
	if turns[4].Role != "user" || turns[4].Text != "second" {
		t.Errorf("trailing user turn wrong: %+v", turns[4])
	}
	// Idx は単調増加（Console の pendingEcho/MirrorView 契約 — 必須）。
	last := -1
	for i, tn := range turns {
		if tn.Idx <= last {
			t.Fatalf("Idx not monotonic at %d: %+v", i, turns)
		}
		last = tn.Idx
	}
}

func TestClassifyPane(t *testing.T) {
	cases := map[string]string{
		"kiro_default · auto · ◔ 3%    /tmp/x\n\n ask a question or describe a task ↵\n": "idle",
		" Kiro is working · Type to steer · Ctrl+S to queue\n":                           "working",
		" shell requires approval\n ❯ Yes, single permission\n":                          "question",
		"":                           "",
		"some unrelated boot text\n": "",
	}
	for in, want := range cases {
		if got := classifyPane(in); got != want {
			t.Errorf("classifyPane(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestClassifyPaneBodyQuote is the A-3 regression: an assistant answer that QUOTES a
// contract phrase in the conversation body must NOT spoof the state once the pane is
// back at the idle footer. classifyPane looks only at the footer window, and idle wins.
func TestClassifyPaneBodyQuote(t *testing.T) {
	// Body far above quotes "Kiro is working"; the live footer is idle.
	pane := `> You asked me to explain the footer.
> When busy, kiro shows "Kiro is working · Type to steer".
> That is the working indicator.

kiro_default · auto · ◔ 5%                         /work/a

 ask a question or describe a task ↵
                                             /copy to clipboard
`
	if got := classifyPane(pane); got != "idle" {
		t.Errorf("body quote of a working phrase must not spoof state: got %q, want idle", got)
	}
	// A working footer at the bottom is still detected even with body text above.
	working := `> Here is the plan.
Some earlier answer text.

 Kiro is working · Type to steer · Ctrl+S to queue
`
	if got := classifyPane(working); got != "working" {
		t.Errorf("real working footer not detected: got %q, want working", got)
	}
}

// fixture: 実測 `kiro-cli chat --list-models -f json`（縮約）。
const modelsFixture = `{"models":[
{"model_name":"auto","description":"Models chosen by task","model_id":"auto","context_window_tokens":1000000},
{"model_name":"claude-sonnet-4.5","description":"Claude Sonnet 4.5 model","model_id":"claude-sonnet-4.5","context_window_tokens":200000},
{"model_name":"claude-haiku-4.5","description":"The latest Claude Haiku model","model_id":"claude-haiku-4.5","context_window_tokens":200000}
],"default_model":"auto"}`

func TestParseModels(t *testing.T) {
	got, windows := parseModels([]byte(modelsFixture))
	if len(got) != 2 { // auto は除外
		t.Fatalf("want 2 models (auto excluded), got %+v", got)
	}
	want := map[string]string{
		"claude-sonnet-4.5": "Claude Sonnet 4.5 model",
		"claude-haiku-4.5":  "The latest Claude Haiku model",
	}
	for _, mc := range got {
		if mc.ID == "auto" {
			t.Errorf("auto must be excluded")
		}
		if want[mc.ID] != mc.Label {
			t.Errorf("model %q label = %q, want %q", mc.ID, mc.Label, want[mc.ID])
		}
	}
	// windows は auto を含め全モデルの context_window_tokens を保持する（pct→token 変換用）。
	wantWin := map[string]int{"auto": 1000000, "claude-sonnet-4.5": 200000, "claude-haiku-4.5": 200000}
	for id, w := range wantWin {
		if windows[id] != w {
			t.Errorf("window[%q] = %d, want %d", id, windows[id], w)
		}
	}
	// 壊れた JSON は非 nil 空リスト＋非 nil 空 window マップ（既定のみで安全側）。
	if list, win := parseModels([]byte("not json")); list == nil || len(list) != 0 || win == nil {
		t.Errorf("broken json must yield non-nil empty list+map: %+v %+v", list, win)
	}
}

func TestDisplayModel(t *testing.T) {
	cases := map[string]string{
		"":                  "Auto",
		"auto":              "Auto",
		"default":           "Auto",
		"claude-sonnet-4.5": "claude-sonnet-4.5",
		"glm-5":             "glm-5",
	}
	for in, want := range cases {
		if got := displayModel(in); got != want {
			t.Errorf("displayModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStampModelAssistantOnly(t *testing.T) {
	turns := parseTranscript(writeFixture(t, transcriptFixture))
	stampModel(turns, "claude-haiku-4.5")
	for _, tn := range turns {
		if tn.Role == "assistant" && tn.Model != "claude-haiku-4.5" {
			t.Errorf("assistant turn not stamped: %+v", tn)
		}
		if tn.Role == "user" && tn.Model != "" {
			t.Errorf("user turn must not carry a model: %+v", tn)
		}
	}
	// 空モデルは no-op（既存を汚さない）。
	turns2 := parseTranscript(writeFixture(t, transcriptFixture))
	stampModel(turns2, "")
	for _, tn := range turns2 {
		if tn.Model != "" {
			t.Errorf("empty model must not stamp: %+v", tn)
		}
	}
}

// baseTime is a fixed epoch the discovery tests hang timestamps off of.
var baseTime = time.Unix(1_700_000_000, 0).UTC()

// TestDiscoverSid verifies cwd-scoped, newest-wins discovery over a fake sessions dir.
func TestDiscoverSid(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // Home() = $HOME/.kiro
	sd := sessionsDir()
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	// mtime drives newest-wins; set it explicitly so the test doesn't depend on the
	// filesystem's timestamp resolution. createdMin sets the session's created_at (the
	// A-1 fence key), which is independent of file mtime.
	write := func(sid, cwd string, createdMin, mtimeMin int) {
		created := baseTime.Add(time.Duration(createdMin) * time.Minute)
		b, _ := json.Marshal(sessionMeta{SessionID: sid, Cwd: cwd, CreatedAt: created.Format(time.RFC3339)})
		p := filepath.Join(sd, sid+".json")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		when := baseTime.Add(time.Duration(mtimeMin) * time.Minute)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	write("old-sid", "/work/a", 0, 0)
	write("other-sid", "/work/b", 5, 5) // different cwd — must be ignored (newer, wrong cwd)
	write("new-sid", "/work/a", 3, 3)   // newest for /work/a
	// Unfenced (zero time): newest-mtime wins within the cwd.
	if got := discoverSid("/work/a", time.Time{}); got != "new-sid" {
		t.Errorf("discoverSid(/work/a) = %q, want new-sid", got)
	}
	if got := discoverSid("/work/none", time.Time{}); got != "" {
		t.Errorf("no session for cwd must yield empty, got %q", got)
	}
	// Trailing-slash / uncleaned cwd still matches.
	if got := discoverSid("/work/a/", time.Time{}); got != "new-sid" {
		t.Errorf("uncleaned cwd should match: got %q", got)
	}
}

// TestDiscoverSidFence is the A-1 regression: a predecessor session lingering in the
// same dir must NOT be adopted when the slot was created after it (recreate cuts a new
// slug into the same dir). Only sessions created at/after the fence qualify.
func TestDiscoverSidFence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	sd := sessionsDir()
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(sid string, createdMin, mtimeMin int) {
		created := baseTime.Add(time.Duration(createdMin) * time.Minute)
		b, _ := json.Marshal(sessionMeta{SessionID: sid, Cwd: "/work/a", CreatedAt: created.Format(time.RFC3339)})
		p := filepath.Join(sd, sid+".json")
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		when := baseTime.Add(time.Duration(mtimeMin) * time.Minute)
		_ = os.Chtimes(p, when, when)
	}
	// Predecessor created at t=0; the new slot is created at t=+10.
	write("predecessor", 0, 0)
	fence := baseTime.Add(10 * time.Minute)

	// During the fresh-launch window (kiro hasn't written its own .json yet), the ONLY
	// candidate is the predecessor — the fence must reject it so discovery returns "".
	if got := discoverSid("/work/a", fence); got != "" {
		t.Fatalf("fence must reject the pre-fence predecessor, got %q", got)
	}
	// Once kiro writes its own session (created after the fence), discovery finds it.
	write("mine", 11, 11)
	if got := discoverSid("/work/a", fence); got != "mine" {
		t.Errorf("post-fence session should be adopted, got %q", got)
	}
	// Even if the predecessor is later touched so its mtime is the NEWEST, the fence
	// still excludes it by created_at — mtime alone must not resurrect it.
	touch := baseTime.Add(20 * time.Minute)
	_ = os.Chtimes(filepath.Join(sd, "predecessor.json"), touch, touch)
	if got := discoverSid("/work/a", fence); got != "mine" {
		t.Errorf("predecessor with newer mtime must stay excluded by the fence, got %q", got)
	}
}

// 承認パネルの本文（docs/75 P5 の持ち越し）。ペインの文字列にしか無いので halt より
// 後では取れない — ここで固定するのは「取れるときに何を取るか」だけ。
func TestApprovalLine(t *testing.T) {
	pane := "some earlier output\n\n  shell requires approval\n  > Enter to allow\n"
	if got := approvalLine(pane); got != "shell requires approval" {
		t.Errorf("approvalLine = %q, want %q", got, "shell requires approval")
	}
	// 承認待ちでないフレームからは何も取らない（idle 句が最優先されるのは classifyPane と同じ）。
	if got := approvalLine("  ask a question or describe a task ↵\n"); got != "" {
		t.Errorf("idle のフレームから承認本文を取った: %q", got)
	}
	if got := approvalLine(""); got != "" {
		t.Errorf("空フレームから取った: %q", got)
	}
}
