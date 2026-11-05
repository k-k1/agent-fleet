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
