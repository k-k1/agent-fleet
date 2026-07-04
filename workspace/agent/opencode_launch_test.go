package main

import (
	"strings"
	"testing"
)

func TestBuildOpencodeProgramAuto(t *testing.T) {
	// Default launch: unattended --auto bypass, no session/model when unset.
	got := buildOpencodeProgram("", nil, "")
	if !strings.Contains(got, "--auto") {
		t.Fatalf("expected --auto (permission bypass) in %q", got)
	}

	// With a captured session id and model, they're passed through alongside --auto.
	got = buildOpencodeProgram("anthropic/claude-x", []string{"AF_SESSION_SID=s1"}, "ses_abc")
	for _, want := range []string{"--auto", "--session", "ses_abc", "--model", "anthropic/claude-x", "AF_SESSION_SID="} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
}
