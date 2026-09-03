package sessionx

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// compactProgress is the parsed state of a running auto-compaction: the CLI draws a
// "Compacting conversation… (2m 3s) [====] 74%" line, and the chat surfaces the same
// percent/elapsed as a progress bar instead of a blind spinner. Fields are best-effort
// — either may be zero/empty if that CLI version renders the line differently.
type compactProgress struct {
	Pct     int    `json:"pct"`               // 0–100; -1 when no percent was on screen
	Elapsed string `json:"elapsed,omitempty"` // e.g. "2m 3s" (verbatim from the pane)
}

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

// sessionTerminalState detects claude terminal-only states that the chat view can't
// otherwise see — they aren't transcript events or AskUserQuestion tool calls — by
// capturing the pane and matching the CLI's on-screen text. Returns:
//
//	"resume"     — parked at the startup "Resume from summary / Resume full session /
//	               Don't ask me again" menu (a chat user who pressed 再開して続ける is
//	               stuck here; keystrokes go to the menu, not a prompt).
//	"compacting" — auto-compaction (context compression) is running; the second return
//	               carries the parsed progress bar (nil for the other states).
//	""           — none detected.
//
// Best-effort: the wording is claude-CLI-specific, so a version bump may need the
// match strings updated.
func sessionTerminalState(name string) (string, *compactProgress) {
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return "", nil
	}
	out, err := tmuxx.Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return "", nil
	}
	return classifyClaudePane(string(out))
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

// codexTerminalState detects codex terminal-only states the chat can't otherwise see,
// the codex counterpart of sessionTerminalState. Returns:
//
//	"update" — parked at the startup "✨ Update available!" menu (1. Update now /
//	           2. Skip / 3. Skip until next version). Keystrokes go to the menu, and
//	           "Update now" exits the process (the pane's tmux session dies with it),
//	           so the mirror must surface the choice instead of accepting a prompt.
//	""       — none detected.
//
// Best-effort, like sessionTerminalState: the wording is codex-CLI-specific
// (verified on 0.144.3), so a version bump may need the match strings updated.
func codexTerminalState(name string) string {
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return ""
	}
	out, err := tmuxx.Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	if isCodexUpdateMenu(string(out)) {
		return "update"
	}
	return ""
}

// isCodexUpdateMenu matches the update menu in captured pane text. Both markers are
// required: after a choice is made the "Update available!" banner STAYS on screen
// (redrawn above the composer), and only the menu's "Press enter to continue" footer
// goes away — the banner alone must not re-trigger the state.
func isCodexUpdateMenu(s string) bool {
	return strings.Contains(s, "Update available!") && strings.Contains(s, "Press enter to continue")
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
