package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// git run helper scoped to this file's worktree-occupancy tests.
func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// TestWorktreeBranches pins the occupancy map the launch/checkout guards are built
// on: a branch live in ANOTHER worktree is reported with that worktree's path, the
// caller's own branch is not (that is "current", not "occupied"), and a detached
// worktree occupies nothing.
func TestWorktreeBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	gitInit(t, main) // main + a "feature" branch

	wt := filepath.Join(root, "wt")
	gitAt(t, main, "worktree", "add", wt, "feature")

	// From the main copy: feature is occupied by wt, main itself is not listed.
	got := worktreeBranches(main)
	if got["feature"] == "" {
		t.Fatalf("feature not reported as occupied: %v", got)
	}
	if realPath(got["feature"]) != realPath(wt) {
		t.Errorf("feature occupied by %q, want %q", got["feature"], wt)
	}
	if _, ok := got["main"]; ok {
		t.Errorf("own branch must not be reported as occupied: %v", got)
	}

	// Symmetric: from the worktree, main is the occupied one.
	if got := worktreeBranches(wt); realPath(got["main"]) != realPath(main) {
		t.Errorf("from worktree: main occupied by %q, want %q", got["main"], main)
	}

	// A detached worktree holds no branch ref, so it occupies nothing.
	det := filepath.Join(root, "det")
	gitAt(t, main, "worktree", "add", "--detach", det, "main")
	if got := worktreeBranches(main); len(got) != 1 || got["feature"] == "" {
		t.Errorf("detached worktree changed occupancy: %v", got)
	}

	// The branch list carries the same fact, and keeps Current separate from it.
	var feature, mainInfo *branchInfo
	infos := gitBranchInfos(main, "main")
	for i := range infos {
		switch infos[i].Name {
		case "feature":
			feature = &infos[i]
		case "main":
			mainInfo = &infos[i]
		}
	}
	if feature == nil || mainInfo == nil {
		t.Fatal("branch list missing main/feature")
	}
	if feature.WorktreePath == "" {
		t.Error("branch list: feature should carry its occupying worktree")
	}
	if mainInfo.WorktreePath != "" || !mainInfo.Current {
		t.Errorf("branch list: main = %+v, want current & unoccupied", *mainInfo)
	}
}

// TestGitRefusesSameBranchTwice is the premise the whole guard rests on: git itself
// rejects a second checkout of a live branch, from both `checkout` and
// `worktree add`. If a future git ever relaxes this, the guard's error mapping is
// wrong and this test says so.
func TestGitRefusesSameBranchTwice(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	gitInit(t, main)
	wt := filepath.Join(root, "wt")
	gitAt(t, main, "worktree", "add", wt, "feature")

	if out, err := gitx.Combined(main, "checkout", "feature"); err == nil {
		t.Errorf("checkout of a branch live in another worktree succeeded: %s", out)
	}
	if out, err := gitx.Combined(main, "worktree", "add", filepath.Join(root, "wt2"), "feature"); err == nil {
		t.Errorf("worktree add of a branch live in another worktree succeeded: %s", out)
	}
}
