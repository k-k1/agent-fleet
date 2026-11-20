package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestPaneKindOf(t *testing.T) {
	for cmd, want := range map[string]string{
		"codex --yolo":             session.KindCodex,
		"/usr/bin/claude --resume": session.KindClaude,
		"opencode attach":          session.KindOpencode,
		"opencode in /srv/codex":   session.KindOpencode, // longest marker wins
		"bash":                     session.KindShell,
		"":                         session.KindShell,
	} {
		if got := paneKindOf(cmd); got != want {
			t.Errorf("paneKindOf(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestManagedAbortSignalKind(t *testing.T) {
	for kind, want := range map[string]bool{
		session.KindCodex: true, session.KindOpencode: true,
		session.KindClaude: false, "": false, "nope": false,
	} {
		if got := managedAbortSignalKind(kind); got != want {
			t.Errorf("managedAbortSignalKind(%q) = %v, want %v", kind, got, want)
		}
	}
}
