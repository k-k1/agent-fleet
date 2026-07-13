package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Codex and OpenCode need an explicit bracketed-paste boundary before the submit
// Enter. A raw send-keys -l burst leaves their paste detector active and can swallow
// Enter, which is especially visible in create_session's first prompt delivery.
func TestTypeLineAndSubmitUsesBracketedPasteForTUIs(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(bin, "tmux.log")
	stdinPath := filepath.Join(bin, "tmux.stdin")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_TEST_LOG"
if [ "$1" = "load-buffer" ]; then
  /bin/cat > "$TMUX_TEST_STDIN"
fi
`
	tmux := filepath.Join(bin, "tmux")
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX_TEST_LOG", logPath)
	t.Setenv("TMUX_TEST_STDIN", stdinPath)
	t.Setenv("AGENT_INPUT_SUBMIT_DELAY_MS", "0")
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))

	for _, kind := range []string{session.KindCodex, session.KindOpencode} {
		t.Run(kind, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			name := "paste_" + kind
			session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: kind})
			const prompt = "first line\nsecond line"
			if err := typeLineAndSubmit(name, "%7", prompt); err != nil {
				t.Fatalf("typeLineAndSubmit: %v", err)
			}
			got, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			commands := string(got)
			if !strings.Contains(commands, "load-buffer -b af-prompt-"+name+"-") ||
				!strings.Contains(commands, "paste-buffer -p -d -b af-prompt-"+name+"-") ||
				!strings.Contains(commands, "send-keys -t %7 Enter") {
				t.Fatalf("tmux commands = %q, want load + bracketed paste + Enter", commands)
			}
			pasted, err := os.ReadFile(stdinPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(pasted) != prompt {
				t.Fatalf("pasted text = %q, want %q", pasted, prompt)
			}
		})
	}
}

// A {prompt} while an AskUserQuestion is pending would be typed into the modal, which
// ignores the text and lets the Enter confirm the FIRST option (docs/dev/92). The
// input handler must therefore see the session as question-pending and reject it.
func TestQuestionPendingGatesPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const name = "auq_guard"
	dir := t.TempDir()
	session.WriteMeta(session.Meta{Name: name, Dir: dir, Kind: session.KindClaude})
	sid := session.UUID(dir, name)

	if questionPending(name) {
		t.Fatal("no status recorded yet — must read as not-pending")
	}
	status.Persist(sid, "question")
	if !questionPending(name) {
		t.Fatal("status=question must gate the {prompt} path")
	}
	// Answered → PostToolUse flips to working; the gate must lift.
	status.Persist(sid, "working")
	if questionPending(name) {
		t.Fatal("status=working must not gate prompts")
	}
	if questionPending("no_such_session") {
		t.Fatal("unknown session must read as not-pending")
	}
}
