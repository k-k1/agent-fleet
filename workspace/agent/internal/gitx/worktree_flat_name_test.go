package gitx

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEnsureWorktreeFlatNameFromLinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	first, err := EnsureWorktree(parent, "main", "feat-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(first), "app@feat-a"; got != want {
		t.Fatalf("first = %q, want %q", got, want)
	}
	// A child created from the linked worktree must not nest its name.
	child, err := EnsureWorktree(first, "feat-a", "feat-b", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(child), "app@feat-b"; got != want {
		t.Fatalf("child = %q, want %q", got, want)
	}
	// Start point still resolves from the linked worktree.
	if b := GitCurrentBranch(child); b != "feat-b" {
		t.Fatalf("child branch = %q", b)
	}
}

func TestEnsureWorktreeFlatNameCollisionRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	p1, err := EnsureWorktree(parent, "main", "p1", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := EnsureWorktree(parent, "main", "p2", "")
	if err != nil {
		t.Fatal(err)
	}
	r1, err := EnsureWorktree(p1, "p1", "review-1", "review")
	if err != nil {
		t.Fatal(err)
	}
	// A sibling asking for the same folder on another branch must be refused.
	if got, err := EnsureWorktree(p2, "p2", "review-2", "review"); err == nil {
		t.Fatalf("collision handed out %q, want error", got)
	}
	// The same branch is the idempotent re-launch.
	again, err := EnsureWorktree(p1, "p1", "review-1", "review")
	if err != nil || again != r1 {
		t.Fatalf("relaunch = %q, %v; want %q", again, err, r1)
	}
}
