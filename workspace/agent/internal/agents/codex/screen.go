package codex

// Screens a codex Terminal pane draws outside the conversation that act on typed keys. None of
// them leaves a trace in the rollout, so the pane is the only place to read them, and a prompt
// pasted into one does not reach the model:
//
//   - The startup update menu. Its first row is "Update now", which exits the process.
//
//     Update available · 0.159.2 → 0.999.0
//     Release notes: https://github.com/openai/codex/releases/latest
//     › 1. Update now (runs `npm install -g @openai/codex`)
//     2. Skip
//     3. Skip until next version
//     enter continue · esc skip
//
//   - The lock screen of a conversation another process holds (the shared app-server, a codex
//     the user runs elsewhere). It keeps waiting after the holder lets go, until someone
//     presses r, and its single-key actions include f (fork) and q (exit):
//
//     🔒  This conversation is open in another app                    r to retry
//     Close it there and press R to continue here.
//     r retry   f fork   ←/esc command center   ctrl+c/q exit   ctrl+t transcript
//
//   - The model-switch nudge near a usage limit (docs/log/27), whose first row switches the
//     session to a smaller model: "Approaching rate limits" / "Switch to <model> for lower
//     credit usage?" / "1. Switch to <model>" / "2. Keep current model" / "3. Keep current
//     model (never show again)".
//
// The first two are captures of 0.159.2 (testdata/); the nudge could not be brought up on
// demand, so its strings come from the 0.159.2 binary and its fixture is synthetic.

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// paneScreens are the screens by their title, a row of their own below it, and, where it is
// known, a footer only the live screen draws. The footer alternatives cover the update menu's
// wording before and after 0.159.
var paneScreens = []struct {
	title, row string
	footers    []string
	state      string
}{
	{"Update available", "Skip until next version", []string{"enter continue", "Press enter to continue"}, "update"},
	{"This conversation is open in another app", "press R to continue here", []string{"f fork"}, "locked"},
	{"Approaching rate limits", "Keep current model (never show again)", nil, "model_switch"},
}

// screenTailMax bounds what may follow a screen's row: the rest of that screen (a description,
// the key hints) and nothing else. It is counted on the flattened text, so it does not depend on
// the pane's width.
const screenTailMax = 160

// composerMarks are drawn around codex's composer. A screen replaces the composer, so after a
// screen's row none of them may appear; that is what tells the live screen from the same words
// quoted in the conversation above the composer.
var composerMarks = []string{"Ask Codex to do anything", "for shortcuts", "esc to interrupt"}

// PaneScreen classifies one captured frame: "update", "locked", "model_switch" or "".
//
// A banner that stays after the choice ("Update available" above the composer) or the words in
// the transcript are not the screen: the screen's row has to follow its title, and after the row
// only the screen's own tail, short and free of the composer, may follow.
func PaneScreen(s string) string {
	flat := flatten(s)
	for _, sc := range paneScreens {
		i := strings.LastIndex(flat, sc.title)
		if i < 0 {
			continue
		}
		rest := flat[i+len(sc.title):]
		j := strings.Index(rest, sc.row)
		if j < 0 {
			continue
		}
		tail := rest[j+len(sc.row):]
		if len(tail) > screenTailMax || containsAny(tail, composerMarks) {
			continue
		}
		if sc.footers != nil && !containsAny(tail, sc.footers) {
			continue
		}
		return sc.state
	}
	return ""
}

// TerminalScreen is the screen a codex Terminal pane shows now (PaneScreen), "" for a managed
// session, which has no pane.
func TerminalScreen(m session.Meta) string {
	if m.DriverKind() == session.DriverManaged {
		return ""
	}
	return PaneScreen(tmuxx.CapturePane(session.TmuxName(m.Name)))
}

// flatten joins the frame's lines into one with single spaces. codex wraps a row at a word
// boundary itself on a narrow pane (measured at 50 columns: "Close it there and press R to
// continue" / "here."), which capture-pane -J does not undo.
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
