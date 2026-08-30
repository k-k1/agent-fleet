package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// buildProgram のフォークコマンド組み立てテストは internal/agents/claude の
// program_test.go へ移設（docs/log/23 残① Wave F）。

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
