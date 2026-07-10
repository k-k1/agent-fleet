package main

import "testing"

// TestToBrowseRel checks SendUserFile path normalization: absolute paths under the browse
// root become root-relative (forward-slashed), cwd-relative paths are anchored on the
// turn's cwd first, and anything outside the root (or a relative path with no cwd) is left
// untouched so it still lists in the panel even if it can't be opened.
func TestToBrowseRel(t *testing.T) {
	root := "/home/dev"
	cases := []struct {
		name, p, cwd, want string
	}{
		{"abs under root", "/home/dev/repos/x/report.md", "", "repos/x/report.md"},
		{"rel joined on cwd", "report.md", "/home/dev/repos/x", "repos/x/report.md"},
		{"rel dotted on cwd", "./out/a.png", "/home/dev/repos/x", "repos/x/out/a.png"},
		{"abs outside root", "/tmp/claude/scratch/a.png", "", "/tmp/claude/scratch/a.png"},
		{"rel no cwd", "report.md", "", "report.md"},
		{"cwd outside root", "a.png", "/tmp/work", "/tmp/work/a.png"},
	}
	for _, c := range cases {
		if got := toBrowseRel(c.p, c.cwd, root); got != c.want {
			t.Errorf("%s: toBrowseRel(%q,%q,%q) = %q, want %q", c.name, c.p, c.cwd, root, got, c.want)
		}
	}
}
