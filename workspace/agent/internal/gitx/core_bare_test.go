package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coreBare answers known only when the config alone settles core.bare: a parent is named from
// it, so a value it cannot read with certainty must leave the parent out.
func TestCoreBare(t *testing.T) {
	for _, c := range []struct {
		name, config string
		bare, known  bool
	}{
		{"non-bare", "[core]\n\trepositoryformatversion = 0\n\tbare = false\n", false, true},
		{"bare", "[core]\n\tbare = true\n", true, true},
		{"absent", "[core]\n\tfilemode = true\n", false, true},
		{"case and spacing", "[CORE]\n  Bare=Yes\n", true, true},
		{"key with no value", "[core]\n\tbare\n", true, true},
		{"last wins", "[core]\n\tbare = true\n[core]\n\tbare = off\n", false, true},
		{"other section", "[remote \"origin\"]\n\tbare = true\n", false, true},
		{"include", "[include]\n\tpath = other\n[core]\n\tbare = false\n", false, false},
		{"includeIf", "[includeIf \"gitdir:/x/\"]\n\tpath = other\n", false, false},
		{"unparseable value", "[core]\n\tbare = maybe\n", false, false},
		{"too large", "[core]\n\tbare = false\n#" + strings.Repeat("x", gitConfigMax) + "\n", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config"), []byte(c.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if bare, known := coreBare(dir); bare != c.bare || known != c.known {
				t.Errorf("coreBare = (%v, %v), want (%v, %v)", bare, known, c.bare, c.known)
			}
		})
	}
	if _, known := coreBare(t.TempDir()); known {
		t.Error("a missing config was taken as known")
	}
}
