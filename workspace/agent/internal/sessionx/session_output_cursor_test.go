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
		{"only turn is held back", []transcript.Turn{a}, "working", 0},
	} {
		if got := outputTurnCursor(tc.turns, tc.state); got != tc.want {
			t.Errorf("%s: cursor = %d, want %d", tc.name, got, tc.want)
		}
	}
}
