package sessionx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// turnsAgent serves a mutable list of turns and a fixed live state in place of a real kind's
// store, so a test can append to a turn between two reads.
type turnsAgent struct {
	agents.Agent
	turns *[]transcript.Turn
	state *string
}

func (a turnsAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{Turns: *a.turns}, true
}

func (a turnsAgent) LiveStateTraits() agents.LiveStateTraits {
	return agents.LiveStateTraits{State: func(session.Meta) string { return *a.state }}
}

func outputRead(t *testing.T, name, query string) (string, int) {
	t.Helper()
	rec := getOutput(t, name, query)
	if rec.Code != 200 {
		t.Fatalf("%s: status = %d body = %s", query, rec.Code, rec.Body.String())
	}
	var body struct {
		Output string `json:"output"`
		Cursor int    `json:"cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Output, body.Cursor
}

func TestOutputCursorKeepsGrowingTurnReadable(t *testing.T) {
	peekFixture(t, true)
	// A tmux that says every session exists, so the session reads as alive.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	m := session.Meta{Name: "oc_cur", Dir: t.TempDir(), Kind: session.KindOpencode}
	session.WriteMeta(m)
	turns := []transcript.Turn{
		{Role: "user", Text: "go"},
		{Role: "assistant", Text: "first"},
		{Role: "user", Text: "more"},
		{Role: "assistant", Text: "part one"},
	}
	state := "working"
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, turnsAgent{agentreg.Of(session.KindOpencode), &turns, &state}))

	out, cur := outputRead(t, m.Name, "")
	if !strings.Contains(out, "part one") {
		t.Fatalf("first read = %q", out)
	}

	// The same assistant turn grows while the session is still working.
	turns[3].Text = "part one and part two"
	out, cur = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if !strings.Contains(out, "part two") {
		t.Fatalf("continue-read lost the rest of the turn: %q", out)
	}

	// Once the session is idle the turn is complete: it is counted and not re-sent.
	state = "idle"
	out, cur = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if !strings.Contains(out, "part two") {
		t.Fatalf("read after the turn ended = %q", out)
	}
	if cur != len(turns) {
		t.Fatalf("idle cursor = %d, want %d", cur, len(turns))
	}
	out, _ = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if out != "" {
		t.Fatalf("a finished turn was re-sent: %q", out)
	}
}

func TestOutputTurnCursor(t *testing.T) {
	u := transcript.Turn{Role: "user"}
	a := transcript.Turn{Role: "assistant"}
	for _, tc := range []struct {
		name  string
		turns []transcript.Turn
		state string
		want  int
	}{
		{"empty", nil, "working", 0},
		{"working, last assistant is held back", []transcript.Turn{u, a}, "working", 1},
		{"working, last is a user turn", []transcript.Turn{a, u}, "working", 2},
		{"idle counts everything", []transcript.Turn{u, a}, "idle", 2},
		{"stopped counts everything", []transcript.Turn{u, a}, "stopped", 2},
		{"question counts everything", []transcript.Turn{u, a}, "question", 2},
		{"permission counts everything", []transcript.Turn{u, a}, "permission", 2},
		{"limited counts everything", []transcript.Turn{u, a}, "limited", 2},
		{"only turn is held back", []transcript.Turn{a}, "working", 0},
	} {
		if got := outputTurnCursor(tc.turns, tc.state); got != tc.want {
			t.Errorf("%s: cursor = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// stepTurnsAgent is turnsAgent for a StepIndexed kind.
type stepTurnsAgent struct{ turnsAgent }

func (a stepTurnsAgent) Transcript(session.Meta) (agents.TranscriptData, bool) {
	return agents.TranscriptData{Turns: *a.turns, StepIndexed: true}, true
}

func TestOutputStepOutputCursor(t *testing.T) {
	u := transcript.Turn{Role: "user", Idx: 4}
	a := transcript.Turn{Role: "assistant", Idx: 9}
	for _, tc := range []struct {
		name  string
		turns []transcript.Turn
		state string
		want  int
	}{
		{"empty", nil, "working", stepCursorBase},
		{"working, the growing turn is held back from the previous turn's end", []transcript.Turn{u, a}, "working", stepCursorBase + 5},
		{"working, a lone growing turn is read from the start", []transcript.Turn{a}, "working", stepCursorBase},
		{"working, last is a user turn", []transcript.Turn{a, u}, "working", stepCursorBase + 5},
		{"idle is one past the last index", []transcript.Turn{u, a}, "idle", stepCursorBase + 10},
	} {
		if got := stepOutputCursor(tc.turns, tc.state); got != tc.want {
			t.Errorf("%s: cursor = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func stepFixture(t *testing.T, name string, turns *[]transcript.Turn, state *string) session.Meta {
	t.Helper()
	peekFixture(t, true)
	// A tmux that says every session exists, so the session reads as alive.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	m := session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindAgy}
	session.WriteMeta(m)
	t.Cleanup(agentreg.SwapForTest(session.KindAgy, stepTurnsAgent{turnsAgent{agentreg.Of(session.KindAgy), turns, state}}))
	return m
}

// A late turn that lands between two already-read ones shifts every later position, so a count
// cursor would re-send the turn it had just read. The step cursor does neither that nor skip
// the turn written after it.
func TestOutputStepIndexedCursorSurvivesInsertedTurn(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Idx: 0, Text: "go"},
		{Role: "assistant", Idx: 1, Text: "first"},
		{Role: "user", Idx: 10, Text: "more"},
		{Role: "assistant", Idx: 11, Text: "second"},
	}
	state := "idle"
	m := stepFixture(t, "agy_step_cur", &turns, &state)

	out, cur := outputRead(t, m.Name, "")
	if out != "first\n\nsecond" || cur != stepCursorBase+12 {
		t.Fatalf("first read = %q cursor %d, want both turns and index 12", out, cur)
	}
	// Two rows materialize before the cursor, then a new turn arrives after it.
	turns = append(turns[:2], append([]transcript.Turn{
		{Role: "user", Idx: 4, Text: "failed prompt"},
		{Role: "assistant", Idx: 5},
	}, turns[2:]...)...)
	turns = append(turns, transcript.Turn{Role: "assistant", Idx: 13, Text: "third"})
	out, cur = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if out != "third" || cur != stepCursorBase+14 {
		t.Fatalf("read after the insertion = %q cursor %d, want only \"third\" and index 14", out, cur)
	}
	out, cur = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if out != "" || cur != stepCursorBase+14 {
		t.Fatalf("a finished turn was re-sent: %q cursor %d", out, cur)
	}
	// A transient empty read does not rewind the caller's cursor.
	turns = nil
	if _, got := outputRead(t, m.Name, "since="+strconv.Itoa(cur)); got != cur {
		t.Fatalf("empty read moved the cursor to %d", got)
	}
}

// A growing assistant turn is re-read from the previous turn's end while working, so the next
// read starts at its own Idx (Idx == since: its prompt sits at 40, as in native agy transcripts)
// and sees the rest; once idle the cursor moves past it.
func TestOutputStepIndexedGrowingTurnResumesAtItsIndex(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Idx: 40, Text: "go"},
		{Role: "assistant", Idx: 41, Text: "part one"},
	}
	state := "working"
	m := stepFixture(t, "agy_step_grow", &turns, &state)

	out, cur := outputRead(t, m.Name, "")
	if out != "part one" || cur != stepCursorBase+41 {
		t.Fatalf("working read = %q cursor %d, want the turn held back at index 41", out, cur)
	}
	turns[1].Text = "part one and two"
	out, cur = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if out != "part one and two" || cur != stepCursorBase+41 {
		t.Fatalf("resume at the held-back index = %q cursor %d", out, cur)
	}
	turns[1].Text = "part one and two and three"
	state = "idle"
	out, cur = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if out != "part one and two and three" || cur != stepCursorBase+42 {
		t.Fatalf("idle read = %q cursor %d, want the final text and index 82", out, cur)
	}
	if out, _ = outputRead(t, m.Name, "since="+strconv.Itoa(cur)); out != "" {
		t.Fatalf("a finished turn was re-sent: %q", out)
	}
}

// The growing turn can re-anchor to a lower Idx (agy reads the anchor from a database that was
// briefly unreadable), and the answer can finish before the next poll: that very next poll is
// idle, and still has to deliver the final text.
func TestOutputStepIndexedDeliversAReanchoredTurnFinishedBeforeTheNextPoll(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "assistant", Idx: 99, Text: "earlier"},
		{Role: "assistant", Idx: 121, Text: "partial"},
	}
	state := "working"
	m := stepFixture(t, "agy_step_anchor", &turns, &state)

	out, cur := outputRead(t, m.Name, "")
	if out != "earlier\n\npartial" {
		t.Fatalf("first read = %q", out)
	}
	turns[1].Idx, turns[1].Text = 101, "partial and the rest"
	state = "idle"
	out, cur = outputRead(t, m.Name, "since="+strconv.Itoa(cur))
	if out != "partial and the rest" || cur != stepCursorBase+102 {
		t.Fatalf("idle read after the re-anchor = %q cursor %d, want the final text and index 102", out, cur)
	}
	if out, _ = outputRead(t, m.Name, "since="+strconv.Itoa(cur)); out != "" {
		t.Fatalf("a finished turn was re-sent: %q", out)
	}
}

// A cursor saved by the MCP client before the index format is a turn count: it keeps that
// meaning for the read that replaces it, then the cursor is an index.
func TestOutputStepIndexedKeepsASavedCountCursor(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Idx: 0, Text: "go"},
		{Role: "assistant", Idx: 1, Text: "first"},
		{Role: "user", Idx: 10, Text: "more"},
		{Role: "assistant", Idx: 11, Text: "second"},
	}
	state := "idle"
	m := stepFixture(t, "agy_step_legacy", &turns, &state)

	out, cur := outputRead(t, m.Name, "since=4")
	if out != "" || cur != stepCursorBase+12 {
		t.Fatalf("count cursor 4 replayed %q (cursor %d): it was read as index 4", out, cur)
	}
	turns = append(turns, transcript.Turn{Role: "assistant", Idx: 13, Text: "third"})
	if out, _ = outputRead(t, m.Name, "since=3"); out != "second\n\nthird" {
		t.Fatalf("count cursor 3 = %q, want the turns from position 3", out)
	}
}
