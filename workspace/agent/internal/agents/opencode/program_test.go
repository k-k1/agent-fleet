package opencode

import (
	"strings"
	"testing"
)

func TestBuildOpencodeProgramAuto(t *testing.T) {
	// Default launch: unattended --auto bypass, no session/model when unset.
	got := buildProgram("", nil, "", false)
	if !strings.Contains(got, "--auto") {
		t.Fatalf("expected --auto (permission bypass) in %q", got)
	}

	// With a captured session id and model, they're passed through alongside --auto.
	got = buildProgram("anthropic/claude-x", []string{"AF_SESSION_SID=s1"}, "ses_abc", false)
	for _, want := range []string{"--auto", "--session", "ses_abc", "--model", "anthropic/claude-x", "AF_SESSION_SID="} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
}

func TestBuildOpencodeProgramFork(t *testing.T) {
	// Fork launch (first boot of a forked slot): resume the SOURCE session with --fork
	// so opencode copies it into a new conversation and diverges.
	got := buildProgram("", nil, "ses_src", true)
	if !strings.Contains(got, "--session 'ses_src'") || !strings.Contains(got, "--fork") {
		t.Fatalf("expected --session ses_src --fork in %q", got)
	}
	// A plain resume must NOT carry --fork.
	got = buildProgram("", nil, "ses_src", false)
	if strings.Contains(got, "--fork") {
		t.Fatalf("plain resume must not fork: %q", got)
	}
}
