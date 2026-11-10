package imagegen

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/kinds"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// oldStudioSessionUnsupported is the switch StudioSessionUnsupported had before its
// branches moved into studioManagedRefusal.
func oldStudioSessionUnsupported(kind, driver string) string {
	if driver != session.DriverManaged {
		return ""
	}
	switch kind {
	case session.KindOpencode:
		return "an opencode Managed session shares its MCP server with the other sessions of its folder, so it cannot be bound to an image studio; use the Terminal execution method"
	case session.KindMuse:
		return "a muse session cannot tell the image studio tools which session it is yet, so it cannot be bound to an image studio for now"
	case session.KindCopilot, session.KindCursor, session.KindKiro:
		return "a " + kind + " Managed session cannot tell the image studio tools which session it is yet, so it cannot be bound to an image studio; use the Terminal execution method"
	}
	return ""
}

func TestStudioSessionUnsupportedMatchesOldSwitch(t *testing.T) {
	spellings := append([]string(nil), kinds.All()...)
	spellings = append(spellings, "", "Claude", "OPENCODE", " kiro", "unknown")
	drivers := []string{session.DriverManaged, session.DriverTUI, "", "other"}
	refused := 0
	for _, k := range spellings {
		for _, d := range drivers {
			got, want := StudioSessionUnsupported(k, d), oldStudioSessionUnsupported(k, d)
			if got != want {
				t.Errorf("(%q, %q) = %q, old switch %q", k, d, got, want)
			}
			if want != "" {
				refused++
			}
		}
	}
	if refused == 0 {
		t.Fatal("the old switch refused nothing; the comparison proves nothing")
	}
}
