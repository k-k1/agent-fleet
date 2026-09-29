package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Rollout lines in the shapes codex 0.159.0 wrote during #1227's measurement, ids neutral.
func taskStarted(at, turn string) []byte {
	return []byte(`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"task_started","turn_id":"` + turn + `","root_turn_id":"` + turn + `","collaboration_mode_kind":"default"}}`)
}

func userSays(at, text string) []byte {
	return []byte(`{"timestamp":"` + at + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}`)
}

func askUser(at, call, question string) []byte {
	return []byte(`{"timestamp":"` + at + `","type":"response_item","payload":{"type":"function_call","name":"request_user_input","call_id":"` + call +
		`","arguments":"{\"questions\":[{\"header\":\"Fruit\",\"id\":\"fruit\",\"question\":\"` + question +
		`\",\"options\":[{\"label\":\"りんご1\"},{\"label\":\"みかん2\"}]}]}"}}`)
}

func callOutput(at, call, output string) []byte {
	return []byte(`{"timestamp":"` + at + `","type":"response_item","payload":{"type":"function_call_output","call_id":"` + call + `","output":"` + output + `"}}`)
}

func turnAborted(at, turn string) []byte {
	return []byte(`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"` + turn + `","reason":"interrupted"}}`)
}

func taskComplete(at, turn string) []byte {
	return []byte(`{"timestamp":"` + at + `","type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `"}}`)
}

// A question leaves the screen in three ways, and the rollout records them differently
// (measured 0.159.0): answered (an output line), interrupted with Esc (an "aborted by user"
// output and turn_aborted), and the process SIGKILLed (nothing at all — neither `codex resume`
// nor any later turn writes an output for the call). Only the turn boundary closes the third,
// and without it the card, the badge and the free-text gate keep a question nobody can see.
func TestOpenQuestionClosesWithItsTurn(t *testing.T) {
	asked := [][]byte{
		taskStarted("2026-09-30T02:24:57.898Z", "t1"),
		userSays("2026-09-30T02:24:57.940Z", "ask me"),
		askUser("2026-09-30T02:25:04.737Z", "call_q", "Which fruit?"),
	}
	with := func(more ...[]byte) [][]byte { return append(append([][]byte(nil), asked...), more...) }
	for name, c := range map[string]struct {
		lines [][]byte
		open  bool
	}{
		"waiting":              {with(), true},
		"answered":             {with(callOutput("2026-09-30T02:25:10Z", "call_q", "りんご1")), false},
		"interrupted with Esc": {with(callOutput("2026-09-30T02:25:26.176Z", "call_q", "aborted by user after 21.4s"), turnAborted("2026-09-30T02:25:26.190Z", "t1")), false},
		"turn_aborted alone":   {with(turnAborted("2026-09-30T02:25:26.190Z", "t1")), false},
		"SIGKILL, then a turn": {with(taskStarted("2026-09-30T02:27:15.921Z", "t2"), userSays("2026-09-30T02:27:16.247Z", "Reply with just: OK"), taskComplete("2026-09-30T02:27:20.228Z", "t2")), false},
		"task_complete after":  {with(taskComplete("2026-09-30T02:25:30Z", "t1")), false},
	} {
		turns, _, pending, _ := parseRolloutFull(c.lines)
		if got := len(pending) > 0; got != c.open {
			t.Errorf("%s: pending = %+v, want open=%v", name, pending, c.open)
		}
		if c.open {
			continue
		}
		// A closed question stays in the transcript: only the pending one is lifted out of it.
		found := false
		for _, tn := range turns {
			if len(tn.Parts) > 0 && tn.Parts[0].Kind == "question" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: the closed question vanished from the transcript", name)
		}
	}
}

// A Terminal pane's codex can only show a question it asked itself. One asked before the pane's
// process started is left in the transcript, not surfaced as the card to answer.
func TestSnapshotBoundsTheQuestionToTheProcess(t *testing.T) {
	p := newRolloutParser()
	for _, ln := range [][]byte{
		taskStarted("2026-09-30T02:25:41.600Z", "t1"),
		userSays("2026-09-30T02:25:41.644Z", "ask me"),
		askUser("2026-09-30T02:25:47.331Z", "call_q", "Which animal?"),
	} {
		p.feed(ln)
	}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		since time.Time
		open  bool
	}{
		{time.Time{}, true},                 // no bound
		{at("2026-09-30T02:25:30Z"), true},  // the pane's records begin before the question
		{at("2026-09-30T02:25:48Z"), false}, // replaced within the second the question was asked
		{at("2026-09-30T02:26:42Z"), false}, // relaunched after the process died
	} {
		turns, _, pending, _ := p.snapshot(c.since)
		if got := len(pending) > 0; got != c.open {
			t.Errorf("since %v: pending = %+v, want open=%v", c.since, pending, c.open)
		}
		if !c.open && (len(turns) == 0 || turns[len(turns)-1].Parts[0].Kind != "question") {
			t.Errorf("since %v: the question should stay in the transcript, got %+v", c.since, turns)
		}
	}
}

// fakeTmuxSession puts a tmux on PATH whose session was created at created (zero: no session).
// It logs every call, so a test can tell whether the pane was consulted.
func fakeTmuxSession(t *testing.T, created time.Time) (logPath string) {
	t.Helper()
	bin := t.TempDir()
	logPath = filepath.Join(bin, "tmux.log")
	stamp := ""
	if !created.IsZero() {
		stamp = fmt.Sprint(created.Unix())
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
[ -n "` + stamp + `" ] || exit 1
case "$*" in
  *session_created*) printf '%s\n' "` + stamp + `" ;;
  list-panes*) printf '1 %%7\n' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AF_TMUX_SOCKET", "")
	return logPath
}

// writeSlotRollout lays lines where codex writes the rollout of thread id, and maps m's slot to it.
func writeSlotRollout(t *testing.T, m session.Meta, id string, lines ...[]byte) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "09", "30")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ln := range lines {
		b.Write(ln)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-09-30T02-23-46-"+id+".jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	sids.Write(session.UUID(m.Dir, m.Name), id)
}

// PendingQuestionID is what the badge, the chip, the notification and the free-text gate read.
// On the Terminal route a question must belong to the pane's running codex; under managed the
// pane does not exist, and only the turn boundary applies.
func TestPendingQuestionIDIsBoundToTheRunningPane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := session.Meta{Name: "cx-q", Dir: t.TempDir(), Kind: session.KindCodex}
	asked := time.Date(2026, 9, 30, 2, 25, 47, 331e6, time.UTC)
	lines := [][]byte{
		taskStarted("2026-09-30T02:25:41.600Z", "t1"),
		userSays("2026-09-30T02:25:41.644Z", "ask me"),
		askUser(asked.Format(time.RFC3339Nano), "call_q", "Which animal?"),
	}
	writeSlotRollout(t, m, "01a0ee31-0000-7000-8000-000000000001", lines...)

	fakeTmuxSession(t, asked.Add(-time.Minute))
	if got := PendingQuestionID(m); got != "call_q" {
		t.Errorf("question on the running pane: PendingQuestionID = %q, want call_q", got)
	}
	fakeTmuxSession(t, asked.Add(time.Minute))
	if got := PendingQuestionID(m); got != "" {
		t.Errorf("pane relaunched after the question: PendingQuestionID = %q, want none", got)
	}
	// tmux stamps the creation to the second. A pane created in the second the question was asked
	// can only be a replacement (the old codex was killed just before); one created the second
	// before can have asked it.
	fakeTmuxSession(t, asked.Truncate(time.Second))
	if got := PendingQuestionID(m); got != "" {
		t.Errorf("pane replaced within the question's second: PendingQuestionID = %q, want none", got)
	}
	fakeTmuxSession(t, asked.Truncate(time.Second).Add(-time.Second))
	if got := PendingQuestionID(m); got != "call_q" {
		t.Errorf("pane created the second before: PendingQuestionID = %q, want call_q", got)
	}
	fakeTmuxSession(t, time.Time{})
	if got := PendingQuestionID(m); got != "" {
		t.Errorf("no pane: PendingQuestionID = %q, want none", got)
	}

	managed := m
	managed.Driver = session.DriverManaged
	logPath := fakeTmuxSession(t, asked.Add(time.Minute))
	if got := PendingQuestionID(managed); got != "call_q" {
		t.Errorf("managed: PendingQuestionID = %q, want call_q", got)
	}
	if b, _ := os.ReadFile(logPath); len(b) > 0 {
		t.Errorf("managed consulted tmux:\n%s", b)
	}

	writeSlotRollout(t, m, "01a0ee31-0000-7000-8000-000000000001", append(lines, turnAborted("2026-09-30T02:26:00Z", "t1"))...)
	logPath = fakeTmuxSession(t, asked.Add(-time.Minute))
	if got := PendingQuestionID(m); got != "" {
		t.Errorf("after turn_aborted: PendingQuestionID = %q, want none", got)
	}
	if b, _ := os.ReadFile(logPath); len(b) > 0 {
		t.Errorf("no open question, yet tmux was asked:\n%s", b)
	}
	if got := TerminalModal(m); got != "" {
		t.Errorf("TerminalModal = %q, want none", got)
	}
}
