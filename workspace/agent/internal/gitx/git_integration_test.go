package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runIntegrationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func commitIntegrationFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	runIntegrationGit(t, dir, "add", name)
	runIntegrationGit(t, dir, "commit", "-m", name)
}

func TestGitWorktreeIntegrationRelations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Isolate HOME (like TestEnsureWorktree): EnsureWorktree materializes under
	// ~/repos, and a worktree left in the REAL home outlives its temp parent —
	// once the tmp cleaner removes the parent, later runs see a dangling
	// worktree and fail with relation=unknown.
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	worktree, err := EnsureWorktree(parent, "main", "feature-wt", "")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}

	assertRelation := func(want string, targetUnique, worktreeUnique int) {
		t.Helper()
		got := GitWorktreeIntegration(parent, worktree, "main")
		if got.Relation != want || got.TargetUnique != targetUnique || got.WorktreeUnique != worktreeUnique {
			t.Fatalf("integration = %+v, want relation=%s target=%d worktree=%d", got, want, targetUnique, worktreeUnique)
		}
	}

	assertRelation("same", 0, 0)
	commitIntegrationFile(t, worktree, "worktree-change")
	assertRelation("unmerged", 0, 1)
	commitIntegrationFile(t, parent, "parent-change")
	assertRelation("diverged", 1, 1)
	runIntegrationGit(t, parent, "merge", "--no-ff", "feature-wt", "-m", "merge feature")
	got := GitWorktreeIntegration(parent, worktree, "main")
	if got.Relation != "contained" || got.TargetUnique == 0 || got.WorktreeUnique != 0 {
		t.Fatalf("integration after merge = %+v, want contained with parent-only commits", got)
	}

	unknown := GitWorktreeIntegration(filepath.Join(t.TempDir(), "missing"), worktree, "main")
	if unknown.Relation != "unknown" {
		t.Fatalf("missing parent relation = %q, want unknown", unknown.Relation)
	}
}

func TestFastForwardWorktreeFromParent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Isolate HOME for the same reason TestGitWorktreeIntegrationRelations does:
	// EnsureWorktree materializes under ~/repos, so an unisolated run leaves a worktree
	// in the REAL home whose temp parent is gone — and every later run then reuses that
	// dangling copy (relation=unknown) instead of creating a fresh one.
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	worktree, err := EnsureWorktree(parent, "main", "feature-parent-ff", "")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	commitIntegrationFile(t, parent, "parent-change")
	want, err := Run(parent, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := fastForwardWorktreeFromParent(parent, worktree); err != nil {
		t.Fatalf("fast-forward from parent: %v", err)
	}
	got, err := Run(worktree, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("worktree HEAD = %s, want parent HEAD %s", got, want)
	}
	if err := fastForwardWorktreeFromParent(parent, worktree); err == nil {
		t.Fatal("same worktree unexpectedly accepted for parent fast-forward")
	}
}
