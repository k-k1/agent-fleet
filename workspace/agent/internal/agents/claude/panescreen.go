package claude

// compactPctRe / compactElapsedRe pull the percent and the elapsed timer out of the
// captured pane text. The elapsed match is anchored to the parenthetical that follows
// "Compacting …" so an unrelated "(3s)" elsewhere on screen can't be mistaken for it.
var (
	compactPctRe     = regexp.MustCompile(`(\d{1,3})%`)
	compactElapsedRe = regexp.MustCompile(`Compacting[^\n(]*\((\d+m \d+s|\d+m|\d+s)\)`)

	// resumeMenuRe matches the startup resume selector by its numbered option-1 line
	// ("1. Resume from summary"), not a bare "Resume full session" substring. This repo's
	// own source documents the menu wording — MirrorView.tsx quotes "2. Resume full session
	// as-is" and this file's doc comment lists both phrases — so a substring match would
	// flag an agent merely editing those files as parked at the menu. The numbered option-1
	// line appears in the live menu but in none of that prose (the FE quotes only option 2;
	// the comment has no numeric prefix).
	resumeMenuRe = regexp.MustCompile(`\d+\.\s+Resume from summary`)
)

// compactProgress is the parsed state of a running auto-compaction: the CLI draws a
// "Compacting conversation… (2m 3s) [====] 74%" line, and the chat surfaces the same
// percent/elapsed as a progress bar instead of a blind spinner. Fields are best-effort
// — either may be zero/empty if that CLI version renders the line differently.
type compactProgress struct {
	Pct     int    `json:"pct"`               // 0–100; -1 when no percent was on screen
	Elapsed string `json:"elapsed,omitempty"` // e.g. "2m 3s" (verbatim from the pane)
}

// classifyClaudePane is the pure pane-text classifier behind sessionTerminalState,
// split out so it can be unit-tested without tmux. Both matches are deliberately
// narrow so an agent editing text that merely contains the words is not mistaken for
// a live menu: "Compacting conversation" is the full CLI phrase (not a bare
// "Compacting", which an i18n value "state.compacting" contains), and the resume menu
// is keyed on its numbered option-1 line (see resumeMenuRe) rather than a bare
// "Resume full session" (which this repo's own Console/Go source quotes verbatim).
func classifyClaudePane(s string) (string, *compactProgress) {
	switch {
	case resumeMenuRe.MatchString(s):
		return "resume", nil
	case strings.Contains(s, "Compacting conversation"):
		return "compacting", parseCompactProgress(s)
	default:
		return "", nil
	}
}

// parseCompactProgress reads the percent and elapsed timer off a pane already known to
// be compacting. pct is -1 when the CLI hasn't drawn a percentage yet, so the chat can
// keep the spinner until the first tick lands.
func parseCompactProgress(s string) *compactProgress {
	p := &compactProgress{Pct: -1}
	if m := compactPctRe.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 0 && n <= 100 {
			p.Pct = n
		}
	}
	if m := compactElapsedRe.FindStringSubmatch(s); m != nil {
		p.Elapsed = m[1]
	}
	return p
}
