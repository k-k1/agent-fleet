package main

// git_integration_helpers_test.go holds the git execution helpers `git_worktree_base_test.go`
// uses. They are verbatim copies of the ones in internal/gitx/git_integration_test.go and
// internal/gitx/git_ensure_test.go.
//
// Why copies: `git_worktree_base_test.go` drives `POST /sessions` (`handleCreateSession`) all the
// way through tmux, so it is a main integration test and cannot move to gitx. The three helpers
// are nothing but `exec.Command("git", …)` and depend on neither package, so copying them moves
// less than moving the test would.

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

func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-m", "init")
	run("branch", "feature")
}

// gitAt is a copy of the helper `worktree_existing_branch_test.go` uses in 14 places (the
// original is in internal/gitx/git_worktree_branches_test.go). The original's doc comment claims
// it serves that file's worktree-occupancy tests only, but other files used it as well, so the
// copy lives here to keep main's side working without touching a file we do not own.
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
