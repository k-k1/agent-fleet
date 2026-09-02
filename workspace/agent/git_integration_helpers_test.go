package main

// git_integration_helpers_test.go — `git_worktree_base_test.go` が使う git 実行ヘルパ。
//
// 元は `git_integration_test.go` / `git_ensure_test.go` の中にあり、移送で
// `internal/gitx` へ動いた。**ここにあるのは同じ中身の写しである。**
//
// なぜ写したか: `git_worktree_base_test.go` は `POST /sessions`（`handleCreateSession`）
// と tmux まで通す main の統合テストなので、gitx へは移せない。一方ヘルパ 3 本は
// `exec.Command("git", …)` だけで、gitx にも main にも依存しない純粋な道具である。
// 「テストを移す」より「3 本を写す」方が、駆動の変わり幅が小さい。
// 原本は internal/gitx/git_integration_test.go と internal/gitx/git_ensure_test.go。

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

// gitAt は所有外の `worktree_existing_branch_test.go` が 14 箇所で使っているヘルパの写し
// （原本は internal/gitx/git_worktree_branches_test.go）。
//
// ⚠️ 原本の doc コメントは「このファイルの worktree 占有テスト用」と書いてあるが、
// 実際には**移送前から他ファイルも使っていた**。所有外のファイルを 1 行も触らずに
// 済ませるため、こちらへ写して main 側に残す。
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
