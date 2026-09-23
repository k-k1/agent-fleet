package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
)

// The reference-image gate reads the Files pane's own denylist, not a copy of it (ADR 0100
// decision 4). Checked through the installed hook, with folders that are on the real list.
func TestImagegenInputGateUsesTheFilesDenylist(t *testing.T) {
	if imagegen.BrowseRootDir == nil || imagegen.PathDenied == nil {
		t.Fatal("the imagegen input gate hooks are not installed")
	}
	for _, rel := range []string{".ssh/id.png", ".config/agent-fleet/x.png", ".codex/generated_images/a.png"} {
		if !imagegen.PathDenied(rel) {
			t.Errorf("PathDenied(%q) = false, want the Files pane's refusal", rel)
		}
	}
	if imagegen.PathDenied("generated/console/inputs/a.png") {
		t.Error("the member's own upload folder is denied")
	}
}
