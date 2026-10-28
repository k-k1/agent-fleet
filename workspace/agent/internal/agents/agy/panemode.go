package agy

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// PaneMode is the agy composer's mode label read off a captured pane, "" while the composer
// is not drawn (see sessionx.PaneMode).
func PaneMode(s string) string {
	// agy's composer footer (v1.1.4, measured): left "? for shortcuts" (idle) or
	// "esc to cancel" (generating), right "<model>" — "plan · <model>" in plan
	// mode. The footer only exists once the composer is drawn (the "Signing
	// in..." boot screen has none, and text typed into it is eaten), so this
	// doubles as the launch-seed readiness signal, like the other kinds.
	for _, line := range strings.Split(agents.PaneTail(s, 3), "\n") {
		if strings.Contains(line, "? for shortcuts") || strings.Contains(line, "esc to cancel") {
			if strings.Contains(line, "plan · ") {
				return "Plan"
			}
			return "Default"
		}
	}
	return ""
}
