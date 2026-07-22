package copilot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCopilotRTKToggle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	path := filepath.Join(dir, "hooks", "rtk.json")

	// off with no file: nothing created.
	ApplyRTK(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("off created a file when none existed")
	}

	// on: writes the user-scope preToolUse hook wiring rtk hook copilot.
	ApplyRTK(true)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("on did not create hook file: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"preToolUse"`, `"matcher": "bash"`, `rtk hook copilot`} {
		if !strings.Contains(got, want) {
			t.Fatalf("hook file missing %q:\n%s", want, got)
		}
	}

	// on again: idempotent (byte-identical, no partial rewrite).
	ApplyRTK(true)
	b2, _ := os.ReadFile(path)
	if string(b2) != got {
		t.Fatalf("re-apply changed content:\n%s", string(b2))
	}

	// off: hook file removed so copilot stops routing through rtk.
	ApplyRTK(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("off did not remove hook file")
	}

	// off again: still a no-op, no file resurrected.
	ApplyRTK(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("off-idempotent created a file")
	}
}
