package main

// docs/23 P1-W1: 生 exec.Command("git", ...) の統一ラッパー。-C dir の付与、
// GIT_TERMINAL_PROMPT=0（資格情報がない時にハングせず失敗させる）、出力の trim を
// ここに集約する。環境変数や入出力を個別に細工したい呼び出しだけ gitCmd を直接使う。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// gitCmd builds a git command rooted at dir (dir=="" inherits the cwd) with
// terminal prompts disabled, so a missing credential fails instead of hanging.
func gitCmd(dir string, args ...string) *exec.Cmd {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// runGit runs git and returns trimmed stdout. On failure the error also carries
// git's trimmed stderr (the useful part), so callers can %v it directly.
func runGit(dir string, args ...string) (string, error) {
	out, err := gitCmd(dir, args...).Output()
	s := strings.TrimSpace(string(out))
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				err = fmt.Errorf("%v: %s", err, msg)
			}
		}
	}
	return s, err
}

// runGitCombined returns trimmed stdout+stderr interleaved — for handlers that
// surface git's own message verbatim to the Console.
func runGitCombined(dir string, args ...string) (string, error) {
	out, err := gitCmd(dir, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gitOK reports whether git exits 0 — for existence/state probes where the
// output doesn't matter.
func gitOK(dir string, args ...string) bool {
	return gitCmd(dir, args...).Run() == nil
}
