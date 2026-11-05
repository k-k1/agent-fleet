package claude

import (
	"regexp"
	"strconv"
	"strings"
)

// Permission choices the Console's permission card and the chat bridge offer. They name what
// the member means, never a position: Claude Code's menu has no fixed shape (#3354).
const (
	PermAllow  = "allow"
	PermAlways = "always"
	PermDeny   = "deny"
)

// PermissionMenu is the tool-permission dialog read off a captured pane: its numbered rows,
// each tagged with the choice it carries ("" for rows the card does not offer, such as
// "Yes, and switch to auto mode").
//
// Measured on claude 2.1.318 (testdata/permmenu): an ask-rule match draws "1. Yes / 2. No",
// Bash draws four rows with "don't ask again" second and "No" fourth, Write draws three. Keys
// aimed at fixed positions answered the two-row menu's "Always allow" with No and wrapped
// "Deny" (Down Down) back onto Yes.
type PermissionMenu struct {
	Rows []PermissionRow
}

// PermissionRow is one numbered row of the dialog.
type PermissionRow struct {
	Num    int
	Label  string
	Choice string
}

var (
	// permRowRe matches a numbered menu row, with or without the cursor glyph in front.
	permRowRe = regexp.MustCompile(`^\s*(?:❯\s*)?(\d)\.\s+(\S.*?)\s*$`)
	// permFooterRe is the dialog's footer ("Esc to cancel · Tab to amend"). It must START the
	// line: AskUserQuestion's footer also mentions Esc, after "Enter to select".
	permFooterRe = regexp.MustCompile(`^\s*Esc to cancel\b`)
)

// ParsePermissionMenu reads the permission dialog at the bottom of a claude pane. It answers
// false unless the last non-blank line is the "Esc to cancel" footer, the rows right above it
// are numbered 1..n without a gap, and a "Do you want to" question sits above row 1: when in
// doubt it reports no menu, because a misread menu is answered with the wrong digit.
//
// Anchoring on the bottom line is what keeps a dialog quoted in the transcript above (this
// repository's own tests and comments quote them) from being read as the live one: the live
// dialog replaces the composer, so nothing but blank lines follows its footer.
//
// A label too long for the pane wraps onto unnumbered lines, and so does a long question
// ("Do you want to make this edit to <long path>?"). Continuation lines are joined onto the
// row they follow before the row is classified, so "… for this session" still counts after a
// wrap.
func ParsePermissionMenu(pane string) (PermissionMenu, bool) {
	lines := strings.Split(strings.ReplaceAll(pane, "\r", ""), "\n")
	f := len(lines) - 1
	for f >= 0 && strings.TrimSpace(lines[f]) == "" {
		f--
	}
	if f < 0 || !permFooterRe.MatchString(lines[f]) {
		return PermissionMenu{}, false
	}
	var m PermissionMenu
	var cont []string // unnumbered lines seen since the last row, bottom-up
	question := false
	for i := f - 1; i >= 0 && !question; i-- {
		ln := strings.TrimSpace(lines[i])
		r := permRowRe.FindStringSubmatch(lines[i])
		switch {
		case r != nil && (len(m.Rows) == 0 || m.Rows[0].Num > 1):
			label := r[2]
			for j := len(cont) - 1; j >= 0; j-- {
				label += " " + cont[j]
			}
			cont = nil
			n, _ := strconv.Atoi(r[1])
			m.Rows = append([]PermissionRow{{Num: n, Label: label}}, m.Rows...)
		case len(m.Rows) > 0 && m.Rows[0].Num == 1:
			// Above row 1: the question, possibly wrapped over several lines. Its first line is
			// the one that opens with "Do you want to".
			if strings.HasPrefix(ln, "Do you want to ") {
				question = true
			} else if ln == "" || len(cont) >= 3 {
				return PermissionMenu{}, false
			} else {
				cont = append(cont, ln)
			}
		case ln == "" && len(m.Rows) == 0 && len(cont) == 0:
			// The live dialog leaves a blank line between its last row and the footer.
		case ln == "":
			return PermissionMenu{}, false
		default:
			cont = append(cont, ln)
		}
	}
	if !question || len(m.Rows) < 2 {
		return PermissionMenu{}, false
	}
	for i := range m.Rows {
		if m.Rows[i].Num != i+1 {
			return PermissionMenu{}, false
		}
		m.Rows[i].Choice = classifyPermRow(m.Rows[i].Label)
	}
	return m, true
}

// classifyPermRow maps a row label to the card's choice. "always" covers the rows that stop
// the same prompt coming back: "don't ask again for: <prefix>" and the edit dialog's
// "… for this session". Switching to auto mode also stops the prompts, but hands every later
// decision to a classifier, which is more than "allow this kind of call" — it stays unmapped.
func classifyPermRow(label string) string {
	l := strings.ToLower(strings.ReplaceAll(label, "’", "'"))
	switch {
	case l == "yes":
		return PermAllow
	case l == "no" || strings.HasPrefix(l, "no,") || strings.HasPrefix(l, "no "):
		return PermDeny
	case strings.Contains(l, "auto mode"):
		return ""
	case strings.HasPrefix(l, "yes") &&
		(strings.Contains(l, "don't ask again") || strings.Contains(l, "for this session") ||
			strings.Contains(l, "during this session")):
		return PermAlways
	}
	return ""
}

// Key returns the digit that selects the first row carrying choice. Claude Code selects a
// row on its digit at once (measured), so the cursor's starting row does not matter.
func (m PermissionMenu) Key(choice string) (string, bool) {
	for _, r := range m.Rows {
		if r.Choice == choice {
			return strconv.Itoa(r.Num), true
		}
	}
	return "", false
}

// Choices lists the choices the menu offers, in the card's order.
func (m PermissionMenu) Choices() []string {
	var out []string
	for _, c := range []string{PermAllow, PermAlways, PermDeny} {
		if _, ok := m.Key(c); ok {
			out = append(out, c)
		}
	}
	return out
}
