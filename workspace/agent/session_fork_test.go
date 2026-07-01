package main

import (
	"os"
	"strings"
	"testing"
)

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

// deriveForkName appends -fork (then -fork2..) and truncates the base so the whole
// name stays within the 40-char / [A-Za-z0-9_-] limit.
func TestDeriveForkName(t *testing.T) {
	n, ok := deriveForkName("slot01")
	if !ok || n != "slot01-fork" {
		t.Fatalf("deriveForkName(slot01) = %q,%v; want slot01-fork,true", n, ok)
	}
	long := strings.Repeat("a", 40) // already at the cap
	n2, ok := deriveForkName(long)
	if !ok || len(n2) > 40 || !strings.HasSuffix(n2, "-fork") || !nameRe.MatchString(n2) {
		t.Fatalf("deriveForkName(40 a's) = %q (len %d),%v; want a valid ≤40 name ending -fork", n2, len(n2), ok)
	}
}
