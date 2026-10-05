package sessionx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The screens a Terminal pane shows outside any question, permission or plan modal also take
// the keys a prompt types (#1263): codex's update menu ("Update now" exits the process), its
// lock screen (f forks, q exits) and its model-switch nudge, and opencode's permission prompt
// (the Enter allows). The gate refuses free text while one is up and lets it through on the
// ordinary screens around them. The panes are the captures in each kind's testdata/.
func TestPromptBlockerReadsTerminalScreens(t *testing.T) {
	created := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)

	t.Run("codex", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_codex_screen", session.KindCodex)
		for file, want := range map[string]string{
			"pane-update-menu.txt":                   "update",
			"pane-update-menu-narrow.txt":            "update",
			"pane-lock.txt":                          "locked",
			"pane-lock-narrow.txt":                   "locked",
			"pane-model-switch-synthetic.txt":        "model_switch",
			"pane-model-switch-synthetic-narrow.txt": "model_switch",
			"pane-idle.txt":                          "",
			"pane-working.txt":                       "",
			"pane-after-retry.txt":                   "",
		} {
			fakeModalTmux(t, created, kindPane(t, "codex", file))
			wantBlocker(t, m.Name, want, file)
		}
		managed := session.Meta{Name: "gate_codex_managed", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
		session.WriteMeta(managed)
		fakeModalTmux(t, created, kindPane(t, "codex", "pane-update-menu.txt"))
		wantBlocker(t, managed.Name, "", "a managed session, which has no pane")
	})

	t.Run("opencode", func(t *testing.T) {
		isolateAgentConfigDirs(t)
		m := writeModalMeta(t, "gate_opencode_screen", session.KindOpencode)
		for file, want := range map[string]string{
			"pane-permission.txt":        "permission",
			"pane-permission-narrow.txt": "permission",
			"pane-permission-always.txt": "permission",
			"pane-idle.txt":              "",
			"pane-after-reject.txt":      "",
		} {
			fakeModalTmux(t, created, kindPane(t, "opencode", file))
			wantBlocker(t, m.Name, want, file)
		}
	})
}

func kindPane(t *testing.T, kind, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agents", kind, "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
