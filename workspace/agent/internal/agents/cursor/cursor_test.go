package cursor

// read 層のユニットテスト: 起動コマンド組み立て・JSONL 転写のパース（Turn.Idx
// 単調 — agy 1ccb63e の教訓で必須）・live 状態分類・models パース。フィクスチャの
// 行形式は v2026.07.20 の実測（docs/40 実測記録）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProgram(t *testing.T) {
	id := "9eb73605-3f4a-4a46-84bc-35e6d300a9df"
	got := buildProgram("", "", id, true)
	for _, want := range []string{"cursor-agent ", "--disable-auto-update", "--force", "--trust", "--resume", id} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("program %q lacks %q", got, want)
		}
	}
	// plan は bypass（--force）を外し --plan を足す。--trust と自己更新封殺は残す。
	got = buildProgram("", "plan", id, false)
	if strings.Contains(got, "--force") || !strings.Contains(got, "--plan") ||
		!strings.Contains(got, "--trust") || !strings.Contains(got, "--disable-auto-update") {
		t.Errorf("plan program wrong: %q", got)
	}
	// model はそのまま。"auto" は無指定と同義（フラグを付けない）。
	got = buildProgram("claude-opus-4-8-thinking-high", "", id, true)
	if !strings.Contains(got, "--model") || !strings.Contains(got, "claude-opus-4-8-thinking-high") {
		t.Errorf("model program wrong: %q", got)
	}
	if got := buildProgram("auto", "", id, true); strings.Contains(got, "--model") {
		t.Errorf("auto must not emit --model: %q", got)
	}
	t.Setenv("AGENT_CURSOR_CMD", "echo override")
	if got := buildProgram("", "", id, true); got != "echo override" {
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

// fixture: 実測 `cursor-agent about`（Free アカウント・v2026.07.20）。
const aboutFreeFixture = `About Cursor CLI

CLI Version         2026.07.20-8cc9c0b
Model               Auto
Subscription Tier   Free
OS                  linux (x64)
`

const aboutProFixture = `About Cursor CLI

CLI Version         2026.07.20-8cc9c0b
Model               Auto
Subscription Tier   Pro
`

func TestAboutTierRe(t *testing.T) {
	m := aboutTierRe.FindStringSubmatch(aboutFreeFixture)
	if m == nil || !strings.EqualFold(strings.TrimSpace(m[1]), "free") {
		t.Errorf("Free tier not parsed: %v", m)
	}
	m = aboutTierRe.FindStringSubmatch(aboutProFixture)
	if m == nil || strings.EqualFold(strings.TrimSpace(m[1]), "free") {
		t.Errorf("Pro tier misread as free: %v", m)
	}
	// 書式ドリフト（該当行なし）は nil。
	if aboutTierRe.FindStringSubmatch("About Cursor CLI\n\nModel  Auto\n") != nil {
		t.Errorf("missing tier row must not match")
	}
}

func TestDisplayModel(t *testing.T) {
	cases := map[string]string{
		"":                        "Auto",
		"auto":                    "Auto",
		"default[]":               "Auto",
		"glm-5.2[reasoning=high]": "glm-5.2",
		"claude-opus-4-8[thinking=true,fast=false]": "claude-opus-4-8",
		"composer-2.5":                  "composer-2.5", // dash 形式はそのまま
		"claude-opus-4-8-thinking-high": "claude-opus-4-8-thinking-high",
	}
	for in, want := range cases {
		if got := displayModel(in); got != want {
			t.Errorf("displayModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStampModelAssistantOnly(t *testing.T) {
	turns := parseTranscript(writeFixture(t, transcriptFixture))
	stampModel(turns, "composer-2.5")
	for _, tn := range turns {
		if tn.Role == "assistant" && tn.Model != "composer-2.5" {
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

func TestFreeUsableModels(t *testing.T) {
	// Free では named model（gpt/claude/grok…）を隠し composer 系のみ残す。
	full := parseModels(`Available models

auto - Auto (current, default)
composer-2.5 - Composer 2.5
composer-2.5-fast - Composer 2.5 Fast
gpt-5.3-codex-high - Codex 5.3 High
claude-opus-4-8-thinking-high - Opus 4.8 1M Thinking
`)
	free := freeUsableModels(full)
	if len(free) != 2 {
		t.Fatalf("want 2 composer models, got %+v", free)
	}
	for _, m := range free {
		if !strings.HasPrefix(m.ID, "composer") {
			t.Errorf("non-composer leaked into Free catalog: %q", m.ID)
		}
	}
}

// docs/68: 転写から「編集したファイル」を拾えること。Write の形は実測
// （~/.cursor/projects/*/agent-transcripts/*.jsonl に {"path","contents"} で残っていた）。
func TestToolEditsPicksEditFamilyOnly(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		input    string
		wantFile string
		wantVerb string
		wantNew  string
	}{
		{"Write（実測の形）", "Write", `{"path":"/tmp/x/probe.txt","contents":"hello"}`, "/tmp/x/probe.txt", "", "hello"},
		{"Edit（claude 綴り）", "Edit", `{"path":"/a.ts","old_string":"a","new_string":"b"}`, "/a.ts", "", "b"},
		{"Edit（copilot 綴り）", "Edit", `{"file_path":"/a.ts","old_str":"a","new_str":"b"}`, "/a.ts", "", "b"},
		{"Delete は verb を明示する", "Delete", `{"path":"/a.ts"}`, "/a.ts", "delete", ""},
		// ⚠️ ここが肝。読み取り系を編集として拾うと、見ただけのファイルが
		// 「変更ファイル」に並ぶ＝一覧が黙って嘘をつく。
		{"Read は拾わない", "Read", `{"path":"/a.ts"}`, "", "", ""},
		{"Grep は拾わない", "Grep", `{"path":"/a.ts","pattern":"x"}`, "", "", ""},
		{"Shell は拾わない", "Shell", `{"command":"rm /a.ts"}`, "", "", ""},
		{"知らない名前は拾わない", "Frobnicate", `{"path":"/a.ts","contents":"x"}`, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, verb, edits := toolEdits(c.tool, json.RawMessage(c.input))
			if file != c.wantFile || verb != c.wantVerb {
				t.Fatalf("toolEdits = %q/%q, want %q/%q", file, verb, c.wantFile, c.wantVerb)
			}
			if c.wantNew == "" {
				return
			}
			if len(edits) != 1 || edits[0].New != c.wantNew {
				t.Fatalf("edits = %+v, want New=%q", edits, c.wantNew)
			}
		})
	}
}

// ACP 経路は名前ではなくプロトコルの kind で分類するので、抽出は形だけを見る。
func TestEditsFromInputIgnoresToolName(t *testing.T) {
	file, edits := editsFromInput(json.RawMessage(`{"path":"/a.ts","old_str":"a","new_str":"b"}`))
	if file != "/a.ts" || len(edits) != 1 || edits[0].Old != "a" {
		t.Fatalf("editsFromInput = %q %+v", file, edits)
	}
	if _, es := editsFromInput(json.RawMessage(`{"path":"/a.ts"}`)); len(es) != 0 {
		t.Fatalf("payload の無い入力から差分を作ってはいけない: %+v", es)
	}
}

// 権限確認あり（docs/76）。消えるのは --force だけで、--trust（未信頼ワークスペースの
// 確認スキップ）と自己更新封殺は残す — --trust を落とすと ACP でも TUI でも trust
// プロンプトで固まる（実測）。plan ではないので --plan も付かない。
func TestBuildProgramPermissionsOn(t *testing.T) {
	id := "9eb73605-3f4a-4a46-84bc-35e6d300a9df"
	got := buildProgram("", "", id, false)
	if strings.Contains(got, "--force") || strings.Contains(got, "--plan") {
		t.Errorf("permissions-on program must drop the bypass and stay off plan: %q", got)
	}
	for _, want := range []string{"--trust", "--disable-auto-update", "--resume"} {
		if !strings.Contains(got, want) {
			t.Errorf("permissions-on program %q lacks %q", got, want)
		}
	}
}
