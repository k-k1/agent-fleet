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

// setupIntegrationUpstream gives the parent clone an origin (a bare repo) with main
// tracking origin/main, and returns a second clone standing in for the forge, where
// PRs get merged.
func setupIntegrationUpstream(t *testing.T, parent string) (forge string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	runIntegrationGit(t, parent, "init", "--bare", bare)
	runIntegrationGit(t, parent, "remote", "add", "origin", bare)
	runIntegrationGit(t, parent, "push", "-u", "origin", "main")
	forge = filepath.Join(t.TempDir(), "forge")
	runIntegrationGit(t, parent, "clone", "-q", "-b", "main", bare, forge)
	return forge
}

func TestGitWorktreeIntegrationComparesParentUpstream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	forge := setupIntegrationUpstream(t, parent)
	worktree, err := EnsureWorktree(parent, "main", "feature-up", "")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	commitIntegrationFile(t, worktree, "worktree-change")
	runIntegrationGit(t, worktree, "push", "origin", "feature-up")

	// The PR is merged on the forge; the parent clone fetches but is not fast-forwarded.
	runIntegrationGit(t, forge, "fetch", "origin")
	runIntegrationGit(t, forge, "merge", "--no-ff", "origin/feature-up", "-m", "merge PR")
	runIntegrationGit(t, forge, "push", "origin", "main")
	runIntegrationGit(t, parent, "fetch", "origin")

	got := GitWorktreeIntegration(parent, worktree, "main")
	if got.Relation != "contained" || got.TargetUnique != 1 || got.WorktreeUnique != 0 {
		t.Fatalf("integration = %+v, want contained by origin/main (1 target-only merge commit)", got)
	}
	if !got.TargetUpstream || got.TargetBranch != "origin/main" {
		t.Fatalf("target = %q upstream=%v, want origin/main upstream=true", got.TargetBranch, got.TargetUpstream)
	}

	want, err := Run(parent, "rev-parse", "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	parentBefore, err := Run(parent, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := fastForwardWorktreeFromParent(parent, worktree); err != nil {
		t.Fatalf("fast-forward to upstream: %v", err)
	}
	if head, _ := Run(worktree, "rev-parse", "HEAD"); head != want {
		t.Fatalf("worktree HEAD = %s, want origin/main %s", head, want)
	}
	if after, _ := Run(parent, "rev-parse", "HEAD"); after != parentBefore {
		t.Fatalf("parent HEAD moved %s -> %s; the parent clone must never be touched", parentBefore, after)
	}
}

func TestGitWorktreeIntegrationFallsBackWithoutUpstream(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	setupIntegrationUpstream(t, parent)
	worktree, err := EnsureWorktree(parent, "main", "feature-gone", "")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	// A configured upstream whose remote ref is gone (pruned) must not hide the parent.
	runIntegrationGit(t, parent, "update-ref", "-d", "refs/remotes/origin/main")
	commitIntegrationFile(t, parent, "parent-change")
	got := GitWorktreeIntegration(parent, worktree, "main")
	if got.Relation != "contained" || got.TargetUnique != 1 || got.TargetUpstream || got.TargetBranch != "main" {
		t.Fatalf("integration = %+v, want contained by parent HEAD labelled main", got)
	}
}
