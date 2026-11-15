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
	raw, err := os.ReadFile(failedTurnsFixture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(raw), "\n") // session.start, error, error, shutdown, ""
	p := filepath.Join(t.TempDir(), "events.jsonl")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// First prompt: the file holds only session.start when the turn is sent.
	write(lines[0])
	mark := takeEventsMark(p)
	write(lines[0] + lines[1])
	if got := failureSince(p, mark); got != want {
		t.Fatalf("first turn failure = %q, want %q", got, want)
	}
	// A turn sent after the first failure sees only what was appended after it.
	mark = takeEventsMark(p)
	if got := failureSince(p, mark); got != "" {
		t.Errorf("nothing appended, yet failure = %q", got)
	}
	write(lines[0] + lines[1] + lines[2])
	if got := failureSince(p, mark); got != want {
		t.Errorf("second turn failure = %q, want %q", got, want)
	}
	// The whole recorded file read from its start (no usable mark) still ends in the failure.
	if got := failureSince(failedTurnsFixture, eventsMark{}); got != want {
		t.Errorf("unmarked failure = %q, want %q", got, want)
	}
}

// A file that is truncated or replaced during the turn must not be read from the old byte
// offset: a seek past the new end succeeds and reads nothing, which turned a new failure into a
// clean idle. The mark's size is larger than the new file in the shrink cases and smaller in the
// rotation one.
func TestFailureSinceFileReplacedDuringTurn(t *testing.T) {
	const failLine = `{"type":"session.error","data":{"message":"Execution failed: new","errorType":"query"}}` + "\n"
	// A head of a few KiB made of events that do not end a turn, so the mark has a real size and fingerprint.
	var old strings.Builder
	old.WriteString(`{"type":"session.start","data":{"sessionId":"old"}}` + "\n")
	for old.Len() < 8193 {
		old.WriteString(`{"type":"session.info","data":{"message":"filler filler filler filler"}}` + "\n")
	}
	for name, replace := range map[string]func(p string){
		"truncated": func(p string) {
			if err := os.WriteFile(p, []byte(failLine), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"renamed and recreated, smaller": func(p string) {
			if err := os.Rename(p, p+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(failLine), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"rotated, same header and padding, larger than the mark": func(p string) {
			// The case a head fingerprint alone misses: the replacement keeps the session header
			// and the same padding, regrows past the old size, and only the file identity and
			// the bytes around the mark differ.
			if err := os.Rename(p, p+".old"); err != nil {
				t.Fatal(err)
			}
			head, rest, _ := strings.Cut(old.String(), "\n")
			if err := os.WriteFile(p, []byte(head+"\n"+failLine+rest+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"replaced by a file whose head and tail bytes at the mark are unchanged": func(p string) {
			// Only the file identity tells this one: the new error line is inserted mid-file, sized
			// to a whole number of padding lines so that the bytes at both ends of the old region
			// are identical, and the old size is the offset a size-only reader would resume from.
			if err := os.Rename(p, p+".old"); err != nil {
				t.Fatal(err)
			}
			const padLine = `{"type":"session.info","data":{"message":"filler filler filler filler"}}` + "\n"
			// Trailing spaces inside the message (trimmed from the reported text) round it up.
			spaces := (len(padLine) - len(failLine)%len(padLine)) % len(padLine)
			mid := strings.Replace(failLine, `new"`, `new`+strings.Repeat(" ", spaces)+`"`, 1)
			text := old.String()
			cut := len(text) - 12*len(padLine) // well inside the padding, clear of the fingerprinted tail
			if err := os.WriteFile(p, []byte(text[:cut]+mid+text[cut:]+padLine), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"rotated, new file larger than the mark": func(p string) {
			if err := os.Rename(p, p+".old"); err != nil {
				t.Fatal(err)
			}
			big := `{"type":"session.start","data":{"sessionId":"new"}}` + "\n" + old.String() + failLine
			if err := os.WriteFile(p, []byte(big), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(p, []byte(old.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			mark := takeEventsMark(p)
			replace(p)
			if got := failureSince(p, mark); got != "Execution failed: new" {
				t.Errorf("failure = %q, want the new file's error", got)
			}
		})
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
			if got := failureSince(p, eventsMark{}); got != "" {
				t.Errorf("failure = %q, want none", got)
			}
		})
	}
	if got := failureSince(filepath.Join(t.TempDir(), "missing.jsonl"), eventsMark{}); got != "" {
		t.Errorf("missing file: failure = %q", got)
	}
}

func TestFailureSinceScrubsSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	line := `{"type":"session.error","data":{"message":"denied token=ghp_abcdefghijklmnopqrstuvwxyz0123456789","errorType":"query"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	got := failureSince(p, eventsMark{})
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
