package cursor

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// PaneMode is the cursor composer's mode label read off a captured pane, "" while the composer
// is not drawn (see sessionx.PaneMode).
func PaneMode(s string) string {
	// cursor's composer footer (v2026.07.20, measured): idle placeholder "→ Add a
	// follow-up" / "→ Plan, search, build anything", with the model name ("Auto")
	// and the cwd below it; working is "Running … ctrl+c to stop". The footer only
	// exists once the composer is drawn (--trust suppresses the trust dialog, but
	// there is still nothing right after boot), so this doubles as the launch-seed
	// readiness signal. Plan mode is fixed at launch (--plan), so meta is the truth —
	// here we only distinguish drawn/not-drawn and report the non-plan label.
	for _, line := range strings.Split(agents.PaneTail(s, 4), "\n") {
		if strings.Contains(line, "Add a follow-up") ||
			strings.Contains(line, "Plan, search, build anything") ||
			strings.Contains(line, "ctrl+c to stop") || strings.Contains(line, "Running") {
			return "Default"
		}
	}
	return ""
}
