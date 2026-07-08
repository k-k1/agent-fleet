package main

import (
	"os"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// forkTitle derives a fork's title from the source: its own title, else the stripped
// label, always suffixed " (fork)".
func TestForkTitle(t *testing.T) {
	if got := forkTitle(session.Meta{Title: "my work"}); got != "my work (fork)" {
		t.Fatalf("forkTitle(title) = %q; want %q", got, "my work (fork)")
	}
	if got := forkTitle(session.Meta{Label: "[AF] agent-fleet @0703-1430"}); got != "agent-fleet @0703-1430 (fork)" {
		t.Fatalf("forkTitle(label) = %q; want %q", got, "agent-fleet @0703-1430 (fork)")
	}
}

// buildSessionProgram must emit the official fork command on a fork's FIRST launch
// (no own jsonl yet): claude resumes the SOURCE sid, --fork-session branches it,
// and --session-id pins the new id to OUR deterministic sid (verified: --session-id
// sets the fork's id). Without forkFrom it's a plain new session.
func TestBuildSessionProgramFork(t *testing.T) {
	os.Unsetenv("AGENT_SESSION_CMD")
	// A random sid has no jsonl on disk, so we hit the first-launch branch.
	got := buildSessionProgram("00000000-0000-4000-8000-0000000000fk", "", "", "11111111-1111-4111-8111-111111111src")
	want := "--resume 11111111-1111-4111-8111-111111111src --fork-session --session-id 00000000-0000-4000-8000-0000000000fk"
	if !strings.Contains(got, want) {
		t.Fatalf("fork program = %q, want it to contain %q", got, want)
	}

	// No forkFrom + no jsonl → plain new session, never --fork-session.
	plain := buildSessionProgram("22222222-2222-4222-8222-222222222new", "", "", "")
	if !strings.Contains(plain, "--session-id 22222222-2222-4222-8222-222222222new") || strings.Contains(plain, "--fork-session") {
		t.Fatalf("new-session program = %q, want --session-id and no --fork-session", plain)
	}
}
