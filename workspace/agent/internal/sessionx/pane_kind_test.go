package sessionx

import (
	"os"
	"path/filepath"
	"strings"
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

// TestEnsureClaudeSettingsWiringDispatch pins that only claude's launch re-asserts both the
// status hooks and the statusLine in settings.json; other, empty and unknown kinds write nothing.
func TestEnsureClaudeSettingsWiringDispatch(t *testing.T) {
	read := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
		return string(b)
	}
	for _, kind := range []string{session.KindCodex, session.KindShell, "", "nope"} {
		dir := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		EnsureClaudeSettingsWiring(kind)
		if got := read(dir); got != "" {
			t.Errorf("kind %q wrote settings.json: %s", kind, got)
		}
	}
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	EnsureClaudeSettingsWiring(session.KindClaude)
	got := read(dir)
	if !strings.Contains(got, "session-status") {
		t.Errorf("claude launch did not wire the status hooks: %s", got)
	}
	if !strings.Contains(got, "statusLine") {
		t.Errorf("claude launch did not wire the statusLine: %s", got)
	}
}
