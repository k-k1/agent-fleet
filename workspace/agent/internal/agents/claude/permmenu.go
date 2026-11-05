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
	// permQuestionRe is the dialog's question line ("Do you want to proceed?", "Do you want
	// to create note.txt?", "Do you want to make this edit to x.go?").
	permQuestionRe = regexp.MustCompile(`^\s*Do you want to .*\?\s*$`)
)

// ParsePermissionMenu reads the permission dialog at the bottom of a claude pane. It answers
// false unless it finds the question line, rows numbered 1..n without a gap, and the
// "Esc to cancel" footer after them: when in doubt it reports no menu, because a misread menu
// is answered with the wrong digit.
//
// Only the LAST question line counts, so a transcript above the dialog that merely quotes one
// (this repository's own tests and comments do) cannot be read as the live menu.
func ParsePermissionMenu(pane string) (PermissionMenu, bool) {
	lines := strings.Split(strings.ReplaceAll(pane, "\r", ""), "\n")
	q := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if permQuestionRe.MatchString(lines[i]) {
			q = i
			break
		}
	}
	if q < 0 {
		return PermissionMenu{}, false
	}
	var m PermissionMenu
	for _, ln := range lines[q+1:] {
		if strings.Contains(ln, "Esc to cancel") {
			if len(m.Rows) < 2 {
				return PermissionMenu{}, false
			}
			return m, true
		}
		r := permRowRe.FindStringSubmatch(ln)
		if r == nil {
			// A label too long for the pane wraps onto an unnumbered line; it changes nothing
			// about which digit selects the row.
			if len(m.Rows) == 0 && strings.TrimSpace(ln) != "" {
				return PermissionMenu{}, false
			}
			continue
		}
		n, _ := strconv.Atoi(r[1])
		if n != len(m.Rows)+1 {
			return PermissionMenu{}, false
		}
		m.Rows = append(m.Rows, PermissionRow{Num: n, Label: r[2], Choice: classifyPermRow(r[2])})
	}
	return PermissionMenu{}, false
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
