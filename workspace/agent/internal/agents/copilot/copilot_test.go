package copilot

// Unit tests for the read layer: launch command assembly, pre-registering trust (preserving
// comment lines), events.jsonl parsing (Turn.Idx monotonic — required by the agy 7354916
// lesson) and live state classification. The fixtures' event shapes are measured on v1.0.73
// (docs/log/36).

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
	// plan drops the bypass (--allow-all) and adds --mode plan (the same call as for agy).
	got = buildProgram("", "", "plan", sid, false)
	if strings.Contains(got, "--allow-all") || !strings.Contains(got, "--mode plan") {
		t.Errorf("plan program wrong: %q", got)
	}
	// model/effort are passed through. "auto" means the same as unset (no flag).
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
	// The real copilot config.json is JSONC-ish with comment lines — they must survive.
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
	// It can create the file when none exists.
	home2 := t.TempDir()
	t.Setenv("COPILOT_HOME", home2)
	EnsureFolderTrusted("/work/c")
	if b, _ := os.ReadFile(filepath.Join(home2, "config.json")); !strings.Contains(string(b), "/work/c") {
		t.Errorf("fresh config not written: %q", b)
	}
}

// fixture: measured v1.0.73 events (abridged). One turn — user prompt → one tool call →
// answer — plus a second prompt still running (no turn_end).
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
	if len(turns) != 3 { // user / assistant / user (the running turn is still empty)
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
	// One Turn folds the whole turn_start…turn_end span, so TS alone only marks the start.
	// The mirror's footer shows the landing time, which is what EndTS is for.
	if a.TS != "2026-07-21T01:00:03Z" {
		t.Errorf("assistant TS = %q, want the turn_start time", a.TS)
	}
	if a.EndTS != "2026-07-21T01:00:07Z" {
		t.Errorf("assistant EndTS = %q, want the turn_end time", a.EndTS)
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
	// Running (the last turn_start is not closed).
	if st := liveStateFromFile(writeFixture(t, eventsFixture)); st != "working" {
		t.Errorf("want working, got %q", st)
	}
	// With the completion in place it is idle.
	closed := eventsFixture + `{"type":"assistant.turn_end","data":{"turnId":"1"},"timestamp":"2026-07-21T01:00:10Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, closed)); st != "idle" {
		t.Errorf("want idle, got %q", st)
	}
	// An unfinished permission.requested is question (it outranks working).
	q := closed + `{"type":"user.message","data":{"content":"x"},"timestamp":"2026-07-21T01:00:11Z"}
{"type":"permission.requested","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:12Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, q)); st != "question" {
		t.Errorf("want question, got %q", st)
	}
	// Once completed arrives it goes back to working.
	done := q + `{"type":"permission.completed","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:13Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, done)); st != "working" {
		t.Errorf("want working after perm completed, got %q", st)
	}
	// A graceful shutdown closes everything.
	sd := q + `{"type":"session.shutdown","data":{},"timestamp":"2026-07-21T01:00:14Z"}` + "\n"
	if st := liveStateFromFile(writeFixture(t, sd)); st != "idle" {
		t.Errorf("want idle after shutdown, got %q", st)
	}
	// No file → "" (unknown).
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

// fixture: an abridged capture of the real TUI /model picker (v1.0.73, Free plan): the
// banner, every model row and the decoration (❯/✓/scrollbar/footer).
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
	// A Free-family banner → Auto only (an empty, non-nil list).
	if got := parseModelPicker(modelPickerFree); len(got) != 0 {
		t.Errorf("free plan must yield empty catalog, got %+v", got)
	}
	// No banner → the picker rows are the catalog (Auto, decoration and footer excluded).
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
	// Stray prose lines are not in id shape, so they are not picked up.
	if got := parseModelPicker("   Model\n   run echo and tell me\n ❯  Search models…"); len(got) != 0 {
		t.Errorf("prose row leaked into catalog: %+v", got)
	}
}

// docs/log/68: the edited files must be recoverable from events.jsonl. The shape of `edit`
// is measured (~/.copilot/session-state/*/events.jsonl held
// {"path","old_str","new_str"}).
func TestToolEditsPicksEditFamilyOnly(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		args     string
		wantFile string
		wantOld  string
	}{
		{"edit (the measured shape)", "edit", `{"path":"/a.tsx","old_str":"import x","new_str":"import y"}`, "/a.tsx", "import x"},
		{"create is all additions", "create", `{"path":"/new.ts","content":"hello"}`, "/new.ts", ""},
		// Picking up view would list a file that was merely read as a changed file.
		{"view is not picked up", "view", `{"path":"/a.tsx"}`, "", ""},
		{"bash is not picked up", "bash", `{"command":"rm /a.tsx"}`, "", ""},
		{"grep is not picked up", "grep", `{"path":"/a.tsx","pattern":"x"}`, "", ""},
		{"an unknown name is not picked up", "frobnicate", `{"path":"/a.tsx","content":"x"}`, "", ""},
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

// The carry-over (docs/log/75 P5) shows what permission was being asked for. The
// events.jsonl schema moves between versions, so the detail is used when available; without
// it the permission-wait verdict still holds on requestId alone (an empty detail leaves the
// state at question).
func TestPendingPermissionDetail(t *testing.T) {
	base := `{"type":"user.message","data":{"content":"x"},"timestamp":"2026-07-21T01:00:11Z"}` + "\n"
	withDetail := base + `{"type":"permission.requested","data":{"requestId":"p1","command":"npm ci"},"timestamp":"2026-07-21T01:00:12Z"}` + "\n"
	if st, d := liveStateDetailFromFile(writeFixture(t, withDetail)); st != "question" || d != "npm ci" {
		t.Errorf("liveStateDetailFromFile = (%q,%q), want (question,npm ci)", st, d)
	}
	// On a version that carries no target name, a permission wait is still a permission wait.
	bare := base + `{"type":"permission.requested","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:12Z"}` + "\n"
	if st, d := liveStateDetailFromFile(writeFixture(t, bare)); st != "question" || d != "" {
		t.Errorf("liveStateDetailFromFile = (%q,%q), want (question,\"\")", st, d)
	}
	// The detail of a completed request is not carried over.
	done := withDetail + `{"type":"permission.completed","data":{"requestId":"p1"},"timestamp":"2026-07-21T01:00:13Z"}` + "\n"
	if st, d := liveStateDetailFromFile(writeFixture(t, done)); st == "question" || d != "" {
		t.Errorf("still waiting for permission after completion: (%q,%q)", st, d)
	}
}

// Permission prompts on (docs/log/76). Only --allow-all goes away. --no-remote /
// --no-remote-export (which keep the conversation from leaving the fleet and prevent
// double-driving) are on a different axis from permissions, so they stay.
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
