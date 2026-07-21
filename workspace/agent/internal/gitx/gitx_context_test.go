package gitx

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestCmdContextCancels verifies the context is actually wired into the git process, so a
// deadline/cancel bounds a network op (submodule update) instead of letting it hang: an
// already-cancelled context makes Run fail fast rather than execute git to completion.
func TestCmdContextCancels(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done before Run
	start := time.Now()
	if err := CmdContext(ctx, "", "version").Run(); err == nil {
		t.Fatal("expected an error from a cancelled context, got nil")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancelled CmdContext took %v, want near-immediate", d)
	}
	// Sanity: the same command runs fine under a live context.
	if err := CmdContext(context.Background(), "", "version").Run(); err != nil {
		t.Fatalf("git version under live context failed: %v", err)
	}
}
