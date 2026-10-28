package opencode

// opencodeStatusAgentRe pulls the current agent from opencode's composer status line
// ("Plan auto · <model> …" / "Build auto · …"), the ground-truth mode the TUI shows.
var opencodeStatusAgentRe = regexp.MustCompile(`([A-Za-z][\w-]*) +auto +·`)

// opencodePaneMode is PaneMode for opencode: s is the whole captured pane.
func opencodePaneMode(s string) string {
	// The composer status line ("<Agent> auto · …") sits a few lines above the very
	// bottom (above the border + token/commands footer). The agent name IS the mode.
	if m := opencodeStatusAgentRe.FindStringSubmatch(paneTail(s, 8)); m != nil {
		return titleFirst(m[1]) // "Plan" / "Build" / …
	}
	return ""
}

// titleFirst upper-cases the first rune ("plan" → "Plan", "build" → "Build").
func titleFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
