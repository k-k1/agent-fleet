package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func readPane(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The captures are codex 0.159.2 panes (#1263): the update menu, the lock screen of a
// conversation the app-server holds, at 100 columns and on a narrow pane where codex wraps the
// rows itself, and the ordinary screens around them — idle, working, and the conversation after
// the lock was retried. The model-switch fixtures are synthetic (see screen.go). A pasted prompt
// + Enter into any of the three screens decides it for the user, so each must read as one; the
// ordinary screens must not, or every send is refused.
func TestPaneScreenReadsCaptures(t *testing.T) {
	for file, want := range map[string]string{
		"pane-update-menu.txt":                   "update",
		"pane-update-menu-narrow.txt":            "update",
		"pane-lock.txt":                          "locked",
		"pane-lock-narrow.txt":                   "locked",
		"pane-model-switch-synthetic.txt":        "model_switch",
		"pane-model-switch-synthetic-narrow.txt": "model_switch",
		"pane-idle.txt":                          "",
		"pane-idle-narrow.txt":                   "",
		"pane-working.txt":                       "",
		"pane-after-retry.txt":                   "",
	} {
		if got := PaneScreen(readPane(t, file)); got != want {
			t.Errorf("%s: PaneScreen = %q, want %q", file, got, want)
		}
	}
}

// The update menu before 0.159 (a 0.144.3 capture): the same screen under its old wording, and
// the banner it leaves above the composer once skipped, which is not the menu.
func TestPaneScreenReadsTheOldUpdateMenu(t *testing.T) {
	menu := "  ✨ Update available! 0.144.3 -> 0.999.0\n" +
		"  Release notes: https://github.com/openai/codex/releases/latest\n" +
		"› 1. Update now (runs `npm install -g @openai/codex`)\n" +
		"  2. Skip\n" +
		"  3. Skip until next version\n" +
		"  Press enter to continue\n"
	if got := PaneScreen(menu); got != "update" {
		t.Errorf("0.144 menu: PaneScreen = %q, want update", got)
	}
	banner := "│ ✨ Update available! 0.144.3 -> 0.999.0   │\n" +
		"│ Run npm install -g @openai/codex to update.│\n" +
		"│ >_ OpenAI Codex (v0.144.3)                │\n"
	if got := PaneScreen(banner); got != "" {
		t.Errorf("banner after a skip: PaneScreen = %q, want none", got)
	}
}

// The same words above the composer are prose — this package's source shown in the transcript,
// say. Refusing then leaves no screen to answer, so while the composer follows nothing reads as
// a screen, idle or working.
func TestPaneScreenIgnoresTheWordsAboveTheComposer(t *testing.T) {
	quotes := map[string]string{
		"update": "  Update available · 0.159.2 → 0.999.0\n  3. Skip until next version\n  enter continue · esc skip\n",
		"locked": "  This conversation is open in another app\n  Close it there and press R to continue here.\n  r retry   f fork\n",
		"nudge":  "  Approaching rate limits\n  3. Keep current model (never show again)\n",
	}
	for _, file := range []string{"pane-idle.txt", "pane-idle-narrow.txt", "pane-working.txt"} {
		pane := readPane(t, file)
		at := strings.LastIndex(pane, "› ")
		if at < 0 {
			t.Fatalf("%s lost its composer", file)
		}
		for what, q := range quotes {
			if got := PaneScreen(pane[:at] + q + pane[at:]); got != "" {
				t.Errorf("%s with the %s words quoted: PaneScreen = %q, want none", file, what, got)
			}
		}
	}
}

func TestTerminalScreenLeavesManagedAlone(t *testing.T) {
	m := session.Meta{Name: "cx-managed", Dir: t.TempDir(), Kind: session.KindCodex, Driver: session.DriverManaged}
	if got := TerminalScreen(m); got != "" {
		t.Errorf("managed: TerminalScreen = %q, want none", got)
	}
}
