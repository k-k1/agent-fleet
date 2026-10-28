package kiro

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

// PaneMode is the kiro composer's mode label read off a captured pane, "" while the composer
// is not drawn (see sessionx.PaneMode).
func PaneMode(s string) string {
	// kiro's composer footer (2.14.1, measured): idle placeholder "ask a question or
	// describe a task ↵", with model/context on the line above it ("kiro_default ·
	// auto · ◔ n%   <cwd>"); working is "Kiro is working · Type to steer · Ctrl+S to
	// queue". The footer only exists once the composer is drawn (a pinned
	// chat.disableTrustAllConfirmation suppresses the dangerous-mode confirmation
	// dialog, but there is still nothing right after boot), so this doubles as the
	// launch-seed readiness signal. Plan mode is fixed at launch (by omitting
	// --trust-all-tools), so meta is the truth — here we only distinguish
	// drawn/not-drawn and report the non-plan label.
	for _, line := range strings.Split(agents.PaneTail(s, 4), "\n") {
		if strings.Contains(line, "ask a question or describe a task") ||
			strings.Contains(line, "Kiro is working") || strings.Contains(line, "requires approval") {
			return "Default"
		}
	}
	return ""
}
