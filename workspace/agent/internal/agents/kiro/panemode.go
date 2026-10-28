package kiro

// kiroPaneMode is PaneMode for kiro: s is the whole captured pane.
func kiroPaneMode(s string) string {
	// kiro's composer footer (2.14.1, measured): idle placeholder "ask a question or
	// describe a task ↵", with model/context on the line above it ("kiro_default ·
	// auto · ◔ n%   <cwd>"); working is "Kiro is working · Type to steer · Ctrl+S to
	// queue". The footer only exists once the composer is drawn (a pinned
	// chat.disableTrustAllConfirmation suppresses the dangerous-mode confirmation
	// dialog, but there is still nothing right after boot), so this doubles as the
	// launch-seed readiness signal. Plan mode is fixed at launch (by omitting
	// --trust-all-tools), so meta is the truth — here we only distinguish
	// drawn/not-drawn and report the non-plan label.
	for _, line := range strings.Split(paneTail(s, 4), "\n") {
		if strings.Contains(line, "ask a question or describe a task") ||
			strings.Contains(line, "Kiro is working") || strings.Contains(line, "requires approval") {
			return "Default"
		}
	}
	return ""
}
