package harness

import (
	"os"
	"strings"
	"testing"
)

// wantParallelSystemPrompt and wantParallelTaskZero are the exact historical strings that used
// to be inlined directly in TestManualLiveAgenticSession (live_manual_test.go, manuallive-tagged
// and therefore not exercised by a normal `go test ./...`). Comparing against a second, literal
// copy here — rather than only checking for the presence/absence of a substring — is what makes
// this test able to catch an accidental change to the default (parallel=true) path, which must
// stay byte-for-byte identical to every prior live run for comparability.
func wantParallelSystemPrompt(cwd string) string {
	return "You are a careful coding agent working in a real Go project at " + cwd + ". " +
		"You have read/write/edit/glob/grep/ls/bash tools and a todo_write tool. " +
		"Use todo_write to track multi-step work. When a step lets you look at several " +
		"independent things at once (e.g. reading multiple files), issue those tool calls " +
		"in the SAME turn rather than one at a time. Always verify your work by actually " +
		"running `go build ./...` and `go test ./...` with the bash tool before declaring " +
		"something fixed — do not just claim success without having run it."
}

func wantParallelTaskZero() string {
	return "This Go project currently fails `go build ./...` and, once it builds, has failing " +
		"tests under `go test ./...` because of real bugs. Start by reading every .go file " +
		"under this directory (in parallel — one tool call per file, all in the same turn) " +
		"to see the whole project before changing anything. Then find and fix every bug so " +
		"that both `go build ./...` and `go test ./...` succeed. Use todo_write to track " +
		"each bug as you find and fix it."
}

func TestLiveAgenticPromptParallelDefaultUnchanged(t *testing.T) {
	const cwd = "/tmp/some-project"
	if got, want := liveAgenticSystemPrompt(cwd, true), wantParallelSystemPrompt(cwd); got != want {
		t.Errorf("liveAgenticSystemPrompt(cwd, true) changed from the historical prompt:\ngot:  %q\nwant: %q", got, want)
	}
	if got, want := liveAgenticTaskZero(true), wantParallelTaskZero(); got != want {
		t.Errorf("liveAgenticTaskZero(true) changed from the historical prompt:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestLiveAgenticPromptParallelFalseDropsInstruction(t *testing.T) {
	const cwd = "/tmp/some-project"
	sys := liveAgenticSystemPrompt(cwd, false)
	if strings.Contains(sys, "SAME turn") || strings.Contains(sys, "one at a time") {
		t.Errorf("liveAgenticSystemPrompt(cwd, false) still contains the parallel-call instruction: %q", sys)
	}
	// The surrounding sentences must survive untouched — only the parallel clause is dropped.
	if !strings.Contains(sys, "You are a careful coding agent working in a real Go project at "+cwd) {
		t.Errorf("liveAgenticSystemPrompt(cwd, false) lost the opening sentence: %q", sys)
	}
	if !strings.Contains(sys, "Always verify your work by actually running `go build ./...`") {
		t.Errorf("liveAgenticSystemPrompt(cwd, false) lost the verification sentence: %q", sys)
	}

	task := liveAgenticTaskZero(false)
	if strings.Contains(task, "in parallel") || strings.Contains(task, "same turn") {
		t.Errorf("liveAgenticTaskZero(false) still contains the parallel-reading instruction: %q", task)
	}
	if !strings.Contains(task, "reading every .go file under this directory") {
		t.Errorf("liveAgenticTaskZero(false) lost the read-everything instruction: %q", task)
	}
	if !strings.Contains(task, "find and fix every bug") {
		t.Errorf("liveAgenticTaskZero(false) lost the fix-the-bugs instruction: %q", task)
	}
}

func TestLiveParallelEnabledEnv(t *testing.T) {
	const key = "AF_LCPP_LIVE_PARALLEL"
	prev, hadPrev := os.LookupEnv(key)
	t.Cleanup(func() {
		if hadPrev {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})

	os.Unsetenv(key)
	if !liveParallelEnabled() {
		t.Error("liveParallelEnabled() = false with the env var unset, want true (default unchanged)")
	}

	os.Setenv(key, "1")
	if !liveParallelEnabled() {
		t.Error(`liveParallelEnabled() = false with AF_LCPP_LIVE_PARALLEL=1, want true`)
	}

	os.Setenv(key, "0")
	if liveParallelEnabled() {
		t.Error(`liveParallelEnabled() = true with AF_LCPP_LIVE_PARALLEL=0, want false`)
	}
}
