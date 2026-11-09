package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func menuFixture(t *testing.T, name string) PermissionMenu {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", name))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ParsePermissionMenu(string(b))
	if !ok {
		t.Fatalf("%s: menu not parsed", name)
	}
	return m
}

// TestCheckTarget pins the target comparison (#3359) on the four panes captured from claude
// 2.1.318 and three derived from their layout (bash_wrapped_command, bash_multiline_command,
// edit_relative_path: the same dialog frame with a wrapped command, a heredoc, and a path
// drawn relative to the session directory).
func TestCheckTarget(t *testing.T) {
	long := "find /home/dev/repos/demo/src -type f -name '*.go' -newer /home/dev/repos/demo/go.mod -exec grep -l 'permissionToken' {} + | sort | uniq"
	barCmd := "cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp /dev/null js/alpha-bravo-charlie-delta-echo-foxtrot-golf-hotel.txt; echo seen"
	cases := []struct {
		name, file, detail, cwd string
		want                    TargetCheck
	}{
		{"plain command", "bash_ask_rule_af_label.txt", "Bash · echo live-check-3357", "", TargetMatches},
		{"other command", "bash_ask_rule_af_label.txt", "Bash · rm -rf build", "", TargetDiffers},
		{"prefix of another command", "bash_ask_rule_af_label.txt", "Bash · echo live-check-33570", "", TargetDiffers},
		{"rewritten command still contains it", "bash_four_options.txt", `Bash · jira issue create -p ABC -t Task -s "テスト 日本語" --no-input || true`, "", TargetMatches},
		{"wrapped command", "bash_wrapped_command.txt", "Bash · " + long[:100] + "…", "", TargetMatches},
		{"wrapped, other command", "bash_wrapped_command.txt", "Bash · find /tmp -name x", "", TargetDiffers},
		{"word-wrapped on a narrow pane", "bash_word_wrapped_command.txt", "Bash · cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp js.txt queue.txt; : > seen.txt", "", TargetMatches},
		{"word-wrapped, other command", "bash_word_wrapped_command.txt", "Bash · cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp js.txt queue.txt", "", TargetDiffers},
		{"left bar, wrapped continuation", "bash_bar_dangerous_rm.txt", "Bash · " + string([]rune(barCmd)[:100]) + "…", "", TargetMatches},
		{"left bar, other command", "bash_bar_dangerous_rm.txt", "Bash · rm -rf js", "", TargetDiffers},
		{"multi-line: first line", "bash_multiline_command.txt", "Bash · cat > note.txt <<'EOF'", "", TargetMatches},
		{"multi-line: other", "bash_multiline_command.txt", "Bash · cat > other.txt <<'EOF'", "", TargetDiffers},
		{"new file, relative", "write_three_options.txt", "Write · /home/dev/repos/demo/note.txt", "/home/dev/repos/demo", TargetMatches},
		{"same name, other directory", "write_three_options.txt", "Write · /home/dev/repos/other/note.txt", "/home/dev/repos/demo", TargetDiffers},
		{"edit under a subdirectory", "edit_relative_path.txt", "Edit · /home/dev/repos/demo/src/util/note.txt", "/home/dev/repos/demo", TargetMatches},
		{"same basename, other subdirectory", "edit_relative_path.txt", "Edit · /home/dev/repos/demo/note.txt", "/home/dev/repos/demo", TargetDiffers},
		{"relative path without a cwd", "edit_relative_path.txt", "Edit · /home/dev/repos/demo/src/util/note.txt", "", TargetDiffers},
		// Nothing to compare: fail-open, the prompt token still binds the answer.
		{"bare tool", "bash_ask_rule_af_label.txt", "Bash", "", TargetUnchecked},
		{"notification prose", "bash_ask_rule_af_label.txt", "Claude needs your permission", "", TargetUnchecked},
		{"empty", "bash_ask_rule_af_label.txt", "", "", TargetUnchecked},
	}
	for _, c := range cases {
		if got := menuFixture(t, c.file).CheckTarget(c.detail, c.cwd); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A dialog whose body cannot be read refuses (fail-closed) when the payload names a target, and
// lets an untargeted payload through.
func TestCheckTargetUnreadableBody(t *testing.T) {
	m := menuFixture(t, "bash_ask_rule_af_label.txt")
	m.Body, m.BodyOK = nil, false
	if got := m.CheckTarget("Bash · echo live-check-3357", ""); got != TargetDiffers {
		t.Errorf("got %v, want TargetDiffers", got)
	}
	if got := m.CheckTarget("Bash", ""); got != TargetUnchecked {
		t.Errorf("got %v, want TargetUnchecked", got)
	}
}

// paneWith swaps text in a fixture and parses the result.
func paneWith(t *testing.T, name string, repl ...string) PermissionMenu {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", name))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ParsePermissionMenu(strings.NewReplacer(repl...).Replace(string(b)))
	if !ok {
		t.Fatalf("%s: menu not parsed after %q", name, repl)
	}
	return m
}

// Only the command (or path) region is evidence: the previous prompt's text sitting in the
// description, the diff, the Tip or another tool's dialog proves nothing.
func TestCheckTargetIgnoresEverythingButTheTarget(t *testing.T) {
	const p = "Bash · echo live-check-3357"
	// Q runs rm -rf build; its description mentions P's command.
	m := paneWith(t, "bash_ask_rule_af_label.txt", " echo live-check-3357\n╌", " rm -rf build\n╌", "Print live-check marker", "Describe echo live-check-3357")
	if got := m.CheckTarget(p, ""); got != TargetDiffers {
		t.Errorf("description only: %v", got)
	}
	// Same command text under another tool's title.
	m = paneWith(t, "bash_ask_rule_af_label.txt", "Bash command", "Fetch page")
	if got := m.CheckTarget(p, ""); got != TargetDiffers {
		t.Errorf("other tool: %v", got)
	}
	// Q edits other.txt; P's path appears in the diff.
	m = paneWith(t, "edit_relative_path.txt", " src/util/note.txt\n", " other.txt\n", " 1 -old line", " 1 -/home/dev/repos/demo/src/util/note.txt")
	if got := m.CheckTarget("Edit · /home/dev/repos/demo/src/util/note.txt", "/home/dev/repos/demo"); got != TargetDiffers {
		t.Errorf("path only in the diff: %v", got)
	}
	// Same path, but a Write dialog for an Edit payload.
	m = paneWith(t, "edit_relative_path.txt", "Edit file", "Create file")
	if got := m.CheckTarget("Edit · /home/dev/repos/demo/src/util/note.txt", "/home/dev/repos/demo"); got != TargetDiffers {
		t.Errorf("other tool, same path: %v", got)
	}
	// A tool the dialog titles do not cover is refused rather than guessed at.
	if got := menuFixture(t, "bash_ask_rule_af_label.txt").CheckTarget("WebFetch · echo live-check-3357", ""); got != TargetDiffers {
		t.Errorf("unknown tool: %v", got)
	}
}

// Whitespace inside an argument or a file name is part of it.
func TestCheckTargetKeepsWhitespaceInArguments(t *testing.T) {
	m := paneWith(t, "bash_ask_rule_af_label.txt", "echo live-check-3357", "rm 'oldfile'")
	if got := m.CheckTarget("Bash · rm 'old file'", ""); got != TargetDiffers {
		t.Errorf("quoted space dropped: %v", got)
	}
	if got := m.CheckTarget("Bash · rm 'oldfile'", ""); got != TargetMatches {
		t.Errorf("control: %v", got)
	}
	m = paneWith(t, "edit_relative_path.txt", "src/util/note.txt", "/home/dev/repos/demo/ab.txt")
	if got := m.CheckTarget("Edit · /home/dev/repos/demo/a b.txt", "/home/dev/repos/demo"); got != TargetDiffers {
		t.Errorf("spaced file name: %v", got)
	}
	// A short first line never swallows the next one.
	m = paneWith(t, "bash_multiline_command.txt")
	if got := m.CheckTarget("Bash · cat > note.txt <<'EOFhello", ""); got != TargetDiffers {
		t.Errorf("joined short lines: %v", got)
	}
}

// A real newline after a line that does not reach the pane's edge is never joined: P's one-line
// `echo <40 a> rm -rf build` must not match Q's two-line command that deletes (review round 2).
func TestCheckTargetDoesNotJoinHardNewlines(t *testing.T) {
	a := strings.Repeat("a", 40)
	m := paneWith(t, "bash_ask_rule_af_label.txt", " echo live-check-3357\n", " echo "+a+"\n rm -rf build\n")
	for _, sep := range []string{" ", ""} {
		if got := m.CheckTarget("Bash · echo "+a+sep+"rm -rf build", ""); got != TargetDiffers {
			t.Errorf("sep %q: %v", sep, got)
		}
	}
	if got := m.CheckTarget("Bash · echo "+a, ""); got != TargetMatches {
		t.Errorf("first line alone: %v", got)
	}
}

// A line that fills the pane continues onto the next, and only the completed logical line is
// compared: P's text as Q's first physical line proves nothing about the rest (review round 3).
func TestCheckTargetComparesTheWholeWrappedLine(t *testing.T) {
	first := "echo " + strings.Repeat("a", 73) // 78 columns: the pane edge with the indent
	m := paneWith(t, "bash_ask_rule_af_label.txt", " echo live-check-3357\n", " "+first+"\n && rm -rf build\n")
	if got := m.CheckTarget("Bash · "+first, ""); got != TargetDiffers {
		t.Errorf("arbitrary suffix: %v", got)
	}
	// Control: the same wrap, completed as P says.
	if got := m.CheckTarget("Bash · "+first+" && rm -rf build", ""); got != TargetMatches {
		t.Errorf("complete wrapped command: %v", got)
	}
}

// Zero-width marks make a line look wider than it is; such a line never counts as pane-wide.
func TestCheckTargetDoesNotTrustCombiningWidth(t *testing.T) {
	first := "echo " + strings.Repeat("a\u0301", 37) // 42 columns, 79 runes
	m := paneWith(t, "bash_ask_rule_af_label.txt", " echo live-check-3357\n", " "+first+"\n rm -rf build\n")
	for _, sep := range []string{" ", ""} {
		if got := m.CheckTarget("Bash · "+first+sep+"rm -rf build", ""); got != TargetDiffers {
			t.Errorf("sep %q: %v", sep, got)
		}
	}
}

// Unknown width plus a line below is refused outright: stopping at the first physical line
// would leave the continuation unchecked (review round 4). Single-line combining commands match.
func TestCheckTargetRefusesUnknownWidthWithContinuation(t *testing.T) {
	first := "echo " + strings.Repeat("a", 73) + "\u0301"
	m := paneWith(t, "bash_ask_rule_af_label.txt", " echo live-check-3357\n", " "+first+"\n && rm -rf build\n")
	if got := m.CheckTarget("Bash · "+first, ""); got != TargetDiffers {
		t.Errorf("combining wrapped suffix: %v", got)
	}
	m = paneWith(t, "bash_ask_rule_af_label.txt", "echo live-check-3357", first)
	if got := m.CheckTarget("Bash · "+first, ""); got != TargetMatches {
		t.Errorf("single-line combining command: %v", got)
	}
}

// A blank next line (ASCII or ideographic space) must neither panic nor count as a word wrap.
func TestWordWrappedBlankNext(t *testing.T) {
	for _, next := range []string{"", " ", "\u3000", "\u3000x"} {
		if wordWrapped(10, next, 12) {
			t.Errorf("wordWrapped(%q) = true, want false", next)
		}
	}
}

// One command may mix a word wrap (space swallowed) and a hard wrap of a long word (no space).
func TestMatchLogicalLineMixedWrap(t *testing.T) {
	lines := []string{" echo abcdefghijk", " abcdefghijklmnopqr", " st"}
	target := "echo abcdefghijk abcdefghijklmnopqrst"
	eq := func(s string) bool { return s == target }
	if got := matchLogicalLine(lines, target, false, 20, eq); got != TargetMatches {
		t.Errorf("mixed wrap: got %v, want TargetMatches", got)
	}
	other := "echo abcdefghijk abcdefghijklmnopqrsx"
	if got := matchLogicalLine(lines, other, false, 20, func(s string) bool { return s == other }); got != TargetDiffers {
		t.Errorf("mixed wrap, other target: got %v, want TargetDiffers", got)
	}
}

// Word-wrap boundaries have one separator, so a long wrapped command is not refused for length.
func TestMatchLogicalLineManyWordWraps(t *testing.T) {
	lines := []string{" echo abcdefghijk"}
	target := "echo abcdefghijk"
	for i := 0; i < 14; i++ {
		lines = append(lines, " abcdefghijkl")
		target += " abcdefghijkl"
	}
	if got := matchLogicalLine(lines, target, false, 20, func(s string) bool { return s == target }); got != TargetMatches {
		t.Errorf("14 word-wrapped lines: got %v, want TargetMatches", got)
	}
}

// #3521: the payload keeps only the first line, so a Q that differs from P by a line break (or
// by lines after the first) used to match. The whole command is compared now.
func TestCheckTargetCommandIsNewlineAware(t *testing.T) {
	// P is `echo hi`; Q is `echo hi` + newline + `rm -rf build`. The one-line payload cannot see
	// the second line, so the old check accepted Q for P.
	q := paneWith(t, "bash_ask_rule_af_label.txt", " echo live-check-3357\n", " echo hi\n rm -rf build\n")
	if got := q.CheckTarget("Bash · echo hi", ""); got != TargetMatches {
		t.Fatalf("fixture no longer exercises the old gap: %v", got)
	}
	if got := q.CheckTargetCommand("Bash · echo hi", "", "echo hi"); got != TargetDiffers {
		t.Errorf("Q accepted for P: %v", got)
	}
	if got := q.CheckTargetCommand("Bash · echo hi", "", "echo hi\nrm -rf build"); got != TargetMatches {
		t.Errorf("control, Q for Q: %v", got)
	}
	if got := q.CheckTargetCommand("Bash · echo hi", "", "echo hi rm -rf build"); got != TargetDiffers {
		t.Errorf("newline read as a space: %v", got)
	}
	// Lines after the first are part of the identity.
	multi := menuFixture(t, "bash_multiline_command.txt")
	full := "cat > note.txt <<'EOF'\nhello\nEOF\nls -l note.txt"
	if got := multi.CheckTargetCommand("Bash · cat > note.txt <<'EOF'", "", full); got != TargetMatches {
		t.Errorf("multi-line P: %v", got)
	}
	if got := multi.CheckTargetCommand("Bash · cat > note.txt <<'EOF'", "", "cat > note.txt <<'EOF'"); got != TargetDiffers {
		t.Errorf("Q has extra lines: %v", got)
	}
	if got := multi.CheckTargetCommand("Bash · cat > note.txt <<'EOF'", "", "cat > note.txt <<'EOF' hello EOF ls -l note.txt"); got != TargetDiffers {
		t.Errorf("newlines joined: %v", got)
	}
	// No command beside a Bash payload: refuse.
	if got := multi.CheckTargetCommand("Bash · cat > note.txt <<'EOF'", "", ""); got != TargetDiffers {
		t.Errorf("no command: %v", got)
	}
}

// Word-wrapped, hard-wrapped, trailing-space and rtk-prefixed commands still match.
func TestCheckTargetCommandKeepsMatchingWraps(t *testing.T) {
	long := "find /home/dev/repos/demo/src -type f -name '*.go' -newer /home/dev/repos/demo/go.mod -exec grep -l 'permissionToken' {} + | sort | uniq"
	if got := menuFixture(t, "bash_wrapped_command.txt").CheckTargetCommand("Bash · "+long[:100]+"…", "", long); got != TargetMatches {
		t.Errorf("hard wrap: %v", got)
	}
	ww := "cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp js.txt queue.txt; : > seen.txt\nwhile [ -s queue.txt ]; do a=$(head -1 queue.txt); done"
	m := menuFixture(t, "bash_word_wrapped_command.txt")
	if got := m.CheckTargetCommand("Bash · cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp js.txt queue.txt; : > seen.txt", "", ww); got != TargetMatches {
		t.Errorf("word wrap: %v", got)
	}
	if got := m.CheckTargetCommand("Bash · cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp js.txt queue.txt; : > seen.txt", "", ww+"   \n"); got != TargetMatches {
		t.Errorf("trailing blanks: %v", got)
	}
	if got := m.CheckTargetCommand("Bash · cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp js.txt queue.txt; : > seen.txt", "", ww+"\nrm -rf /"); got != TargetDiffers {
		t.Errorf("extra line: %v", got)
	}
	rtk := menuFixture(t, "bash_four_options.txt")
	cmd := `jira issue create -p ABC -t Task -s "テスト 日本語" --no-input || true`
	if got := rtk.CheckTargetCommand("Bash · "+cmd, "", cmd); got != TargetMatches {
		t.Errorf("rtk prefix: %v", got)
	}
	// Other tools are unaffected by the command.
	if got := menuFixture(t, "write_three_options.txt").CheckTargetCommand("Write · /home/dev/repos/demo/note.txt", "/home/dev/repos/demo", ""); got != TargetMatches {
		t.Errorf("file tool: %v", got)
	}
}

// A real newline after a pane-wide line is told apart from the wrap by the command's text.
func TestCheckTargetCommandNewlineAtPaneEdge(t *testing.T) {
	first := "echo " + strings.Repeat("a", 73) // 78 columns: the pane edge with the indent
	m := paneWith(t, "bash_ask_rule_af_label.txt", " echo live-check-3357\n", " "+first+"\n rm -rf build\n")
	for _, c := range []struct {
		cmd  string
		want TargetCheck
	}{
		{first + "\nrm -rf build", TargetMatches},
		{first + " rm -rf build", TargetMatches},
		{first + "rm -rf build", TargetMatches},
		{first + "\n\nrm -rf build", TargetDiffers},
		{first + "\nrm -rf /", TargetDiffers},
	} {
		if got := m.CheckTargetCommand("Bash · "+first, "", c.cmd); got != c.want {
			t.Errorf("%q: %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestCheckTargetCommandRefusesBareBash(t *testing.T) {
	m := menuFixture(t, "bash_ask_rule_af_label.txt")
	for _, c := range [][2]string{{"Bash", ""}, {"Bash", "  "}, {"Bash · ", ""}, {"Bash", "echo live-check-3357"}} {
		if got := m.CheckTargetCommand(c[0], "", c[1]); got != TargetDiffers {
			t.Errorf("%q %q: %v", c[0], c[1], got)
		}
	}
	// Other tools stay untargeted-friendly.
	if got := m.CheckTargetCommand("Claude needs your permission", "", ""); got != TargetUnchecked {
		t.Errorf("prose: %v", got)
	}
}

// A Bash dialog taller than the pane (a heredoc on a phone-width pane) has its top rule, title
// and first command lines drawn nowhere: Claude Code uses the alternate screen, so they are not
// in tmux's history either. Captured live from claude 2.1.319 on a 67x54 pane.
func TestCheckTargetCommandClippedDialog(t *testing.T) {
	m := menuFixture(t, "bash_clipped_tall_command.txt")
	if m.BodyOK || !m.Clipped {
		t.Fatalf("BodyOK=%v Clipped=%v, want a clipped body", m.BodyOK, m.Clipped)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", "bash_clipped_tall_command.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	full := string(b)
	first, _, _ := strings.Cut(full, "\n")
	detail := "Bash · " + string([]rune(first)[:100]) + "…"
	if got := m.CheckTargetCommand(detail, "", full); got != TargetMatches {
		t.Errorf("same command: got %v, want TargetMatches", got)
	}
	// A different tail, the same tail with a line changed, and a payload from another command.
	if got := m.CheckTargetCommand(detail, "", full+"\nrm -rf build"); got != TargetDiffers {
		t.Errorf("extra last line: got %v, want TargetDiffers", got)
	}
	changed := strings.Replace(full, `toContain("nodata")`, `toContain("data")`, 1)
	if got := m.CheckTargetCommand(detail, "", changed); got != TargetDiffers {
		t.Errorf("changed visible line: got %v, want TargetDiffers", got)
	}
	if got := m.CheckTargetCommand("Bash · ls", "", full); got != TargetDiffers {
		t.Errorf("payload of another command: got %v, want TargetDiffers", got)
	}
	// The one-line check has nothing to compare its target with and stays closed.
	if got := m.CheckTarget(detail, ""); got != TargetDiffers {
		t.Errorf("CheckTarget: got %v, want TargetDiffers", got)
	}
	// The visible tail must hold clippedMinLines command lines: cut the body from the top until
	// one line fewer is left, and that tail, though a true suffix, is refused.
	seen := 0
	for k := 0; k < len(m.Body)-7; k++ {
		short := m
		short.Body = m.Body[k:]
		n := 0
		for _, ln := range short.Body[:len(short.Body)-7] { // the last 7 lines are below the command
			if s := strings.TrimSpace(ln); s != "" && s != "│" {
				n++
			}
		}
		got := short.CheckTargetCommand(detail, "", full)
		if n == clippedMinLines {
			seen++
			if got != TargetMatches {
				t.Errorf("%d-line tail: got %v, want TargetMatches", n, got)
			}
		}
		if n == clippedMinLines-1 {
			seen++
			if got != TargetDiffers {
				t.Errorf("%d-line tail: got %v, want TargetDiffers", n, got)
			}
			break
		}
	}
	if seen < 2 {
		t.Fatalf("the cut never reached %d and %d visible lines", clippedMinLines, clippedMinLines-1)
	}
	// No dashed rule under the command, or three: not the measured layout.
	noRule := m
	noRule.Body = nil
	for _, ln := range m.Body {
		if !permDashRuleRe.MatchString(ln) {
			noRule.Body = append(noRule.Body, ln)
		}
	}
	if got := noRule.CheckTargetCommand(detail, "", full); got != TargetDiffers {
		t.Errorf("no dashed rule: got %v, want TargetDiffers", got)
	}
	rule := strings.Repeat("╌", 67)
	// The two extra rules go below the real one, so the region above the first rule is still the
	// matching tail: only the rule count refuses it.
	threeRules := m
	threeRules.Body = nil
	for _, ln := range m.Body {
		threeRules.Body = append(threeRules.Body, ln)
		if permDashRuleRe.MatchString(ln) {
			threeRules.Body = append(threeRules.Body, "x", rule, "y", rule)
		}
	}
	if got := threeRules.CheckTargetCommand(detail, "", full); got != TargetDiffers {
		t.Errorf("three dashed rules: got %v, want TargetDiffers", got)
	}
	// A clipped body never answers for a file tool, even with a command that matches the tail and a
	// target that is the command's own first line: only the tool check refuses the last case.
	for _, c := range []struct{ detail, command string }{
		{"Edit · /home/dev/repos/demo/src/ui/MonthlyTrend.dom.test.tsx", ""},
		{"Edit · /home/dev/repos/demo/src/ui/MonthlyTrend.dom.test.tsx", full},
		{"Edit · " + strings.TrimPrefix(detail, "Bash · "), full},
	} {
		if got := m.CheckTargetCommand(c.detail, "/home/dev/repos/demo", c.command); got != TargetDiffers {
			t.Errorf("%q on a clipped Bash dialog: got %v, want TargetDiffers", c.detail, got)
		}
	}
}

// A clip that falls inside the title or description leaves both dashed rules on screen; the
// command between them is then compared whole, as in an unclipped dialog.
func TestCheckTargetCommandClippedAboveCommand(t *testing.T) {
	m := menuFixture(t, "bash_multiline_command.txt")
	if !m.BodyOK {
		t.Fatal("fixture should parse with its top rule")
	}
	// Drop everything above the description, as a pane too short for the title would.
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", "bash_multiline_command.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "Create the file and list it" {
			start = i
		}
	}
	if start < 0 {
		t.Fatal("fixture lacks its description line")
	}
	c, ok := ParsePermissionMenu(strings.Join(lines[start:], "\n"))
	if !ok || c.BodyOK || !c.Clipped {
		t.Fatalf("ok=%v BodyOK=%v Clipped=%v, want a clipped body", ok, c.BodyOK, c.Clipped)
	}
	full := "cat > note.txt <<'EOF'\nhello\nEOF\nls -l note.txt"
	detail := "Bash · cat > note.txt <<'EOF'"
	if got := c.CheckTargetCommand(detail, "", full); got != TargetMatches {
		t.Errorf("same command: got %v, want TargetMatches", got)
	}
	// With the whole command on screen, a longer command whose end is that whole screen is refused:
	// a suffix comparison (the clipped-tail rule) would accept it, so this pins the whole one.
	if got := c.CheckTargetCommand(detail, "", "cat > note.txt <<'EOF'\n"+full); got != TargetDiffers {
		t.Errorf("command with an extra line: got %v, want TargetDiffers", got)
	}
}

// A barred command with an empty line, in a dialog whose top is on screen: tmux trims the blank
// after the bar, and the bare "│" still reads as the command's empty line.
func TestCheckTargetCommandBareBarLine(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", "bash_bar_dangerous_rm.txt"))
	if err != nil {
		t.Fatal(err)
	}
	pane := strings.Replace(string(b), " │ seen\n", " │ seen\n │\n", 1)
	m, ok := ParsePermissionMenu(pane)
	if !ok || !m.BodyOK {
		t.Fatalf("ok=%v BodyOK=%v", ok, m.BodyOK)
	}
	cmd := "cd $AF_WORK_DIR; mkdir -p js && rm -f js/*; cp /dev/null js/alpha-bravo-charlie-delta-echo-foxtrot-golf-hotel.txt; echo seen\n\nls $AF_WORK_DIR/js"
	// The fixture's last line is "rtk ls …", a hook rewrite inside the command. Compare the
	// reconstruction directly so this pins the bare bar line alone.
	region := []string{}
	in := false
	for _, ln := range m.Body {
		if permDashRuleRe.MatchString(ln) {
			if in {
				break
			}
			in = true
			continue
		}
		if in {
			region = append(region, ln)
		}
	}
	stripped, w := stripCommandBar(region, m.RuleWidth)
	if w != m.RuleWidth-2 {
		t.Fatalf("bar not stripped: width %d of %d", w, m.RuleWidth)
	}
	want := strings.Replace(cmd, "ls $AF", "rtk ls $AF", 1)
	if !matchFullCommand(stripped, want, w, false) {
		t.Errorf("command with an empty barred line did not reconstruct: %q", stripped)
	}
}

// A wrap swallows the whole run of blanks at the break, not one space: the command line
// " │     expect(…classN" is drawn as " │" over "expect(…classN" (captured live from claude
// 2.1.319 on a 67x54 pane; the dialog is clipped too). A run too short to have pushed the word
// to the next line is refused; a longer one draws the same screen and cannot be told apart.
func TestCheckTargetCommandSwallowedIndent(t *testing.T) {
	m := menuFixture(t, "bash_clipped_swallowed_indent.txt")
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", "bash_clipped_swallowed_indent.cmd"))
	if err != nil {
		t.Fatal(err)
	}
	full := string(b)
	first, _, _ := strings.Cut(full, "\n")
	detail := "Bash · " + first
	if got := m.CheckTargetCommand(detail, "", full); got != TargetMatches {
		t.Errorf("same command: got %v, want TargetMatches", got)
	}
	const shown = ` │     expect(host.querySelector`
	for _, indent := range []string{` │expect(host.querySelector`, ` │ expect(host.querySelector`, ` │  expect(host.querySelector`} {
		other := strings.Replace(full, shown, indent, 1)
		if other == full {
			t.Fatal("fixture command lacks the indented line")
		}
		if got := m.CheckTargetCommand(detail, "", other); got != TargetDiffers {
			t.Errorf("indent %q: got %v, want TargetDiffers", indent, got)
		}
	}
	// Three blanks are the fewest with which the line and the 59-column word fill the 64 columns
	// that wordWrapRun counts as the edge (2+3+59); this pins that edge.
	if got := m.CheckTargetCommand(detail, "", strings.Replace(full, shown, ` │   expect(host.querySelector`, 1)); got != TargetMatches {
		t.Errorf("indent of 3: got %v, want TargetMatches", got)
	}
}

// The rtk hook rewrites any simple command it supports, not only the first one, and the dialog
// draws the rewritten command (measured on 2.1.319, #3725). An Allow on such a dialog was refused
// as target_mismatch.
func TestCheckTargetCommandRtkInsideCommand(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", "bash_rtk_inner_rewrite.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := ParsePermissionMenu(string(b))
	if !ok || !m.BodyOK {
		t.Fatalf("ok=%v BodyOK=%v", ok, m.BodyOK)
	}
	cmd := `cd $AF_WORK_DIR/probe; sed -i 's/width: 4/width: 5/' a.css; sleep 1; cd $AF_WORK_DIR/probe; rm -f old/*; ` +
		`for x in one two three four five six seven eight; do printf '%s\n' ` +
		`"$x-padding-to-make-this-command-long-enough-to-wrap-across-several-lines-of-the-terminal" > /dev/null; done; ls`
	detail := "Bash · " + string([]rune(cmd)[:100]) + "…"
	if got := m.CheckTargetCommand(detail, "", cmd); got != TargetMatches {
		t.Errorf("the command the hook saw: got %v, want TargetMatches", got)
	}
	for _, other := range []string{
		strings.TrimSuffix(cmd, "; ls") + "; ls -a",
		strings.TrimSuffix(cmd, "; ls") + "; rm ls",
		strings.Replace(cmd, "rm -f old/*", "rm -rf old/*", 1),
	} {
		d := "Bash · " + string([]rune(other)[:100]) + "…"
		if got := m.CheckTargetCommand(d, "", other); got != TargetDiffers {
			t.Errorf("%q: got %v, want TargetDiffers", other[len(other)-12:], got)
		}
	}
}

// TestStripRtk pins stripRtk to what `rtk rewrite` (rtk 1.2.0) actually rewrites: each "in" is
// the hook's output for "want", or text the hook leaves alone (in == want).
func TestStripRtk(t *testing.T) {
	cases := []struct{ in, want string }{
		{"rtk ls", "ls"},
		{"cd x; rtk ls", "cd x; ls"},
		{"a && rtk git status | rtk grep x", "a && git status | grep x"},
		{"a || rtk ls", "a || ls"},
		{"for f in *; do rtk cat $f; done", "for f in *; do cat $f; done"},
		{"if x; then rtk ls; else rtk ls; fi", "if x; then ls; else ls; fi"},
		{"echo hi\nrtk ls", "echo hi\nls"},
		// Prefixes the hook skips, chained.
		{"time rtk ls", "time ls"},
		{"time -p rtk ls", "time -p ls"},
		{"VAR=1 VAR2=2 rtk ls", "VAR=1 VAR2=2 ls"},
		{`VAR="a b" rtk ls`, `VAR="a b" ls`},
		{"VAR=1 time rtk ls", "VAR=1 time ls"},
		{"nohup nice rtk ls", "nohup nice ls"},
		{"command rtk ls", "command ls"},
		{"A=$B rtk ls", "A=$B ls"},
		{`A="x$y" rtk ls`, `A="x$y" ls`},
		{`A="a\"b" rtk ls`, `A="a\"b" ls`},
		{`A=a\b rtk ls`, `A=a\b ls`},
		// The script of bash -c / sh -c.
		{"bash -c 'rtk ls; rtk ls'", "bash -c 'ls; ls'"},
		{`sh -c "rtk ls; rtk ls"`, `sh -c "ls; ls"`},
		{"/bin/bash -c 'rtk ls'; rtk ls", "/bin/bash -c 'ls'; ls"},
		{"zsh -c 'rtk ls'", "zsh -c 'ls'"},
		// Prefixes the hook follows and this does not (it refuses Allow there, never matches
		// wrongly), and a value it splits differently from the shell.
		{"nice -n 5 rtk ls", "nice -n 5 rtk ls"},
		{"env VAR=1 rtk ls", "env VAR=1 rtk ls"},
		{"timeout 5 rtk ls", "timeout 5 rtk ls"},
		{"A=${x:-a rtk b} ls", "A=${x:-a rtk b} ls"},
		{`A=a\ rtk ls`, `A=a\ rtk ls`},
		{"dash -c 'rtk ls'", "dash -c 'rtk ls'"},
		{"mybash -c 'rtk ls'", "mybash -c 'rtk ls'"},
		// Left alone by the hook, so an "rtk " there is data and must stay.
		{"echo rtk ls", "echo rtk ls"},
		{"echo 'a; rtk b'", "echo 'a; rtk b'"},
		{`echo "a; rtk b"`, `echo "a; rtk b"`},
		{`echo $'a\'b; rtk c'`, `echo $'a\'b; rtk c'`},
		{"(rtk ls)", "(rtk ls)"},
		{"{ rtk ls; }", "{ rtk ls; }"},
		{"echo $((rtk + 1))", "echo $((rtk + 1))"},
		{"! rtk ls", "! rtk ls"},
		{"command -p rtk ls", "command -p rtk ls"},
		{"bash -x -c 'rtk ls'", "bash -x -c 'rtk ls'"},
		{`bash -c "rtk $x; rtk ls"`, `bash -c "rtk $x; rtk ls"`},
		{"cmd 2>&1 rtk x", "cmd 2>&1 rtk x"},
		{`echo a\; rtk b`, `echo a\; rtk b`},
		{"cat <<'EOF'\nrtk ls\nEOF\nrtk ls", "cat <<'EOF'\nrtk ls\nEOF\nls"},
		{"cat <<-EOF\n\trtk ls\n\tEOF\nrtk ls", "cat <<-EOF\n\trtk ls\n\tEOF\nls"},
		{"rtk rtk ls", "rtk ls"},
		{"rtk\tls", "rtk\tls"},
		{"ls; rtk", "ls; rtk"},
	}
	for _, c := range cases {
		if got := stripRtk(c.in, false); got != c.want {
			t.Errorf("stripRtk(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for in, want := range map[string]string{
		"cd x; r":            "cd x; ",
		"cd x; rt":           "cd x; ",
		"cd x; rtk":          "cd x; ",
		"bash -c 'rtk ls; r": "bash -c 'ls; ",
	} {
		if got := stripRtk(in, true); got != want {
			t.Errorf("partial stripRtk(%q) = %q, want %q", in, got, want)
		}
	}
	if got := stripRtk("cd x; rt", false); got != "cd x; rt" {
		t.Errorf("not partial: %q", got)
	}
}

// The comparison as a whole: an rtk inside quotes or a here-document is data, so a command
// that differs from the dialog only there never matches; an "rtk " wrapped across two lines
// still reconstructs; and a clipped dialog's tail is compared the same way.
func TestMatchFullCommandRtk(t *testing.T) {
	if matchFullCommand([]string{"echo 'a; rtk b'"}, "echo 'a; b'", 0, false) {
		t.Error("an rtk inside quotes was dropped")
	}
	if matchFullCommand([]string{"cat >s <<'EOF'", "rtk ls", "EOF"}, "cat >s <<'EOF'\nls\nEOF", 0, false) {
		t.Error("an rtk inside a here-document was dropped")
	}
	// "…; rt" fills the 10-column pane and "k ls" continues it with no separator.
	if !matchFullCommand([]string{"cd ab; rt", "k ls"}, "cd ab; ls", 10, false) {
		t.Error("an rtk wrapped across lines did not reconstruct")
	}
	if !matchFullCommand([]string{"cd ab; rtk ls"}, "x=1; cd ab; ls", 0, true) {
		t.Error("a clipped tail with an rtk rewrite did not match")
	}
	if matchFullCommand([]string{"cd ab; rtk ls -a"}, "x=1; cd ab; ls", 0, true) {
		t.Error("a clipped tail of another command matched")
	}
}
