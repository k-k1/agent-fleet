package copilot

// copilotPaneMode is PaneMode for copilot: s is the whole captured pane.
func copilotPaneMode(s string) string {
	// copilot's composer footer (v1.0.73, measured): idle "/ commands · ? help · tab
	// next tab", draft "@ files · # issues", working "◎ Working esc interrupt" —
	// right edge shows the model ("Auto → gpt-5-mini"). The footer exists only
	// once the composer is drawn (the trust dialog / boot screen has none), so
	// this doubles as the launch-seed readiness signal. Mode has no footer
	// marker; plan mode is fixed at launch (--mode plan), so meta is the truth —
	// here we only distinguish drawn/not-drawn and report the non-plan label.
	for _, line := range strings.Split(paneTail(s, 3), "\n") {
		if strings.Contains(line, "/ commands") || strings.Contains(line, "@ files") ||
			strings.Contains(line, "Working") {
			return "Default"
		}
	}
	return ""
}
