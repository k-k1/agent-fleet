// Package gitx は git 実行の統一ラッパー（docs/23 P1-W1、W5 で internal 化）。
// -C dir の付与、GIT_TERMINAL_PROMPT=0（資格情報がない時にハングせず失敗させる）、
// 出力の trim をここに集約する。環境変数や入出力を個別に細工したい呼び出しだけ
// Cmd を直接使う。
package gitx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Cmd builds a git command rooted at dir (dir=="" inherits the cwd) with
// terminal prompts disabled, so a missing credential fails instead of hanging.
func Cmd(dir string, args ...string) *exec.Cmd {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// Run runs git and returns trimmed stdout. On failure the error also carries
// git's trimmed stderr (the useful part), so callers can %v it directly.
func Run(dir string, args ...string) (string, error) {
	out, err := Cmd(dir, args...).Output()
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

// Combined returns trimmed stdout+stderr interleaved — for handlers that
// surface git's own message verbatim to the Console.
func Combined(dir string, args ...string) (string, error) {
	out, err := Cmd(dir, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// OK reports whether git exits 0 — for existence/state probes where the
// output doesn't matter.
func OK(dir string, args ...string) bool {
	return Cmd(dir, args...).Run() == nil
}
