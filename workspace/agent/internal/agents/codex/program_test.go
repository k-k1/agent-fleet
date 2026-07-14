package codex

import (
	"strings"
	"testing"
)

func TestBuildCodexProgram(t *testing.T) {
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", "")
	// Fresh launch: plain codex with bypass flags + injected status hooks.
	got := buildProgram("", "slot1", "", "")
	for _, want := range []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "session-status working slot1 codex", "session-status idle slot1 codex"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
	if strings.Contains(got, "resume") || strings.Contains(got, "fork") {
		t.Fatalf("fresh launch must not resume/fork: %q", got)
	}

	// Captured own session: resume it (resume wins over any ForkFrom).
	got = buildProgram("", "slot1", "cx-own", "cx-src")
	if !strings.Contains(got, "resume 'cx-own'") || strings.Contains(got, "fork") {
		t.Fatalf("expected resume cx-own without fork in %q", got)
	}

	// Forked slot's first launch: fork the source conversation.
	got = buildProgram("gpt-5.5", "slot1", "", "cx-src")
	if !strings.Contains(got, "fork 'cx-src'") || !strings.Contains(got, "-m 'gpt-5.5'") {
		t.Fatalf("expected fork cx-src + model in %q", got)
	}
}

func TestBuildCodexProgramUsesAppServerBeforeSubcommand(t *testing.T) {
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", "unix:///tmp/codex.sock")
	got := buildProgram("", "slot1", "cx-own", "")
	want := "codex --remote 'unix:///tmp/codex.sock' resume 'cx-own'"
	if !strings.Contains(got, want) {
		t.Fatalf("expected %q in %q", want, got)
	}
}
