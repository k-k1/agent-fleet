package claude

import (
	"os"
	"path/filepath"
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
