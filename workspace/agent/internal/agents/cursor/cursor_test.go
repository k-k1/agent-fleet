package cursor

// read 層のユニットテスト: 起動コマンド組み立て・JSONL 転写のパース（Turn.Idx
// 単調 — agy 30c5e21 の教訓で必須）・live 状態分類・models パース。フィクスチャの
// 行形式は v2026.07.20 の実測（docs/40 実測記録）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProgram(t *testing.T) {
	id := "9eb73605-3f4a-4a46-84bc-35e6d300a9df"
	got := buildProgram("", "", id)
	for _, want := range []string{"cursor-agent ", "--disable-auto-update", "--force", "--trust", "--resume", id} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("program %q lacks %q", got, want)
		}
	}
	// plan は bypass（--force）を外し --plan を足す。--trust と自己更新封殺は残す。
	got = buildProgram("", "plan", id)
	if strings.Contains(got, "--force") || !strings.Contains(got, "--plan") ||
		!strings.Contains(got, "--trust") || !strings.Contains(got, "--disable-auto-update") {
		t.Errorf("plan program wrong: %q", got)
	}
	// model はそのまま。"auto" は無指定と同義（フラグを付けない）。
	got = buildProgram("claude-opus-4-8-thinking-high", "", id)
	if !strings.Contains(got, "--model") || !strings.Contains(got, "claude-opus-4-8-thinking-high") {
		t.Errorf("model program wrong: %q", got)
	}
	if got := buildProgram("auto", "", id); strings.Contains(got, "--model") {
		t.Errorf("auto must not emit --model: %q", got)
	}
	t.Setenv("AGENT_CURSOR_CMD", "echo override")
	if got := buildProgram("", "", id); got != "echo override" {
		t.Errorf("override ignored: %q", got)
	}
}

func TestNewChatID(t *testing.T) {
	a, err := newChatID()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newChatID()
	if a == b || len(a) != 36 || a[14] != '4' {
		t.Errorf("bad v4 uuids: %q %q", a, b)
	}
}

// fixture: 実測 JSONL（v2026.07.20 の -p ターン）。1 ユーザープロンプト →
// text+tool_use → 最終 text → turn_ended、の 1 ターン＋2 プロンプト目は走行中。
const transcriptFixture = `{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Thursday, Jul 23, 2026, 1:18 PM (UTC+9)</timestamp>\n<user_query>\nRun the shell command: echo hello123. Then say DONE.\n</user_query>"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"I'll run that echo command now."},{"type":"tool_use","name":"Shell","input":{"command":"echo hello123","description":"Echo hello123 to stdout"}}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"DONE"}]}}
{"type":"turn_ended","status":"success"}
{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nsecond\n</user_query>"}]}}
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
	if len(turns) != 3 { // user / assistant（2 行を 1 ターンに畳む）/ user（走行中）
		t.Fatalf("want 3 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "Run the shell command: echo hello123. Then say DONE." {
		t.Errorf("user turn wrong (unwrap failed?): %+v", turns[0])
	}
	a := turns[1]
	if a.Role != "assistant" {
		t.Fatalf("want assistant turn, got %+v", a)
	}
	// text / tool_use / text の 3 パート（複数 assistant 行を 1 ターンに集約）。
	if len(a.Parts) != 3 || a.Parts[1].Kind != "tool" || a.Parts[1].Tool != "Shell" ||
		a.Parts[1].Info != "Echo hello123 to stdout" {
		t.Errorf("tool part wrong: %+v", a.Parts)
	}
	if a.Text != "I'll run that echo command now.\n\nDONE" {
		t.Errorf("assistant text concat wrong: %q", a.Text)
	}
	if turns[2].Role != "user" || turns[2].Text != "second" {
		t.Errorf("second user turn wrong: %+v", turns[2])
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

func TestLiveStateClassify(t *testing.T) {
	// 走行中（最後の user 行が turn_ended 未閉）。
	if st := liveStateFromFile(writeFixture(t, transcriptFixture)); st != "working" {
		t.Errorf("want working, got %q", st)
	}
	// turn_ended まで揃えると idle。
	closed := transcriptFixture + `{"type":"turn_ended","status":"success"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, closed)); st != "idle" {
		t.Errorf("want idle, got %q", st)
	}
	// ファイル無し → ""（不明）。
	if st := liveStateFromFile(filepath.Join(t.TempDir(), "none.jsonl")); st != "" {
		t.Errorf("want empty, got %q", st)
	}
}

// fixture: 実測 `cursor-agent models` 出力（縮約）。
const modelsFixture = `Available models

auto - Auto (current, default)
gpt-5.3-codex-high - Codex 5.3 High
claude-opus-4-8-thinking-high - Opus 4.8 1M Thinking
cursor-grok-4.5-high - Cursor Grok 4.5
`

func TestParseModels(t *testing.T) {
	got := parseModels(modelsFixture)
	want := map[string]string{
		"gpt-5.3-codex-high":            "Codex 5.3 High",
		"claude-opus-4-8-thinking-high": "Opus 4.8 1M Thinking",
		"cursor-grok-4.5-high":          "Cursor Grok 4.5",
	}
	if len(got) != len(want) {
		t.Fatalf("want %d models, got %+v", len(want), got)
	}
	for _, mc := range got {
		if mc.ID == "auto" {
			t.Errorf("auto must be excluded")
		}
		if want[mc.ID] != mc.Label {
			t.Errorf("model %q label = %q, want %q", mc.ID, mc.Label, want[mc.ID])
		}
	}
	// ヘッダ・空行・注記混入は落とす。
	if got := parseModels("Available models\n\n"); len(got) != 0 {
		t.Errorf("header-only must yield empty: %+v", got)
	}
}
