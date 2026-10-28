package claude

// claudePaneMode is PaneMode for claude: s is the whole captured pane.
func claudePaneMode(s string) string {
	return claudeModeLabel(paneTail(s, 3))
}

// claudeModeLabel maps claude's status-line mode strip to the chip label.
//
// Measured (claude 2.1.241, 2026-08-24; the left column is the --permission-mode value):
//
//	(unset = default) / manual  "⏸ manual mode on · ← for agents"
//	auto                        "⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
//	acceptEdits                 "⏵⏵ accept edits on (shift+tab to cycle) · …"
//	bypassPermissions           "⏵⏵ bypass permissions on (shift+tab to cycle) · …"
//	dontAsk                     "⏵⏵ don't ask on (shift+tab to cycle) · …"
//	plan                        "⏸ plan mode on (shift+tab to cycle) · …"
//
// Only manual omits "(shift+tab to cycle)" (tmuxx's modeFooterRe hit the same thing on
// 2.1.212), and manual is exactly where a session launched WITH permission prompts lands
// (docs/log/76) — so matching on "four names plus the shift+tab catchphrase" yields "".
// An empty PaneMode means "composer not drawn", which makes the launch-seed readiness
// gate (submitPromptTUI / initial prompt delivery) wait 30s before falling back to
// best-effort.
//
// So the last resort is the footer strip itself (tmuxx.ClaudeModeFooter) rather than a
// list of names: when a mode is added or renamed this degrades to "drawn, mode treated as
// Default" and delivery keeps working.
func claudeModeLabel(tail string) string {
	switch {
	case strings.Contains(tail, "plan mode on"):
		return "Plan"
	case strings.Contains(tail, "accept edits on"):
		return "Accept Edits"
	case strings.Contains(tail, "bypass permissions on"):
		return "Bypass"
	case strings.Contains(tail, "auto mode on"):
		return "Auto"
	case strings.Contains(tail, "manual mode on"):
		return "Manual"
	case strings.Contains(tail, "don't ask on"):
		return "Don't ask"
	case tmuxx.ClaudeModeFooter(tail), strings.Contains(tail, "shift+tab to cycle"):
		// An unknown mode name, or an older build where only the catchphrase is left.
		return "Default"
	}
	return ""
}
