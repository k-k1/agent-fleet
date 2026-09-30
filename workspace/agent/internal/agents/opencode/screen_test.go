package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readPane(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The captures are opencode 1.18.33 panes launched without --auto (#1263): the permission
// prompt of an external_directory read at 100 columns and on a narrow pane, its "Always allow"
// step, and the ordinary screens after an allow and after a reject. A pasted line + Enter into
// the prompt allowed the read, so the prompt must read as one and the rest must not.
func TestPanePermissionReadsCaptures(t *testing.T) {
	for file, want := range map[string]bool{
		"pane-permission.txt":        true,
		"pane-permission-narrow.txt": true,
		"pane-permission-always.txt": true,
		"pane-idle.txt":              false,
		"pane-after-reject.txt":      false,
	} {
		if got := panePermission(readPane(t, file)); got != want {
			t.Errorf("%s: panePermission = %v, want %v", file, got, want)
		}
	}
}

// The same words above the composer are prose, not the prompt.
func TestPanePermissionIgnoresTheWordsAboveTheComposer(t *testing.T) {
	pane := readPane(t, "pane-idle.txt")
	at := strings.LastIndex(pane, "  ┃  Build")
	if at < 0 {
		t.Fatal("fixture lost its composer")
	}
	quote := "     △ Permission required\n      Allow once   Allow always   Reject     ⇆ select  enter confirm\n"
	if panePermission(pane[:at] + quote + pane[at:]) {
		t.Error("the prompt's words above the composer read as the prompt")
	}
}
