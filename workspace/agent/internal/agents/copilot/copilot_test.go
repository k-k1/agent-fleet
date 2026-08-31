package copilot

// read 層のユニットテスト: 起動コマンド組み立て・trust 事前追記（コメント行
// 保存）・events.jsonl のパース（Turn.Idx 単調 — agy 7354916 の教訓で必須）・
// live 状態分類。fixture のイベント形は v1.0.73 実測（docs/log/36）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildProgram(t *testing.T) {
	sid := "3336143a-bf0b-4db3-a0b8-15957462ce0c"
	got := buildProgram("", "", "", sid, true)
	for _, want := range []string{"copilot ", "--allow-all", "--no-remote ", "--no-remote-export", "--session-id", sid} {
		if !strings.Contains(got+" ", want) {
			t.Errorf("program %q lacks %q", got, want)
		}
	}
	// plan は bypass（--allow-all）を外し --mode plan を足す（agy と同じ判断）。
	got = buildProgram("", "", "plan", sid, false)
	if strings.Contains(got, "--allow-all") || !strings.Contains(got, "--mode plan") {
		t.Errorf("plan program wrong: %q", got)
	}
	// model/effort はそのまま。"auto" は無指定と同義（フラグを付けない）。
	got = buildProgram("claude-sonnet-4.6", "high", "", sid, true)
	if !strings.Contains(got, "--model") || !strings.Contains(got, "claude-sonnet-4.6") ||
		!strings.Contains(got, "--effort") || !strings.Contains(got, "high") {
		t.Errorf("model/effort program wrong: %q", got)
	}
	if got := buildProgram("auto", "", "", sid, true); strings.Contains(got, "--model") {
		t.Errorf("auto must not emit --model: %q", got)
	}
	// Auto rejects --effort (Free's only model), so effort must be suppressed unless a
	// concrete model is set — else the session errors on launch.
	if got := buildProgram("auto", "high", "", sid, true); strings.Contains(got, "--effort") {
		t.Errorf("auto+effort must not emit --effort: %q", got)
	}
	if got := buildProgram("", "high", "", sid, true); strings.Contains(got, "--effort") {
		t.Errorf("default(auto)+effort must not emit --effort: %q", got)
	}
	t.Setenv("AGENT_COPILOT_CMD", "echo override")
	if got := buildProgram("", "", "", sid, true); got != "echo override" {
		t.Errorf("override ignored: %q", got)
	}
}

func TestEnsureFolderTrusted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	// copilot 実物の config.json はコメント行つき JSONC 風 — 保存されること。
	seed := "// User settings belong in settings.json.\n// This file is managed automatically.\n" +
		"{\n  \"firstLaunchAt\": \"2026-07-21T01:42:30.993Z\"\n}\n"
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	EnsureFolderTrusted("/work/a")
	EnsureFolderTrusted("/work/a") // idempotent
	EnsureFolderTrusted("/work/b")
	b, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "// User settings belong in settings.json.") {
		t.Errorf("comment header lost:\n%s", s)
	}
	if strings.Count(s, "/work/a") != 1 || !strings.Contains(s, "/work/b") ||
		!strings.Contains(s, "firstLaunchAt") {
		t.Errorf("trustedFolders wrong:\n%s", s)
	}
	// ファイルが無くても作れる。
	home2 := t.TempDir()
	t.Setenv("COPILOT_HOME", home2)
	EnsureFolderTrusted("/work/c")
	if b, _ := os.ReadFile(filepath.Join(home2, "config.json")); !strings.Contains(string(b), "/work/c") {
		t.Errorf("fresh config not written: %q", b)
	}
}

// fixture: v1.0.73 実測イベント（縮約）。1 ユーザープロンプト → ツール 1 回 →
// 応答、の 1 ターン＋2 プロンプト目は走行中（turn_end 無し）。
const eventsFixture = `{"type":"session.start","data":{"sessionId":"s1"},"timestamp":"2026-07-21T01:00:00Z"}
{"type":"system.message","data":{"role":"system","content":"sys"},"timestamp":"2026-07-21T01:00:01Z"}
{"type":"user.message","data":{"content":"run echo","transformedContent":"<x>run echo</x>"},"timestamp":"2026-07-21T01:00:02Z"}
{"type":"assistant.turn_start","data":{"turnId":"0","model":"gpt-5-mini"},"timestamp":"2026-07-21T01:00:03Z"}
{"type":"tool.execution_start","data":{"toolCallId":"t1","toolName":"bash","arguments":{"command":"echo hi","description":"Run echo"},"model":"gpt-5-mini","turnId":"0"},"timestamp":"2026-07-21T01:00:04Z"}
{"type":"tool.execution_complete","data":{"toolCallId":"t1","success":true,"result":{"content":"hi\n"}},"timestamp":"2026-07-21T01:00:05Z"}
{"type":"assistant.message","data":{"messageId":"m1","model":"gpt-5-mini","content":"done","outputTokens":42,"turnId":"0"},"timestamp":"2026-07-21T01:00:06Z"}
{"type":"assistant.turn_end","data":{"turnId":"0","model":"gpt-5-mini"},"timestamp":"2026-07-21T01:00:07Z"}
{"type":"user.message","data":{"content":"second"},"timestamp":"2026-07-21T01:00:08Z"}
{"type":"assistant.turn_start","data":{"turnId":"1","model":"gpt-5-mini"},"timestamp":"2026-07-21T01:00:09Z"}
`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseEvents(t *testing.T) {
	turns := parseEvents(writeFixture(t, eventsFixture))
	if len(turns) != 3 { // user / assistant / user（走行中 turn はまだ空）
		t.Fatalf("want 3 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "run echo" {
		t.Errorf("user turn wrong: %+v", turns[0])
	}
	a := turns[1]
	if a.Role != "assistant" || a.Model != "gpt-5-mini" || a.OutTok != 42 {
		t.Errorf("assistant turn wrong: %+v", a)
	}
	if len(a.Parts) != 2 || a.Parts[0].Kind != "tool" || a.Parts[0].Tool != "bash" ||
		a.Parts[0].Info != "Run echo" || !strings.Contains(a.Parts[0].Output, "hi") {
		t.Errorf("tool part wrong: %+v", a.Parts)
	}
	if a.Parts[1].Kind != "text" || a.Parts[1].Text != "done" || a.Text != "done" {
		t.Errorf("text part wrong: %+v", a)
	}
	// 1 ターン = turn_start…turn_end の span を 1 Turn に畳むので TS だけでは「開始」に
	// しかならない。ミラーのフッターが出すのは着地時刻なので EndTS が要る。
	if a.TS != "2026-07-21T01:00:03Z" {
		t.Errorf("assistant TS = %q, want the turn_start time", a.TS)
	}
	if a.EndTS != "2026-07-21T01:00:07Z" {
		t.Errorf("assistant EndTS = %q, want the turn_end time", a.EndTS)
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
	// 走行中（最後の turn_start が未閉）。
	if st := liveStateFromFile(writeFixture(t, eventsFixture)); st != "working" {
		t.Errorf("want working, got %q", st)
	}
	// 完了まで揃えると idle。
	closed := eventsFixture + `{"type":"assistant.turn_end","data":{"turnId":"1"},"timestamp":"2026-07-21T01:00:10Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, closed)); st != "idle" {
		t.Errorf("want idle, got %q", st)
	}
	// 未完了の permission.requested は question（走行より優先）。
	q := closed + `{"type":"user.message","data":{"content":"x"},"timestamp":"2026-07-21T01:00:11Z"}
{"type":"permission.requested","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:12Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, q)); st != "question" {
		t.Errorf("want question, got %q", st)
	}
	// completed が来たら working に戻る。
	done := q + `{"type":"permission.completed","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:13Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, done)); st != "working" {
		t.Errorf("want working after perm completed, got %q", st)
	}
	// graceful shutdown は全てを畳む。
	sd := q + `{"type":"session.shutdown","data":{},"timestamp":"2026-07-21T01:00:14Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, sd)); st != "idle" {
		t.Errorf("want idle after shutdown, got %q", st)
	}
	// ファイル無し → ""（不明）。
	if st := liveStateFromFile(filepath.Join(t.TempDir(), "none.jsonl")); st != "" {
		t.Errorf("want empty, got %q", st)
	}
}

func TestNewSessionID(t *testing.T) {
	a, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newSessionID()
	if a == b || len(a) != 36 || a[14] != '4' {
		t.Errorf("bad v4 uuids: %q %q", a, b)
	}
}

// fixture: 実 TUI /model キャプチャ（v1.0.73、Free プラン）の縮約。バナー＋
// 全モデル行＋装飾（❯/✓/スクロールバー/フッタ）。
const modelPickerFree = `   Auto routes based on your task, real-time system health, and model performance. Learn More
 Your Copilot Free plan currently includes only Auto, which automatically selects the best available model
 for each task.
   Model
 ❯ Auto ✓                                                                                                   █
   gpt-5.6-sol                                                                                              █
   claude-sonnet-4.6                                                                                        █
 ❯  Search models…
 ↑/↓ to navigate · enter to select · esc to cancel`

const modelPickerPaid = `   Auto routes based on your task, real-time system health, and model performance. Learn More
   Model
 ❯ Auto ✓                                                                                                   █
   gpt-5.6-sol                                                                                              █
   gpt-5.4-mini                                                                                             █
   gpt-5-mini                                                                                               ┃
   claude-sonnet-4.6                                                                                        █
   claude-fable-5                                                                                           █
   gemini-3.1-pro-preview                                                                                   █
   kimi-k2.7-code                                                                                           █
 ❯  Search models…
 ↑/↓ to navigate · enter to select · esc to cancel`

func TestParseModelPicker(t *testing.T) {
	// Free 系バナー → Auto のみ（空リスト、非 nil）。
	if got := parseModelPicker(modelPickerFree); len(got) != 0 {
		t.Errorf("free plan must yield empty catalog, got %+v", got)
	}
	// バナー無し → ピッカー行がそのままカタログ（Auto/装飾/フッタは除外）。
	got := parseModelPicker(modelPickerPaid)
	want := []string{"gpt-5.6-sol", "gpt-5.4-mini", "gpt-5-mini", "claude-sonnet-4.6", "claude-fable-5", "gemini-3.1-pro-preview", "kimi-k2.7-code"}
	if len(got) != len(want) {
		t.Fatalf("want %d models, got %+v", len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id || got[i].Label != id || len(got[i].Efforts) == 0 {
			t.Errorf("row %d: want %q, got %+v", i, id, got[i])
		}
	}
	// 会話文などの混入行は id 形でないため拾わない。
	if got := parseModelPicker("   Model\n   run echo and tell me\n ❯  Search models…"); len(got) != 0 {
		t.Errorf("prose row leaked into catalog: %+v", got)
	}
}

// docs/log/68: events.jsonl から「編集したファイル」を拾えること。`edit` の形は実測
// （~/.copilot/session-state/*/events.jsonl に {"path","old_str","new_str"} で残っていた）。
func TestToolEditsPicksEditFamilyOnly(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		args     string
		wantFile string
		wantOld  string
	}{
		{"edit（実測の形）", "edit", `{"path":"/a.tsx","old_str":"import x","new_str":"import y"}`, "/a.tsx", "import x"},
		{"create は全追加", "create", `{"path":"/new.ts","content":"hello"}`, "/new.ts", ""},
		// ⚠️ view を拾うと、読んだだけのファイルが「変更ファイル」に並ぶ。
		{"view は拾わない", "view", `{"path":"/a.tsx"}`, "", ""},
		{"bash は拾わない", "bash", `{"command":"rm /a.tsx"}`, "", ""},
		{"grep は拾わない", "grep", `{"path":"/a.tsx","pattern":"x"}`, "", ""},
		{"知らない名前は拾わない", "frobnicate", `{"path":"/a.tsx","content":"x"}`, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, _, edits := toolEdits(c.tool, json.RawMessage(c.args))
			if file != c.wantFile {
				t.Fatalf("toolEdits file = %q, want %q", file, c.wantFile)
			}
			if c.wantFile == "" {
				return
			}
			if len(edits) != 1 || edits[0].Old != c.wantOld {
				t.Fatalf("edits = %+v, want Old=%q", edits, c.wantOld)
			}
		})
	}
}

// 持ち越し（docs/log/75 P5）は「何の許可を求めていたか」を出す。events.jsonl の
// スキーマは版で動くので**取れたら使う**扱いで、取れなくても許可待ちの判定そのものは
// requestId だけで成立すること（detail が空でも state は question のまま）。
func TestPendingPermissionDetail(t *testing.T) {
	base := `{"type":"user.message","data":{"content":"x"},"timestamp":"2026-07-21T01:00:11Z"}` + "\n"
	withDetail := base + `{"type":"permission.requested","data":{"requestId":"p1","command":"npm ci"},"timestamp":"2026-07-21T01:00:12Z"}` + "\n"
	if st, d := liveStateDetailFromFile(writeFixture(t, withDetail)); st != "question" || d != "npm ci" {
		t.Errorf("liveStateDetailFromFile = (%q,%q), want (question,npm ci)", st, d)
	}
	// 対象名が載っていない版でも許可待ちは許可待ち。
	bare := base + `{"type":"permission.requested","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:12Z"}` + "\n"
	if st, d := liveStateDetailFromFile(writeFixture(t, bare)); st != "question" || d != "" {
		t.Errorf("liveStateDetailFromFile = (%q,%q), want (question,\"\")", st, d)
	}
	// 完了したものの detail を引きずらない。
	done := withDetail + `{"type":"permission.completed","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:13Z"}` + "\n"
	if st, d := liveStateDetailFromFile(writeFixture(t, done)); st == "question" || d != "" {
		t.Errorf("完了後も許可待ちのまま: (%q,%q)", st, d)
	}
}

// 権限確認あり（docs/log/76）。消えるのは --allow-all だけ。--no-remote / --no-remote-export
// （会話のフリート外流出と二重操縦の防止）は権限確認とは別軸なので残る。
func TestBuildProgramPermissionsOn(t *testing.T) {
	sid := "3336143a-bf0b-4db3-a0b8-15957462ce0c"
	got := buildProgram("", "", "", sid, false)
	if strings.Contains(got, "--allow-all") || strings.Contains(got, "--mode plan") {
		t.Errorf("permissions-on program must drop the bypass and stay off plan: %q", got)
	}
	for _, want := range []string{"--no-remote", "--no-remote-export", "--session-id"} {
		if !strings.Contains(got, want) {
			t.Errorf("permissions-on program %q lacks %q", got, want)
		}
	}
}
