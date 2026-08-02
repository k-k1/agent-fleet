package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

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

func TestGenericMutableTail(t *testing.T) {
	all := []transcript.Turn{
		{Role: "user", Idx: 0, Text: "調べて"},
		{Role: "assistant", Idx: 1, Text: "最終回答"},
	}

	got := genericMutableTail(all, len(all))
	if len(got) != 1 || got[0].Idx != 1 || got[0].Text != "最終回答" {
		t.Fatalf("mutable tail = %+v, want the completed assistant turn", got)
	}
	if got := genericMutableTail(all, 1); got != nil {
		t.Fatalf("behind cursor tail = %+v, want nil", got)
	}
	if got := genericMutableTail(all[:1], 1); got != nil {
		t.Fatalf("user tail = %+v, want nil", got)
	}
}
