package cursor

// Unit tests for the read layer: launch command assembly, JSONL transcript parsing
// (Turn.Idx monotonic — required by the agy 7354916 lesson), live state classification and
// models parsing. The fixtures' line shapes are measured on v2026.07.20 (docs/log/40).

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
	// plan drops the bypass (--force) and adds --plan, keeping --trust and the self-update
	// block.
	got = buildProgram("", "plan", id, false)
	if strings.Contains(got, "--force") || !strings.Contains(got, "--plan") ||
		!strings.Contains(got, "--trust") || !strings.Contains(got, "--disable-auto-update") {
		t.Errorf("plan program wrong: %q", got)
	}
	// The model is passed through. "auto" means the same as unset (no flag).
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

// The pane program launches the CLI with CI removed. With CI set, cursor draws no
// interactive UI (banner only, keystrokes ignored) and the session ends up a dead pane
// (ci_env.go). tmux's -e cannot unset a variable, so the shell's `env -u` is required.
func TestBuildProgramUnsetsCI(t *testing.T) {
	id := "9eb73605-3f4a-4a46-84bc-35e6d300a9df"
	got := buildProgram("", "", id, true)
	if !strings.HasPrefix(got, "env -u CI ") {
		t.Errorf("program must unset CI before exec: %q", got)
	}
	// Blanking it with `CI=` leaves the UI dead, so check it has not degraded to that.
	if strings.Contains(got, "CI=") {
		t.Errorf("blanking CI does not revive the UI; it must be unset: %q", got)
	}
}

func TestEnvWithoutCI(t *testing.T) {
	in := []string{"PATH=/usr/bin", "CI=true", "CI_NAME=github", "MY_CI=1", "HOME=/home/dev"}
	got := EnvWithoutCI(in)
	want := []string{"PATH=/usr/bin", "CI_NAME=github", "MY_CI=1", "HOME=/home/dev"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("EnvWithoutCI = %v, want %v", got, want)
	}
	// An empty CI kills it too (measured: presence decides, not the value), so drop that as
	// well.
	if got := EnvWithoutCI([]string{"CI=", "TZ=Asia/Tokyo"}); len(got) != 1 || got[0] != "TZ=Asia/Tokyo" {
		t.Errorf("empty CI must be dropped too: %v", got)
	}
	// The input is not mutated (callers pass os.Environ()).
	if len(in) != 5 || in[1] != "CI=true" {
		t.Errorf("input was mutated: %v", in)
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

// fixture: measured JSONL (a -p turn on v2026.07.20). One turn — user prompt →
// text+tool_use → final text → turn_ended — plus a second prompt still running.
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
	if len(turns) != 3 { // user / assistant (2 lines folded into 1 turn) / user (running)
		t.Fatalf("want 3 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "Run the shell command: echo hello123. Then say DONE." {
		t.Errorf("user turn wrong (unwrap failed?): %+v", turns[0])
	}
	a := turns[1]
	if a.Role != "assistant" {
		t.Fatalf("want assistant turn, got %+v", a)
	}
	// Three parts, text / tool_use / text (several assistant lines gathered into one turn).
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
	// Idx increases monotonically (the Console's pendingEcho/MirrorView contract — required).
	last := -1
	for i, tn := range turns {
		if tn.Idx <= last {
			t.Fatalf("Idx not monotonic at %d: %+v", i, turns)
		}
		last = tn.Idx
	}
}

func TestLiveStateClassify(t *testing.T) {
	// Running (the last user line is not closed by a turn_ended).
	if st := liveStateFromFile(writeFixture(t, transcriptFixture)); st != "working" {
		t.Errorf("want working, got %q", st)
	}
	// With the turn_ended in place it is idle.
	closed := transcriptFixture + `{"type":"turn_ended","status":"success"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, closed)); st != "idle" {
		t.Errorf("want idle, got %q", st)
	}
	// No file → "" (unknown).
	if st := liveStateFromFile(filepath.Join(t.TempDir(), "none.jsonl")); st != "" {
		t.Errorf("want empty, got %q", st)
	}
}

// fixture: measured `cursor-agent models` output (abridged).
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
	// Headers, blank lines and stray notes are dropped.
	if got := parseModels("Available models\n\n"); len(got) != 0 {
		t.Errorf("header-only must yield empty: %+v", got)
	}
}

// fixture: measured `cursor-agent about` (Free account, v2026.07.20).
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
	// A format drift (no such row) yields nil.
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
		"composer-2.5":                  "composer-2.5", // the dash form is left alone
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
	// An empty model is a no-op (existing values are not touched).
	turns2 := parseTranscript(writeFixture(t, transcriptFixture))
	stampModel(turns2, "")
	for _, tn := range turns2 {
		if tn.Model != "" {
			t.Errorf("empty model must not stamp: %+v", tn)
		}
	}
}

func TestFreeUsableModels(t *testing.T) {
	// On Free the named models (gpt/claude/grok…) are hidden and only the composer family
	// remains.
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

// docs/log/68: the edited files must be recoverable from the transcript. The shape of Write
// is measured (~/.cursor/projects/*/agent-transcripts/*.jsonl held {"path","contents"}).
func TestToolEditsPicksEditFamilyOnly(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		input    string
		wantFile string
		wantVerb string
		wantNew  string
	}{
		{"Write (the measured shape)", "Write", `{"path":"/tmp/x/probe.txt","contents":"hello"}`, "/tmp/x/probe.txt", "", "hello"},
		{"Edit (claude spelling)", "Edit", `{"path":"/a.ts","old_string":"a","new_string":"b"}`, "/a.ts", "", "b"},
		{"Edit (copilot spelling)", "Edit", `{"file_path":"/a.ts","old_str":"a","new_str":"b"}`, "/a.ts", "", "b"},
		{"Delete states the verb explicitly", "Delete", `{"path":"/a.ts"}`, "/a.ts", "delete", ""},
		// The crux: picking up a read tool as an edit would list a file that was merely
		// looked at as a changed file — the list would silently lie.
		{"Read is not picked up", "Read", `{"path":"/a.ts"}`, "", "", ""},
		{"Grep is not picked up", "Grep", `{"path":"/a.ts","pattern":"x"}`, "", "", ""},
		{"Shell is not picked up", "Shell", `{"command":"rm /a.ts"}`, "", "", ""},
		{"an unknown name is not picked up", "Frobnicate", `{"path":"/a.ts","contents":"x"}`, "", "", ""},
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

// The ACP path classifies by the protocol's kind rather than by name, so extraction looks
// only at the shape.
func TestEditsFromInputIgnoresToolName(t *testing.T) {
	file, edits := editsFromInput(json.RawMessage(`{"path":"/a.ts","old_str":"a","new_str":"b"}`))
	if file != "/a.ts" || len(edits) != 1 || edits[0].Old != "a" {
		t.Fatalf("editsFromInput = %q %+v", file, edits)
	}
	if _, es := editsFromInput(json.RawMessage(`{"path":"/a.ts"}`)); len(es) != 0 {
		t.Fatalf("an input without a payload must not produce edits: %+v", es)
	}
}

// Permission prompts on (docs/log/76). Only --force goes away; --trust (which skips the
// confirmation for an untrusted workspace) and the self-update block stay — dropping --trust
// wedges both ACP and TUI on the trust prompt (measured). This is not plan mode, so --plan
// is absent too.
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
