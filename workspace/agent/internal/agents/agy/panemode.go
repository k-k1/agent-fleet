package agy

// agyPaneMode is PaneMode for agy: s is the whole captured pane.
func agyPaneMode(s string) string {
	// agy's composer footer (v1.1.4, measured): left "? for shortcuts" (idle) or
	// "esc to cancel" (generating), right "<model>" — "plan · <model>" in plan
	// mode. The footer only exists once the composer is drawn (the "Signing
	// in..." boot screen has none, and text typed into it is eaten), so this
	// doubles as the launch-seed readiness signal, like the other kinds.
	for _, line := range strings.Split(paneTail(s, 3), "\n") {
		if strings.Contains(line, "? for shortcuts") || strings.Contains(line, "esc to cancel") {
			if strings.Contains(line, "plan · ") {
				return "Plan"
			}
			return "Default"
		}
	}
	return ""
}
