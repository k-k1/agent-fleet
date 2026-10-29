package cursor

// Reading a Terminal send's outcome off the pane. A prompt the TUI rejects at submit (measured
// 2026.10.16: a named model the plan cannot use) first looks accepted for about a second - the
// composer empties and a spinner shows - and then the text comes back into the composer with an
// error row under the model and directory rows:
//
//	→ say hello in one word
//
//	  Composer 2.5 Fast
//	  ~/proj
//
//	  Error: Named models unavailable
//	  Free plans can only use Auto. Switch to Auto or upgrade plans to continue.
//	  hideIcon: true
//	  hideKeybindings: true
//
// Nothing else says so: the JSONL is written when a turn ends, not when it starts, so only the
// pane can tell a rejection from a turn under way. A turn that started leaves the composer on
// its placeholder.

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// maxErrorChars bounds the error text handed back to the sender.
const maxErrorChars = 300

// keyHint is the working composer's hint at the right of its first row.
const keyHint = "ctrl+c to stop"

// spinnerRow is the spinner line a running turn draws above the composer: braille cells, a verb
// (Working, Thinking, Reading, ...) and the running token count once there is one.
var spinnerRow = regexp.MustCompile(`(?m)^\s*[\x{2800}-\x{28FF}]+\s+[A-Z][a-z]+(\s+[\d.]+k?\s+tokens)?\s*$`)

// optionRow is a "key: value" line of the toast's options (hideIcon: true), not part of its text.
var optionRow = regexp.MustCompile(`^\w+: (true|false)$`)

// PaneWorking reports whether the pane shows a turn under way. The JSONL cannot say so: it is
// written when a turn ends (see above), so a long turn reads as no state at all until it is over.
// The spinner row is the signal (measured 2026.10.22); 2026.10.16 also printed "ctrl+c to stop"
// at the right of the composer, which 2026.10.22 no longer does.
func PaneWorking(captured string) bool {
	if spinnerRow.MatchString(captured) {
		return true
	}
	lines := strings.Split(captured, "\n")
	top := composerTop(lines)
	return top >= 0 && strings.HasSuffix(strings.TrimSpace(lines[top]), keyHint)
}

// ComposerHolds reports whether the pane's composer still holds the prompt as an unsubmitted
// draft, given the first and last runes of the prompt (head and tail, whitespace removed).
// The composer is the last "→" line and the lines that follow it up to the first gap of two
// blank lines, so the model and directory rows under it never match; a draft wrapped at the
// pane width is joined back. When the screen has no composer it answers false: the old
// behaviour (the send counts as delivered) is what an unrecognised screen gets.
func ComposerHolds(captured, head, tail string) bool {
	d := squash(composerDraft(captured))
	if d == "" {
		return false
	}
	return head != "" && strings.Contains(d, head) || tail != "" && strings.Contains(d, tail)
}

// ComposerDraft reports whether the composer holds any unsent text, whoever typed it.
func ComposerDraft(captured string) bool { return composerDraft(captured) != "" }

// TerminalDraft is ComposerDraft for a Terminal pane that shows no modal: a menu row also
// starts with "→", and the free-text gate answers for it.
func TerminalDraft(m session.Meta) bool {
	if m.DriverKind() == session.DriverManaged {
		return false
	}
	s := tmuxx.CapturePane(session.TmuxName(m.Name))
	return paneModal(s, cwdForms(m)...) == "" && ComposerDraft(s)
}

// composerDraft is the text typed into the composer, "" when it is empty or not on screen.
// The placeholder ("Add a follow-up") and the hint drawn at the right of the first row ("ctrl+c
// to stop" while a turn runs) are not text: a prompt ending in "stop" or "follow-up" must not
// match them.
func composerDraft(captured string) string {
	lines := strings.Split(captured, "\n")
	top := composerTop(lines)
	if top < 0 {
		return ""
	}
	var draft strings.Builder
	blanks := 0
	for i, ln := range lines[top:] {
		ln = strings.TrimSpace(ln)
		if i == 0 {
			ln = strings.TrimSpace(strings.TrimPrefix(ln, "→"))
			ln = strings.TrimSpace(strings.TrimSuffix(ln, keyHint))
		}
		if ln == "" {
			if blanks++; blanks >= 2 {
				break
			}
			continue
		}
		blanks = 0
		draft.WriteString(ln)
		draft.WriteByte('\n')
	}
	out := strings.TrimSpace(draft.String())
	// The whole block, not its first row: "Add a follow-up" followed by more rows is a draft.
	if slices.Contains(composerMarks, out) {
		return ""
	}
	return out
}

// SubmitError is the error cursor printed under its composer after refusing a prompt, "" when
// there is none: the "Error:" row and the lines after it up to the next blank one, without the
// toast's option rows.
func SubmitError(captured string) string {
	lines := strings.Split(captured, "\n")
	// Only below the composer: an "Error:" line above it is transcript prose.
	top := composerTop(lines)
	if top < 0 {
		return ""
	}
	for i := len(lines) - 1; i > top; i-- {
		first := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(first, "Error:") {
			continue
		}
		parts := []string{first}
		for _, ln := range lines[i+1:] {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				break
			}
			if !optionRow.MatchString(ln) {
				parts = append(parts, ln)
			}
		}
		out := strings.Join(parts, " ")
		if r := []rune(out); len(r) > maxErrorChars {
			out = string(r[:maxErrorChars]) + "…"
		}
		return out
	}
	return ""
}

// composerTop is the index of the last "→" line, -1 when the screen has none.
func composerTop(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "→") {
			return i
		}
	}
	return -1
}

// squash drops every whitespace rune.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
