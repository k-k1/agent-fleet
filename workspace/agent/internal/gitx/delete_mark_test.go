package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The overlap rules of BeginDelete (#4833): the same copy, a worktree and its base clone never
// run together; unrelated copies do, and a mark is gone once its delete ends.
func TestBeginDeleteRules(t *testing.T) {
	endA, ok, _ := BeginDelete("/r/app@a", "/r/app")
	if !ok {
		t.Fatal("first delete refused")
	}
	if _, ok, _ := BeginDelete("/r/app@a", "/r/app"); ok {
		t.Fatal("the same copy was marked twice")
	}
	endB, ok, _ := BeginDelete("/r/app@b", "/r/app")
	if !ok {
		t.Fatal("a sibling worktree was refused")
	}
	if _, ok, _ := BeginDelete("/r/app", ""); ok {
		t.Fatal("the base clone was marked while its worktrees were being deleted")
	}
	if !UnderDeletion("/r/app@a") || !UnderDeletion("/r/app@a/sub") || UnderDeletion("/r/app@a2") || UnderDeletion("/r/app") {
		t.Fatal("UnderDeletion does not follow the marks")
	}
	endA()
	endA() // idempotent
	endB()
	endBase, ok, _ := BeginDelete("/r/app", "")
	if !ok {
		t.Fatal("the base clone is still refused after its worktrees' deletes ended")
	}
	if _, ok, _ := BeginDelete("/r/app@c", "/r/app"); ok {
		t.Fatal("a worktree was marked while its base clone was being deleted")
	}
	endBase()
	if UnderDeletion("/r/app") {
		t.Fatal("mark left behind")
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// Removes of different worktrees of one base clone hold the parent lock together (read side),
// and prune waits for every one of them (write side). The hook fires after the read lock is taken,
// so a remove that took the write side instead would never let the second one arrive.
func TestRemoveWorktreeSharesTheParentLockAndPruneWaits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "app")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, parent, "init", "-q", "-b", "main")
	gitT(t, parent, "commit", "-q", "--allow-empty", "-m", "i")
	var wts []string
	for _, n := range []string{"a", "b"} {
		wt := filepath.Join(root, "app@"+n)
		gitT(t, parent, "worktree", "add", "-q", "-b", n, wt, "main")
		wts = append(wts, wt)
	}
	arrived, release := make(chan string, 2), make(chan struct{})
	t.Cleanup(SetDeleteStepHook(func(step, dir string) {
		if step == "git-remove" {
			arrived <- dir
			<-release
		}
	}))
	var wg sync.WaitGroup
	for _, wt := range wts {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = removeWorktree(parent, wt) }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatalf("only %d removes held the parent lock together", i)
		}
	}
	pruned := make(chan struct{})
	go func() { pruneWorktrees(parent); close(pruned) }()
	select {
	case <-pruned:
		close(release)
		t.Fatal("prune ran while removes were in flight")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	select {
	case <-pruned:
	case <-time.After(10 * time.Second):
		t.Fatal("prune never ran after the removes ended")
	}
}

// A path that reaches a marked copy through a symlink, or that is gone already, is the same copy.
func TestMarksFollowSymlinksAndSurviveTheFolderGoing(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "app@one")
	if err := os.MkdirAll(filepath.Join(real, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	end, ok, _ := BeginDelete(alias, "")
	if !ok {
		t.Fatal("refused")
	}
	if _, ok, _ := BeginDelete(real, ""); ok {
		t.Fatal("the same copy was marked again through its real path")
	}
	if !UnderDeletion(filepath.Join(alias, "sub")) || !UnderDeletion(real) {
		t.Fatal("a path through the alias does not meet the mark")
	}
	if err := os.RemoveAll(real); err != nil {
		t.Fatal(err)
	}
	if !UnderDeletion(real) || !UnderDeletion(filepath.Join(real, "sub")) {
		t.Fatal("the mark is lost once the folder is gone")
	}
	end()
	if UnderDeletion(real) {
		t.Fatal("mark left behind")
	}
}
