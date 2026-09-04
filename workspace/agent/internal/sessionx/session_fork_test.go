package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The fork-command assembly tests for buildProgram live in internal/agents/claude/program_test.go.

// forkTitle derives a fork's title from the source: its own title, else the stripped
// label, always suffixed " (fork)".
func TestForkTitle(t *testing.T) {
	if got := forkTitle(session.Meta{Title: "my work"}); got != "my work (fork)" {
		t.Fatalf("forkTitle(title) = %q; want %q", got, "my work (fork)")
	}
	if got := forkTitle(session.Meta{Label: "[AF] agent-fleet @0703-1430"}); got != "agent-fleet @0703-1430 (fork)" {
		t.Fatalf("forkTitle(label) = %q; want %q", got, "agent-fleet @0703-1430 (fork)")
	}
	// A label carrying a session name: the source's name must never reach the title, or the
	// forked session ends up announcing another session's name.
	if got := forkTitle(session.Meta{Label: "[AF:s6bbilu] agent-fleet @0703-1430"}); got != "agent-fleet @0703-1430 (fork)" {
		t.Fatalf("forkTitle(label with session name) = %q; want %q", got, "agent-fleet @0703-1430 (fork)")
	}
}
