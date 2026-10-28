package codex

// codexFooterEffortRe pulls the reasoning effort from codex's composer footer
// "<model> <effort> · <cwd>" (the word right before " · " then the cwd path).
var codexFooterEffortRe = regexp.MustCompile(`([a-z]+) +· +[~/]`)

// codexPaneMode is PaneMode for codex: s is the whole captured pane.
func codexPaneMode(s string) string {
	// codex's composer footer is "<model> <effort> · <cwd>". Through 0.144 it
	// appended "Plan mode [(shift+tab to cycle)]" in plan mode; 0.145 removed that
	// textual marker. Keep accepting it for pinned/older versions, while the bare
	// footer remains the composer-readiness signal and mirror-driven mode is persisted
	// in meta (rememberCodexTUIMode). Never inspect history text here: a stale
	// "… for Plan mode." line would spoof the current state.
	for _, line := range strings.Split(paneTail(s, 3), "\n") {
		if codexFooterEffortRe.MatchString(line) {
			if strings.Contains(line, "Plan mode") {
				return "Plan"
			}
			return "Default"
		}
	}
	return ""
}
