package codex

import (
	"strings"
	"testing"
)

func TestBuildCodexProgram(t *testing.T) {
	// Fresh launch: plain codex with bypass flags + injected status hooks.
	got := buildProgram("", "", "slot1", "", "")
	for _, want := range []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "session-status working slot1 codex", "session-status idle slot1 codex", "'features.default_mode_request_user_input=true'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
	if strings.Contains(got, "resume") || strings.Contains(got, "fork") {
		t.Fatalf("fresh launch must not resume/fork: %q", got)
	}

	// Captured own session: resume it (resume wins over any ForkFrom).
	got = buildProgram("", "", "slot1", "cx-own", "cx-src")
	if !strings.Contains(got, "resume 'cx-own'") || strings.Contains(got, "fork") {
		t.Fatalf("expected resume cx-own without fork in %q", got)
	}

	// Forked slot's first launch: fork the source conversation.
	got = buildProgram("gpt-5.5", "high", "slot1", "", "cx-src")
	if !strings.Contains(got, "fork 'cx-src'") || !strings.Contains(got, "-m 'gpt-5.5'") || !strings.Contains(got, `'model_reasoning_effort="high"'`) {
		t.Fatalf("expected fork cx-src + model + effort in %q", got)
	}
}

// TestBuildCodexProgramEnablesQuestionsOnEveryRoute pins the request_user_input opt-in
// to every launch shape, not just a fresh one: a resumed or forked slot that dropped the
// flag would silently lose its "has a question" state (codex refuses the tool in Default
// mode without it — measured on 0.144.3 and 0.144.5), which is exactly the failure the flag
// exists to prevent.
func TestBuildCodexProgramEnablesQuestionsOnEveryRoute(t *testing.T) {
	const want = "'features.default_mode_request_user_input=true'"
	for _, tc := range []struct {
		name           string
		resume, forkFr string
	}{
		{"fresh", "", ""},
		{"resume", "cx-own", ""},
		{"fork", "", "cx-src"},
	} {
		if got := buildProgram("", "", "slot1", tc.resume, tc.forkFr); !strings.Contains(got, want) {
			t.Errorf("%s: expected %q in %q", tc.name, want, got)
		}
	}
}

// A TUI attached with --remote runs its thread in the app-server's $HOME and drops every -c
// override, the status hooks included (docs/log/124 §3), so the launch stays direct even
// while the shared app-server is up and advertising its address.
func TestBuildCodexProgramNeverAttachesToTheAppServer(t *testing.T) {
	t.Setenv("AF_CODEX_APP_SERVER_ADDR", "ws://127.0.0.1:7798")
	for _, resume := range []string{"", "cx-own"} {
		got := buildProgram("", "", "slot1", resume, "")
		if strings.Contains(got, "--remote") {
			t.Fatalf("resume=%q: launch attaches to the app-server: %q", resume, got)
		}
		if !strings.HasPrefix(got, "codex ") {
			t.Fatalf("resume=%q: expected a direct `codex …` launch, got %q", resume, got)
		}
	}
}
