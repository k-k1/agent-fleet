package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The record shapes are claude 2.1.288's (measured with a second Stop hook that blocks once,
// #1600), trimmed to the fields the verdict reads.
func TestStopContinuedFrom(t *testing.T) {
	stop := time.Date(2026, 10, 4, 0, 48, 57, 0, time.UTC) // the marker (second-truncated)
	ts := func(d time.Duration) string { return stop.Add(d).Format(time.RFC3339Nano) }
	prompt := `{"type":"user","timestamp":"` + ts(-time.Minute) + `","message":{"role":"user","content":"Reply with just the word hello."}}`
	answer := `{"type":"assistant","timestamp":"` + ts(39*time.Millisecond) + `","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`
	feedback := `{"type":"user","timestamp":"` + ts(1142*time.Millisecond) + `","isMeta":true,"message":{"role":"user","content":"Stop hook feedback:\nrun sleep 5 first"}}`
	blockErr := `{"type":"attachment","timestamp":"` + ts(1141*time.Millisecond) + `","attachment":{"type":"hook_blocking_error","hookName":"Stop","hookEvent":"Stop"}}`
	summary := func(d time.Duration) string {
		return `{"type":"system","subtype":"stop_hook_summary","timestamp":"` + ts(d) + `","hookCount":2,"preventedContinuation":false}`
	}
	toolUse := `{"type":"assistant","timestamp":"` + ts(3381*time.Millisecond) + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"sleep 5"}}]}}`
	toolResult := `{"type":"user","timestamp":"` + ts(8556*time.Millisecond) + `","message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"done-sleeping"}]}}`
	final := `{"type":"assistant","timestamp":"` + ts(10189*time.Millisecond) + `","message":{"role":"assistant","content":[{"type":"text","text":"Done!"}]}}`
	bookkeeping := `{"type":"last-prompt","lastPrompt":"x"}`
	blocked := []string{prompt, answer, feedback, blockErr, summary(1150 * time.Millisecond)}

	for name, c := range map[string]struct {
		lines     []string
		marker    time.Time
		continued bool
		found     bool
	}{
		"blocked stop":                         {blocked, stop, true, true},
		"blocked, continued turn running tool": {append(append([]string{}, blocked...), toolUse, bookkeeping), stop, true, true},
		"blocked, tool returned":               {append(append([]string{}, blocked...), toolUse, toolResult), stop, true, true},
		"blocked, then the real end":           {append(append([]string{}, blocked...), toolUse, toolResult, final, summary(10239*time.Millisecond)), stop.Add(10 * time.Second), false, true},
		"unblocked stop":                       {[]string{prompt, answer, summary(150 * time.Millisecond)}, stop, false, true},
		"only the feedback line":               {[]string{prompt, answer, feedback, summary(1150 * time.Millisecond)}, stop, true, true},
		"only the attachment":                  {[]string{prompt, answer, blockErr, summary(1150 * time.Millisecond)}, stop, true, true},
		// Esc fires no Stop: the interruption line is the continued turn's end.
		"blocked, then interrupted": {append(append([]string{}, blocked...), toolUse,
			`{"type":"user","timestamp":"`+ts(5*time.Second)+`","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`), stop, false, true},
		// An idle written well after the blocked stop came from elsewhere (a heal): trust it.
		"marker later than the summary": {blocked, stop.Add(time.Minute), false, true},
		// A subagent's records do not belong to the main thread.
		"sidechain summary is ignored": {[]string{answer, `{"type":"system","subtype":"stop_hook_summary","isSidechain":true,"timestamp":"` + ts(time.Second) + `"}`}, stop, false, false},
		"no summary at all":            {[]string{answer, bookkeeping}, stop, false, false},
		// A blocking error of another event (PreToolUse) is not a blocked Stop.
		"other hook event blocked": {[]string{prompt, answer,
			`{"type":"attachment","timestamp":"` + ts(time.Second) + `","attachment":{"type":"hook_blocking_error","hookEvent":"PreToolUse"}}`,
			summary(1150 * time.Millisecond)}, stop, false, true},
	} {
		lines := make([][]byte, len(c.lines))
		for i, l := range c.lines {
			lines[i] = []byte(l)
		}
		continued, found := stopContinuedFrom(lines, c.marker)
		if continued != c.continued || found != c.found {
			t.Errorf("%s: stopContinuedFrom = (%v, %v), want (%v, %v)", name, continued, found, c.continued, c.found)
		}
	}
}

// StopContinued reads the live transcript under CLAUDE_CONFIG_DIR, and a session with no
// transcript, or no marker, answers false.
func TestStopContinuedReadsTranscript(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	const sid = "11111111-2222-3333-4444-555555551600"
	marker := time.Now().Truncate(time.Second)
	if StopContinued(sid, marker) {
		t.Fatal("no transcript must answer false")
	}
	ts := marker.Add(time.Second).UTC().Format(time.RFC3339Nano)
	proj := filepath.Join(cfg, "projects", "p1")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"attachment","timestamp":"` + ts + `","attachment":{"type":"hook_blocking_error","hookEvent":"Stop"}}`,
		`{"type":"system","subtype":"stop_hook_summary","timestamp":"` + ts + `"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(proj, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !StopContinued(sid, marker) {
		t.Fatal("a blocked Stop at the tail must answer true")
	}
	if StopContinued(sid, time.Time{}) {
		t.Fatal("no marker must answer false")
	}
}
