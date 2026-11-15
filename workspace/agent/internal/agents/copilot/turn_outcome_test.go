package copilot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const failedTurnsFixture = "testdata/failed_turns_events.jsonl"

// failed_turns_events.jsonl is a recorded events.jsonl of a managed session whose model was
// refused (copilot 1.0.110, two prompts): session.start, two session.error lines, session.shutdown
// - no user.message and no turn events at all.
func TestFailureSinceRecordedFailedTurns(t *testing.T) {
	const want = `Execution failed: Model "claude-haiku-5.5" is not available.`
	if got := failureSince(failedTurnsFixture, 0); got != want {
		t.Fatalf("failure = %q, want %q", got, want)
	}
	// A turn sent after the first failure sees only what was appended after it.
	raw, err := os.ReadFile(failedTurnsFixture)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Index(string(raw), "\n") + 1
	second := first + strings.Index(string(raw[first:]), "\n") + 1 // just past the first session.error
	if got := failureSince(failedTurnsFixture, int64(second)); got != want {
		t.Errorf("second turn failure = %q, want %q", got, want)
	}
	if got := failureSince(failedTurnsFixture, int64(len(raw))); got != "" {
		t.Errorf("nothing appended, yet failure = %q", got)
	}
}

func TestFailureSinceIgnoresNoticeAndAnsweredTurns(t *testing.T) {
	for name, content := range map[string]string{
		"fallback notice": `{"type":"session.error","data":{"message":"Model \"x\" from --model flag is not available. Using \"auto\" instead.","errorType":"model"}}
{"type":"assistant.turn_start","data":{}}
{"type":"assistant.turn_end","data":{}}
`,
		"error then answer": `{"type":"session.error","data":{"message":"retrying","errorType":"query"}}
{"type":"assistant.message","data":{"content":"ok"}}
`,
		"no file content": "",
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := failureSince(p, 0); got != "" {
				t.Errorf("failure = %q, want none", got)
			}
		})
	}
	if got := failureSince(filepath.Join(t.TempDir(), "missing.jsonl"), 0); got != "" {
		t.Errorf("missing file: failure = %q", got)
	}
}

func TestFailureSinceScrubsSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	line := `{"type":"session.error","data":{"message":"denied token=ghp_abcdefghijklmnopqrstuvwxyz0123456789","errorType":"query"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	got := failureSince(p, 0)
	if got == "" || strings.Contains(got, "ghp_abcdef") {
		t.Errorf("failure = %q", got)
	}
}

// turnWatch starts at the end of the file as it is when the turn is sent, and a first turn
// (no file yet) starts at zero.
func TestTurnWatchOffset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	const sid = "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(home, "session-state", sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "events.jsonl")
	errLine := `{"type":"session.error","data":{"message":"Execution failed: boom","errorType":"query"}}` + "\n"

	if EventsPath(sid) != p {
		t.Skipf("EventsPath = %s, test assumes %s", EventsPath(sid), p)
	}
	first := turnWatch(sid) // no file yet
	if err := os.WriteFile(p, []byte(errLine), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := first(); got != "Execution failed: boom" {
		t.Errorf("first turn failure = %q", got)
	}
	second := turnWatch(sid) // the old error is before the offset
	if got := second(); got != "" {
		t.Errorf("old error attributed to a new turn: %q", got)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(errLine)
	_ = f.Close()
	if got := second(); got != "Execution failed: boom" {
		t.Errorf("new error missed: %q", got)
	}
}

// Consecutive failed turns recorded without any turn boundary stay separate turns.
func TestParseEventsRecordedFailedTurnsStaySeparate(t *testing.T) {
	turns := parseEvents(failedTurnsFixture)
	if len(turns) != 2 {
		t.Fatalf("want 2 error turns, got %d: %+v", len(turns), turns)
	}
	for i, tr := range turns {
		if tr.Role != "assistant" || len(tr.Parts) != 1 || tr.Parts[0].Kind != "error" ||
			!strings.HasPrefix(tr.Text, "[error] Execution failed") {
			t.Errorf("turn %d wrong: %+v", i, tr)
		}
	}
	if turns[0].Idx >= turns[1].Idx {
		t.Errorf("Idx not increasing: %d, %d", turns[0].Idx, turns[1].Idx)
	}
}
