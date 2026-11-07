package claude

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
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
	// Body is the text the dialog draws above its question: the tool's title, description and
	// target (the command or file), between the dialog's top rule and "Do you want to …".
	// BodyOK is false when no such rule was found, so nothing can be said about the target.
	Body   []string
	BodyOK bool
	// RuleWidth is the width of the dialog's top rule, i.e. of the pane.
	RuleWidth int
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
	// permLabelRuleRe is the rule claude draws under the dialog when it runs with --name, which
	// every Agent Fleet session does: "──── [AF:<session>] <title> ─". Measured on 2.1.318 in a
	// live session (testdata/permmenu/bash_ask_rule_af_label.txt); a bare tmux claude has none.
	// permTopRuleRe is the solid rule that opens the dialog.
	permTopRuleRe   = regexp.MustCompile(`^─{20,}\s*$`)
	permLabelRuleRe = regexp.MustCompile(`^\s*─{3,}.*─\s*$`)
)

// ParsePermissionMenu reads the permission dialog at the bottom of a claude pane. It answers
// false unless the last non-blank line (below the session's name rule, if any) is the
// "Esc to cancel" footer, the rows right above it
// are numbered 1..n without a gap, and a "Do you want to" question sits above row 1: when in
// doubt it reports no menu, because a misread menu is answered with the wrong digit.
//
// Anchoring on the bottom line is what keeps a dialog quoted in the transcript above (this
// repository's own tests and comments quote them) from being read as the live one: the live
// dialog replaces the composer, so nothing but blank lines and the name rule follow its footer.
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
	if f >= 0 && permLabelRuleRe.MatchString(lines[f]) {
		f--
		for f >= 0 && strings.TrimSpace(lines[f]) == "" {
			f--
		}
	}
	if f < 0 || !permFooterRe.MatchString(lines[f]) {
		return PermissionMenu{}, false
	}
	var m PermissionMenu
	var cont []string // unnumbered lines seen since the last row, bottom-up
	question := false
	qIdx := -1
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
				qIdx = i
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
	// The body runs up to the nearest full-width solid rule above the question (the dashed
	// "╌" rules inside the body are not it).
	for j := qIdx - 1; j >= 0; j-- {
		if permTopRuleRe.MatchString(lines[j]) {
			m.Body, m.BodyOK, m.RuleWidth = lines[j+1:qIdx], true, len([]rune(strings.TrimSpace(lines[j])))
			break
		}
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

// TargetCheck is the outcome of comparing a pending payload's target with the dialog on screen.
type TargetCheck int

const (
	// TargetUnchecked: the payload names no target (just a tool, or prose), so there is nothing
	// to compare. Fail-open: the prompt token still binds the answer, and refusing here would
	// make every prompt of such a tool unanswerable from the card.
	TargetUnchecked TargetCheck = iota
	// TargetMatches: the dialog shows the payload's target.
	TargetMatches
	// TargetDiffers: the dialog shows something else, or its body could not be read. Fail-closed:
	// a false refusal costs a retry in the Console, a false accept answers the wrong tool.
	TargetDiffers
)

// dialogTitles maps the tool in the payload to the title line the dialog opens with. A tool
// not listed here cannot be matched to a dialog, so its answer is refused.
var dialogTitles = map[string][]string{
	"Bash":         {"Bash command"},
	"Edit":         {"Edit file"},
	"MultiEdit":    {"Edit file"},
	"Write":        {"Create file", "Overwrite file"},
	"NotebookEdit": {"Edit notebook"},
	"Read":         {"Read file"},
}

// payloadTarget splits the permission payload "Tool · target" (permToolDetail) into the tool
// and its target, with the 100-rune truncation mark removed. ok is false for anything else: a
// bare tool name, or the prose claude's Notification hook sends.
func payloadTarget(detail string) (tool, target string, ok bool) {
	tool, target, found := strings.Cut(detail, " · ")
	tool = strings.TrimSpace(tool)
	target = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(target), "…"))
	if !found || tool == "" || strings.ContainsAny(tool, " \t") || target == "" {
		return "", "", false
	}
	return tool, target, true
}

// permDashRuleRe is the dashed rule that fences the dialog's target (command, path, diff).
var permDashRuleRe = regexp.MustCompile(`^\s*╌{10,}\s*$`)

// CheckTarget compares the payload's target with the dialog on screen (#3359). The dialog is
// read by structure, never searched as free text: the title must be the payload tool's, and the
// target is only the command between the two dashed rules (Bash) or the path lines under the
// title (file tools). A description, a diff or a "Tip:" never counts as evidence.
//
// Whitespace is kept: only the dialog's one-space indent is removed, and a wrapped line is
// rejoined only when the terminal broke it: the line reaches the pane's right edge (its indent
// plus its columns within one column of ruleWidth), or the next word would not have fit. Any other
// shorter line ends in a real newline, which is never joined. Residual: a real newline right after a line that happens to fill the pane exactly is
// indistinguishable from a wrap; that needs a Q whose first command line is pane-wide, or ends
// where the next word would not have fit, and whose continuation completes P's command. The
// payload keeps no newline information, so this cannot be closed here. The prompt token binds the
// answer to the payload only, not to the screen: if the next dialog is drawn before the hook
// rewrites the payload (#3359) and is such a Q, the answer lands on Q (follow-up: #3521).
// cwd is the session directory, for a path drawn relative to it. The one rewrite accepted is the "rtk " prefix a command hook puts on the command
// (measured: bash_four_options.txt); the payload holds the command as it was before.
func (m PermissionMenu) CheckTarget(detail, cwd string) TargetCheck {
	tool, target, ok := payloadTarget(detail)
	if !ok {
		return TargetUnchecked
	}
	if !m.BodyOK {
		return TargetDiffers
	}
	truncated := strings.HasSuffix(strings.TrimSpace(detail), "…")
	titles, known := dialogTitles[tool]
	body := m.Body
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	if !known || len(body) == 0 {
		return TargetDiffers
	}
	title := strings.TrimSpace(body[0])
	titled := false
	for _, t := range titles {
		titled = titled || t == title
	}
	if !titled {
		return TargetDiffers
	}
	// The target region: lines up to the first dashed rule (file tools), or between the first
	// and the second (Bash).
	var before, between []string
	i := 1
	for ; i < len(body) && !permDashRuleRe.MatchString(body[i]); i++ {
		before = append(before, body[i])
	}
	if i >= len(body) {
		return TargetDiffers // no fenced region: the layout is not the one measured
	}
	for i++; i < len(body) && !permDashRuleRe.MatchString(body[i]); i++ {
		between = append(between, body[i])
	}
	if tool == "Bash" {
		// The description sits above the first rule and is not the command.
		if i >= len(body) {
			return TargetDiffers
		}
		return matchLogicalLine(between, target, truncated, m.RuleWidth, func(shown string) bool { return shown == target || shown == "rtk "+target })
	}
	cands := []string{target}
	if rel, err := filepath.Rel(cwd, target); cwd != "" && err == nil {
		cands = append(cands, rel)
	}
	return matchLogicalLine(before, target, truncated, m.RuleWidth, func(shown string) bool {
		for _, c := range cands {
			if shown == c {
				return true
			}
		}
		return false
	})
}

// cols is the display width of s, and whether it can be trusted. East Asian wide runes take
// two columns; a zero-width or combining rune makes the width unknowable here (it would be
// counted as a column and make a short line look pane-wide), so such a line reports ok=false
// and is never treated as filling the pane.
func cols(s string) (n int, ok bool) {
	for _, r := range s {
		if unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc, unicode.Cf, unicode.Cc) {
			return 0, false
		}
		n++
		if r >= 0x1100 && (r <= 0x115F || (r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) ||
			(r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE30 && r <= 0xFE6F) || (r >= 0xFF00 && r <= 0xFF60) || (r >= 0xFFE0 && r <= 0xFFE6)) {
			n++
		}
	}
	return n, true
}

// matchLogicalLine tries the first line of region as it stands, then rejoined with the lines the
// terminal wrapped it over (with no separator, or with the space the wrap swallowed). A
// truncated target (the payload cuts at 100 runes) only has to prefix the shown line.
func matchLogicalLine(region []string, target string, truncated bool, width int, eq func(string) bool) TargetCheck {
	var lines []string
	for _, ln := range region {
		ln = strings.TrimRight(strings.TrimPrefix(ln, " "), " \t")
		lines = append(lines, ln)
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	// opts[k] are the separators that may sit between lines[k] and lines[k+1]. A line that
	// reaches the edge was broken by the terminal (hard wrap, or the swallowed space); a line that
	// stops short because the next word did not fit was word-wrapped, which swallows one space.
	// Each boundary is decided on its own, so a command can mix both kinds.
	last := len(lines) - 1
	var opts [][]string
	for k := 0; k < len(lines)-1; k++ {
		c, ok := cols(lines[k])
		if !ok {
			// Unknown width and more lines below: whether this line was wrapped cannot be told,
			// and stopping here would leave the continuation unchecked.
			return TargetDiffers
		}
		if width > 0 && c+1 >= width-1 {
			opts = append(opts, []string{"", " "})
		} else if width > 0 && wordWrapped(c, lines[k+1], width) {
			opts = append(opts, []string{" "})
		} else {
			last = k
			break
		}
	}
	// Only a boundary with two separators multiplies the search; word-wrap boundaries have one.
	branching := 0
	for _, o := range opts {
		if len(o) > 1 {
			branching++
		}
	}
	if branching > 12 {
		return TargetDiffers // too many ambiguous boundaries to enumerate: fail closed
	}
	// The logical line is compared only once complete: an early physical line equal to P proves
	// nothing about what the continuation adds.
	var try func(i int, shown string) bool
	try = func(i int, shown string) bool {
		if i > last {
			if truncated {
				return strings.HasPrefix(shown, target)
			}
			return eq(shown)
		}
		for _, sep := range opts[i-1] {
			if try(i+1, shown+sep+lines[i]) {
				return true
			}
		}
		return false
	}
	if len(lines) > 0 && try(1, lines[0]) {
		return TargetMatches
	}
	return TargetDiffers
}

// wordWrapped reports whether a line of c columns ended short of the pane's edge because the
// next word did not fit, which is how Claude Code's renderer breaks a line at a space (measured
// on a narrow pane: "cp js.txt" / "queue.txt;"). A next line that starts with whitespace is a
// real indent, never a wrap. The join is still compared in full against the payload, so the
// wider test only costs the residual already described on CheckTarget.
func wordWrapped(c int, next string, width int) bool {
	if next == "" || next[0] == ' ' || next[0] == '\t' {
		return false
	}
	f := strings.Fields(next)
	if len(f) == 0 || unicode.IsSpace([]rune(next)[0]) {
		return false
	}
	w, ok := cols(f[0])
	return ok && c+1+1+w >= width
}
