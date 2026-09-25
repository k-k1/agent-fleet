package sessionx

// Issue #950: a meta read earlier and written back whole rolls back whatever another writer
// changed in between — the deletion lock among them, after which the next delete goes through.
// Every such write goes through UpdateSessionMeta instead, which re-reads under sessionLockMu
// and lets the caller set only the fields it owns. Plain session.WriteMeta is left to two kinds
// of site: locks.go (which owns the mutex and writes only under it) and the first write of a
// name nobody else can know yet (create, fork, recreate's successor).
//
// A new call site has no reason to know that rule and compiles either way, so this test freezes
// where plain WriteMeta may appear — the same device meta_remove_sites_test.go uses for
// RemoveMeta. A site added anywhere else turns the suite red instead of waiting for a reviewer.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var writeMetaCaller = regexp.MustCompile(`(?:^|[^.\w])(?:session\.)?WriteMeta\(`)

// wantWriteMetaSites is the frozen count per file (module-relative). Raise a count ONLY for a
// site that is under sessionLockMu in locks.go or writes a brand-new name; anything that read
// the meta first belongs on UpdateSessionMeta.
var wantWriteMetaSites = map[string]int{
	"internal/sessionx/locks.go":            5, // the lock and keep-awake handlers, UpdateSessionMeta, settleInitialPrompt, RecoverPendingInitialPrompts — all under sessionLockMu
	"internal/sessionx/session_spawn.go":    2, // spawnSlot.publish: the create's first write of its new name
	"internal/sessionx/session_handlers.go": 4, // fork (managed + tui) and recreate's successor (managed + tui): new names
}

func TestPlainWriteMetaSitesAreFrozen(t *testing.T) {
	root := writeSitesModuleRoot(t)
	got := map[string][]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, ln := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(ln)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "func WriteMeta(") {
				continue
			}
			if writeMetaCaller.MatchString(ln) {
				got[rel] = append(got[rel], trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]bool{}
	for f := range got {
		files[f] = true
	}
	for f := range wantWriteMetaSites {
		files[f] = true
	}
	var names []string
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		if n, want := len(got[f]), wantWriteMetaSites[f]; n != want {
			t.Errorf("%s: %d plain session.WriteMeta call(s), want %d:\n  %s\n"+
				"A write that starts from a meta read earlier must go through UpdateSessionMeta "+
				"(issue #950). Update wantWriteMetaSites only for a first write of a new name or a "+
				"write under sessionLockMu in locks.go.",
				f, n, want, strings.Join(got[f], "\n  "))
		}
	}
}

// writeSitesModuleRoot walks up to workspace/agent (the directory holding go.mod), as
// meta_remove_sites_test.go does in package session.
func writeSitesModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found above the test's working directory")
	return ""
}
