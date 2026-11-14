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
	// Clipped is set when BodyOK is not because the dialog is taller than the pane: no top rule
	// above the question and the pane's first line already drawn. Claude Code draws on the
	// alternate screen (measured: tmux history_size 0), so the lines above are drawn nowhere and
	// Body then holds what is visible, from the pane's first line down to the question.
	Clipped bool
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
	if !m.BodyOK && strings.TrimSpace(lines[0]) != "" {
		m.Body, m.Clipped = lines[:qIdx], true
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
// shorter line ends in a real newline, which is never joined.
//
// This function sees only the payload's one-line target (the command's first line, cut at 100
// runes), so it cannot tell a real newline from a wrap, nor see lines after the first. The
// prompt token binds an answer to the payload, not to the screen: if the next dialog is drawn
// before the hook rewrites the payload (#3359), the answer lands on it. A Bash answer must
// therefore go through CheckTargetCommand, which compares the dialog with the whole command.
// cwd is the session directory, for a path drawn relative to it. The one rewrite accepted is the "rtk " a command hook puts in front of
// a command (measured: bash_four_options.txt); the payload holds the command as it was before. CheckTargetCommand accepts it in
// front of any simple command (stripRtk); this one-line comparison only at the start.
func (m PermissionMenu) CheckTarget(detail, cwd string) TargetCheck {
	return m.checkTarget(detail, cwd, nil)
}

// CheckTargetCommand is CheckTarget plus, for a Bash target, the whole command the hook saw
// (#3521). The dialog must reconstruct to exactly that text with every real newline kept, so a
// dialog that differs from the pending command by a line break, or by lines after the first,
// never matches. A Bash payload with no command (command == "") is refused: when in doubt the
// answer is "no match". The dialog is read as drawn; a command the dialog shortens or reflows
// beyond a wrap does not reconstruct and is refused too.
//
// A dialog taller than the pane shows only the command's end; checkClippedCommand compares that
// tail instead.
//
// Limit: a screen drawn identically for P wrapped by the terminal and for Q with a real newline
// where a wrap was possible (a pane-wide line, or a next word that would not have fit) cannot be
// told apart by any reading of the screen, and matches. Everything else that differs by a
// newline, or by lines after the first, is refused. The answer path closes that case off the
// screen: it also checks the dialog against the newer call a PreToolUse hook has already
// recorded, and refuses when both match (sessionx.newerCallOnScreen).
func (m PermissionMenu) CheckTargetCommand(detail, cwd, command string) TargetCheck {
	// A bare "Bash" (the hook saw an empty command) names nothing to compare; it is not the
	// untargeted case that CheckTarget lets through.
	if tool, _, _ := strings.Cut(detail, " · "); strings.TrimSpace(tool) == "Bash" {
		if _, _, ok := payloadTarget(detail); !ok || strings.TrimSpace(command) == "" {
			return TargetDiffers
		}
	}
	return m.checkTarget(detail, cwd, &command)
}

func (m PermissionMenu) checkTarget(detail, cwd string, command *string) TargetCheck {
	tool, target, ok := payloadTarget(detail)
	if !ok {
		return TargetUnchecked
	}
	if !m.BodyOK {
		if m.Clipped && tool == "Bash" && command != nil && strings.HasPrefix(normalizeCommand(*command), target) {
			return m.checkClippedCommand(*command)
		}
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
		between, width := stripCommandBar(between, m.RuleWidth)
		if command != nil {
			// The payload's target must be the start of that command, or the two were read from
			// different prompts. The whole command is then compared in place of the first line.
			if !strings.HasPrefix(normalizeCommand(*command), target) || !matchFullCommand(between, *command, width, false) {
				return TargetDiffers
			}
			return TargetMatches
		}
		return matchLogicalLine(between, target, truncated, width, func(shown string) bool { return shown == target || shown == "rtk "+target })
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

// wordWrapRun is wordWrapped for a swallowed run of blanks: the fewest blanks n (at least 1) for
// which a line of c columns followed by n blanks and the next word would not have fit. ok is false
// when no run up to maxWrapSpaces explains a break there, or the next line is a real indent.
func wordWrapRun(c int, next string, width int) (n int, ok bool) {
	if next == "" || unicode.IsSpace([]rune(next)[0]) {
		return 0, false
	}
	f := strings.Fields(next)
	if len(f) == 0 {
		return 0, false
	}
	w, known := cols(f[0])
	if !known {
		return 0, false
	}
	// The same edge as wordWrapped (n = 1 gives its c+1+1+w >= width). Only runs that overflowed
	// are measured; whether a run that exactly fills the line also breaks is not, so this may
	// accept one blank fewer than the renderer needs.
	n = max(1, width-1-w-c)
	return n, n <= maxWrapSpaces
}

// permCommandBar is the left bar ("│ ") claude 2.1.319 draws before each line of the command in
// some dialogs (measured: the "Dangerous rm operation" one, wrapped continuation lines included).
const permCommandBar = "\u2502 "

// stripCommandBar removes that bar, and the width it takes, when every non-blank line of the
// command region carries it. One bar is removed per line and only then, so a command whose own
// lines start with the bar character is still compared with it. The returned width is the room
// left for the command.
func stripCommandBar(region []string, width int) ([]string, int) {
	barred := false
	for _, ln := range region {
		if strings.TrimSpace(ln) == "" || isBareBar(ln) {
			continue
		}
		if !strings.HasPrefix(strings.TrimPrefix(ln, " "), permCommandBar) {
			return region, width
		}
		barred = true
	}
	if !barred {
		return region, width
	}
	out := make([]string, len(region))
	for i, ln := range region {
		if rest, ok := strings.CutPrefix(strings.TrimPrefix(ln, " "), permCommandBar); ok {
			out[i] = " " + rest
		} else if isBareBar(ln) {
			out[i] = ""
		} else {
			out[i] = ln
		}
	}
	return out, width - len([]rune(permCommandBar))
}

// maxWrapSpaces bounds the run of blanks a wrap can swallow. The renderer drops the whole run at
// the break, not one space (measured on 2.1.319: "│     expect(…" drawn as "│" over "expect(…"),
// so an indented line whose next word does not fit loses its indent.
const maxWrapSpaces = 16

// wrapSeps is what a boundary the terminal could have wrapped at may hide: a run of between from
// and maxWrapSpaces blanks, or a real newline. The command text prunes all but the right count.
func wrapSeps(from int) []string {
	seps := make([]string, 0, maxWrapSpaces+1)
	for n := from; n <= maxWrapSpaces; n++ {
		seps = append(seps, strings.Repeat(" ", n))
	}
	return append(seps, "\n")
}

// isBareBar reports a barred blank line of the command: tmux drops the trailing blank, so the bar
// is all that is left of " │ ".
func isBareBar(ln string) bool {
	return strings.TrimSpace(ln) == strings.TrimSpace(permCommandBar)
}

// normalizeCommand is a command's text as the dialog can show it: no trailing blanks on a line
// (the dialog drops them), no leading or trailing blank lines, \n line ends.
func normalizeCommand(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// stripRtk removes the "rtk " a command hook puts in front of a simple command, so the command
// the dialog draws compares equal to the one the PreToolUse hook saw. The hook rewrites any
// simple command it supports, not only the first: measured on claude 2.1.319 with rtk 1.2.0,
// "…; done; ls" was drawn as "…; done; rtk ls", and Allow was refused as target_mismatch (#3725).
//
// It follows `rtk rewrite` (rtk 1.2.0), and loosens it only where a command really starts (after
// do/then/else, where the hook skips compound commands). When in doubt it removes nothing,
// which costs a refused Allow; removing too much can make two different commands compare equal
// and send an answer to the wrong prompt. An "rtk " is removed only where a simple command
// starts:
//   - at the start, after a newline, or after an unquoted ; & or |, past a do/then/else
//     keyword;
//   - past the prefixes the hook skips: NAME=value assignments, "time" or "time -p", and a bare
//     command/builtin/exec/nohup/nice (not followed by an option);
//   - inside the script of a bash/sh/zsh -c (by name or path) whose script is one quoted word,
//     which the hook rewrites too.
//
// Nothing is removed inside other quotes, in a here-document body, or right after ( or { (those
// do not start a command here; a newline after them does): the hook leaves those alone, and
// there an "rtk " is data. One "rtk " at most per command. env and timeout in any form, nice
// with options, and assignment values with ${…}, $$ or $'…' are prefixes the hook follows and
// this does not: those dialogs still refuse Allow.
//
// partial is for a text that may end mid-word (a reconstruction still growing line by line): a
// trailing "r", "rt" or "rtk" where a command starts is dropped too, and an unterminated bash -c
// script is stripped as far as it goes, since the next line may complete either.
func stripRtk(s string, partial bool) string {
	var b strings.Builder
	start := true
	var quote byte // '\'', '"', or 'D' for $'…'
	var heredocs []heredocDelim
	for i := 0; i < len(s); {
		c := s[i]
		if quote != 0 {
			b.WriteByte(c)
			i++
			switch {
			case c == '\\' && quote != '\'' && i < len(s):
				b.WriteByte(s[i])
				i++
			case c == '\'' && (quote == '\'' || quote == 'D'), c == '"' && quote == '"':
				quote = 0
			}
			continue
		}
		if start {
			rest := s[i:]
			if n := rtkCommandPrefix(rest); n > 0 {
				b.WriteString(rest[:n])
				i += n
				continue
			}
			if strings.HasPrefix(rest, "rtk ") {
				i += len("rtk ")
				start = false
				continue
			}
			if partial && (rest == "r" || rest == "rt" || rest == "rtk") {
				break
			}
			if n, script, closed, ok := shellScript(rest); ok && (closed || partial) {
				b.WriteString(rest[:n])
				b.WriteString(stripRtk(script, partial && !closed))
				if closed {
					b.WriteByte(rest[n-1])
				}
				i += n + len(script)
				if closed {
					i++
				}
				start = false
				continue
			}
			start = false
		}
		switch c {
		case '\'', '"':
			quote = c
		case '$':
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteString("$'")
				i += 2
				quote = 'D'
				continue
			}
		case '\\':
			b.WriteByte(c)
			i++
			if i < len(s) {
				b.WriteByte(s[i])
				i++
			}
			continue
		case '<':
			if d, n, ok := parseHeredoc(s[i:]); ok {
				b.WriteString(s[i : i+n])
				i += n
				heredocs = append(heredocs, d)
				continue
			}
		case ';', '&', '|':
			start = true
		case '\n':
			b.WriteByte(c)
			i++
			// A here-document body runs from the next line to its delimiter line, verbatim.
			for _, d := range heredocs {
				for i < len(s) {
					j := strings.IndexByte(s[i:], '\n')
					line := s[i:]
					if j >= 0 {
						line = s[i : i+j+1]
					}
					b.WriteString(line)
					i += len(line)
					body := strings.TrimSuffix(line, "\n")
					if d.dash {
						body = strings.TrimLeft(body, "\t")
					}
					if body == d.word {
						break
					}
				}
			}
			heredocs = nil
			start = true
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

var (
	// rtkPrefixRe matches the words rtk skips before the command it rewrites (measured with
	// `rtk rewrite`, rtk 1.2.0): an assignment with a plain, '…' or "…" value, "time [-p]", and a
	// bare command/builtin/exec/nohup/nice. "command -p", "exec -a" and nice's options are not
	// followed by the hook, so an option after them ends the match.
	//
	// A bare value may hold \x pairs and a plain $NAME; a "…" value any \x pair. Not ${…} or
	// $(…): a space inside them would be taken for the end of the value. An escaped space is read
	// as part of the value, although the hook ends the value there ("A=a\ ls" becomes
	// "A=a\ rtk ls"): following it would make "A=a\ ls" equal to "A=a\ rtk ls", a command that
	// runs ls and one that runs nothing.
	rtkPrefixRe = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*\+?=(?:'[^']*'|"(?:\\.|[^"\\])*"|(?:\\.|\$[A-Za-z0-9_?@#*!$-]|[^\s'"\\$` + "`" + `;&|<>()])*)|time(?:[ \t]+-p)?|command|builtin|exec|nohup|nice|do|then|else)[ \t]+`)
	// shellScriptRe opens the script of a bash/sh/zsh -c, by name or by path; the quote that
	// follows is captured. dash -c is not rewritten by the hook (measured).
	shellScriptRe = regexp.MustCompile(`^(?:[^\s'"\\$` + "`" + `;&|<>()]*/)?(?:bash|sh|zsh)[ \t]+-c[ \t]+(['"])`)
)

// rtkCommandPrefix is the length of the leading blanks and hook-skipped prefix words of a
// command; 0 when there are none.
func rtkCommandPrefix(s string) int {
	n := 0
	for {
		for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
			n++
		}
		m := rtkPrefixRe.FindStringIndex(s[n:])
		if m == nil || strings.HasPrefix(s[n+m[1]:], "-") {
			return n
		}
		n += m[1]
	}
}

// shellScript reports a bash/sh/zsh -c whose script is one quoted word: n is the length up to
// and including the opening quote, script the text inside, closed whether the closing quote was
// found. A double-quoted script containing $, ` or \ is not taken apart: what the shell would
// run is not the text.
func shellScript(s string) (n int, script string, closed, ok bool) {
	m := shellScriptRe.FindStringSubmatchIndex(s)
	if m == nil {
		return 0, "", false, false
	}
	n = m[1]
	q := s[m[2]]
	end := strings.IndexByte(s[n:], q)
	body := s[n:]
	if end >= 0 {
		body = s[n : n+end]
	}
	if q == '"' && strings.ContainsAny(body, "$`\\") {
		return 0, "", false, false
	}
	return n, body, end >= 0, true
}

type heredocDelim struct {
	word string
	dash bool
}

// parseHeredoc reads a "<<WORD" or "<<-WORD" operator (WORD bare or quoted) at the start of s.
// "<<<" is a here-string, not a here-document.
func parseHeredoc(s string) (d heredocDelim, n int, ok bool) {
	if !strings.HasPrefix(s, "<<") || strings.HasPrefix(s, "<<<") {
		return d, 0, false
	}
	n = 2
	if n < len(s) && s[n] == '-' {
		d.dash = true
		n++
	}
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	if n < len(s) && (s[n] == '\'' || s[n] == '"') {
		end := strings.IndexByte(s[n+1:], s[n])
		if end < 0 {
			return d, 0, false
		}
		d.word = s[n+1 : n+1+end]
		return d, n + end + 2, d.word != ""
	}
	w := n
	for n < len(s) && !strings.ContainsRune(" \t\n;&|<>()'\"", rune(s[n])) {
		n++
	}
	d.word = strings.ReplaceAll(s[w:n], `\`, "")
	return d, n, d.word != ""
}

// matchFullCommand reports whether the command region of the dialog can be the whole of command.
// Every boundary between two drawn lines is a wrap (nothing, or the swallowed run of blanks) only
// where the terminal could have broken the line there, and is otherwise a real newline; a boundary
// where a wrap is possible is also tried as a newline. Only an exact reconstruction matches, so
// the one thing that stays ambiguous is a screen drawn identically for two commands, which no
// reading of the screen can separate. The search is pruned by the command's own text.
//
// With tail set, region is only the end of the command (the dialog's top is clipped): its first
// line may start anywhere in the command, and the reconstruction must be a suffix of it.
func matchFullCommand(region []string, command string, width int, tail bool) bool {
	var lines []string
	for _, ln := range region {
		lines = append(lines, strings.TrimRight(strings.TrimPrefix(ln, " "), " \t"))
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return false
	}
	opts := make([][]string, len(lines)-1)
	for k := range opts {
		c, ok := cols(lines[k])
		if !ok {
			return false // unknown width with more lines below: cannot say how it was broken
		}
		switch {
		case width > 0 && c+1 >= width-1:
			opts[k] = append([]string{""}, wrapSeps(1)...)
		case width > 0:
			if n, ok := wordWrapRun(c, lines[k+1], width); ok {
				opts[k] = wrapSeps(n)
			} else {
				opts[k] = []string{"\n"}
			}
		default:
			opts[k] = []string{"\n"}
		}
	}
	want := stripRtk(normalizeCommand(command), false)
	if want == "" {
		return false
	}
	var try func(i int, shown string) bool
	try = func(i int, shown string) bool {
		if tail {
			if !strings.Contains(want, stripRtk(shown, true)) {
				return false
			}
			if i == len(lines) {
				return strings.HasSuffix(want, stripRtk(shown, false))
			}
		} else {
			if !strings.HasPrefix(want, stripRtk(shown, true)) {
				return false
			}
			if i == len(lines) {
				return stripRtk(strings.TrimSpace(shown), false) == want
			}
		}
		for _, sep := range opts[i-1] {
			if try(i+1, shown+sep+lines[i]) {
				return true
			}
		}
		return false
	}
	return try(1, lines[0])
}

// clippedMinLines is the fewest command lines a clipped dialog must show before its tail counts as
// evidence. A dialog only clips when it is taller than the pane, so a real one shows the pane's
// height less its bottom (about 40 of 54 lines on a phone pane). A few lines such as "  });",
// "EOF" and a closing command are shared by too many commands to tell P from Q; a pane too short
// to show this many is answered in the terminal.
const clippedMinLines = 8

// checkClippedCommand compares a Bash dialog whose top is above the pane with the whole command.
// Only the command's end is visible: the region from the pane's first line down to the dashed
// rule under the command must reconstruct to a suffix of command, with the same wrap rules as a
// full match. When the clip falls inside the title or description, both rules are on screen and
// the command between them is compared whole.
//
// Limit: nothing on screen is compared with the start of the command (the payload line is only
// checked against the payload's own command), so any command whose end reconstructs the visible
// tail matches, whatever its first lines. The prompt token still binds the answer to the payload.
// Refusing instead made every Bash prompt taller than the pane (a heredoc on a phone-width pane)
// unanswerable from the card.
func (m PermissionMenu) checkClippedCommand(command string) TargetCheck {
	var rules []int
	for i, ln := range m.Body {
		if permDashRuleRe.MatchString(ln) {
			rules = append(rules, i)
		}
	}
	if len(rules) == 0 || len(rules) > 2 {
		return TargetDiffers
	}
	// The dashed rule spans the pane, as the top rule does.
	width := len([]rune(strings.TrimSpace(m.Body[rules[0]])))
	if len(rules) == 2 {
		region, w := stripCommandBar(m.Body[rules[0]+1:rules[1]], width)
		if matchFullCommand(region, command, w, false) {
			return TargetMatches
		}
		return TargetDiffers
	}
	region, w := stripCommandBar(m.Body[:rules[0]], width)
	shown := 0
	for _, ln := range region {
		if strings.TrimSpace(ln) != "" {
			shown++
		}
	}
	if shown < clippedMinLines || !matchFullCommand(region, command, w, true) {
		return TargetDiffers
	}
	return TargetMatches
}
