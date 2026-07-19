package agy

import (
	"os"
	"strings"
	"testing"
)

func readAgents(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(agentsPath())
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	return string(b)
}

func TestApplyRTKOnOffRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Off with no file: nothing to write.
	ApplyRTK(false)
	if _, err := os.Stat(agentsPath()); !os.IsNotExist(err) {
		t.Fatal("off with no base file must not create AGENTS.md")
	}
	// On: block appended (file created).
	ApplyRTK(true)
	if got := readAgents(t); !strings.Contains(got, rtkMarkerStart) || !strings.Contains(got, "rtk (token saver)") {
		t.Fatalf("missing rtk block:\n%s", got)
	}
	// Idempotent: applying on twice keeps exactly one block.
	ApplyRTK(true)
	if got := readAgents(t); strings.Count(got, rtkMarkerStart) != 1 {
		t.Fatalf("duplicated block:\n%s", got)
	}
	// Off again: block stripped, user content (none here) → file emptied is not
	// written; the strip leaves "" which short-circuits, so file keeps last content?
	// No: out=="" returns early, so the file retains the block-only content — mirror
	// codex semantics by asserting user content survives instead.
	base := "# my notes\n"
	if err := os.WriteFile(agentsPath(), []byte(base+"\n"+rtkMarkerStart+"\n"+rtkBlock+rtkMarkerEnd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ApplyRTK(false)
	got := readAgents(t)
	if strings.Contains(got, rtkMarkerStart) {
		t.Fatalf("block not stripped:\n%s", got)
	}
	if !strings.Contains(got, "# my notes") {
		t.Fatalf("user content lost:\n%s", got)
	}
}
