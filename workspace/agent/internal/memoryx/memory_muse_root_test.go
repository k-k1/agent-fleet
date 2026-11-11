package memoryx

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// museMemoryDir seeds muse's personal-project and personal stores plus the files that must stay
// out of the history, and returns the stores' root.
func museMemoryDir(t *testing.T, home string) string {
	t.Helper()
	mem := filepath.Join(home, ".local", "share", "muse", "memory")
	memoryWrite(t, filepath.Join(mem, "personal", "MEMORY.md"), "- [p](p.md)\n")
	memoryWrite(t, filepath.Join(mem, "personal", "p.md"), "personal v1\n")
	memoryWrite(t, filepath.Join(mem, "projects", "demo-0123456789abcdef", "n.md"), "project v1\n")
	memoryWrite(t, filepath.Join(mem, "projects", "demo-0123456789abcdef", "sub", "m.md"), "nested v1\n")
	memoryWrite(t, filepath.Join(mem, "personal", ".muse-memory.lock"), "pid 1\n")
	memoryWrite(t, filepath.Join(mem, "personal", "notes.txt"), "not a note\n")
	return mem
}

func TestMemoryMuseRootAllowlist(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	museMemoryDir(t, home)
	root, ok := memoryRootByKind("muse")
	if !ok {
		t.Fatal("muse root is not active although its store exists")
	}
	for rel, want := range map[string]bool{
		"personal/MEMORY.md":                  true,
		"personal/p.md":                       true,
		"projects/demo-0123456789abcdef/n.md": true,
		"projects/x/sub/deep/m.md":            true,
		"personal/.muse-memory.lock":          false,
		"personal/notes.txt":                  false,
		"sessions/s.md":                       false,
		"p.md":                                false,
	} {
		if got := memoryAllowed(root, rel); got != want {
			t.Errorf("memoryAllowed(%q) = %v, want %v", rel, got, want)
		}
	}
	if _, ok := memoryRootByKind("claude"); !ok {
		t.Fatal("claude root vanished")
	}
}

// Without a muse store the root stays out of Targets (nothing to switch on) but remains
// resolvable, so a restore can recreate a deleted store and an import has a destination.
func TestMemoryMuseRootAbsentIsHiddenButUsable(t *testing.T) {
	memoryTestEnv(t)
	if _, ok := memoryRootByKind("muse"); !ok {
		t.Fatal("muse root must stay active without a store")
	}
	for _, r := range memoryInactiveRoots() {
		if r.Kind == "muse" {
			t.Fatalf("muse listed as inactive: %+v", r)
		}
	}
	rec := httptest.NewRecorder()
	HandleMemoryRoots(rec, httptest.NewRequest("GET", "/api/memory/roots", nil))
	if strings.Contains(rec.Body.String(), `"kind":"muse"`) {
		t.Errorf("absent muse store shown in Targets: %s", rec.Body.String())
	}
}

// Deleting the store after a snapshot must not make it unrestorable.
func TestMemoryMuseRestoreRecreatesDeletedStore(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	mem := museMemoryDir(t, home)
	v1, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v1.Committed {
		t.Fatalf("snapshot: %+v %v", v1, err)
	}
	if err := os.RemoveAll(mem); err != nil {
		t.Fatal(err)
	}
	for _, sc := range []memoryRestoreScope{{Kinds: []string{"muse"}}, {All: true}} {
		if err := os.RemoveAll(mem); err != nil {
			t.Fatal(err)
		}
		if _, err := memoryRestore(sc, v1.Rev, "", time.Now()); err != nil {
			t.Fatalf("restore %+v: %v", sc, err)
		}
		if got := memoryReadLive(t, filepath.Join(mem, "personal", "p.md")); got != "personal v1\n" {
			t.Errorf("scope %+v: p.md = %q", sc, got)
		}
	}
}

func TestMemoryAllowedRejectsDotDotAndRestoreTemp(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	museMemoryDir(t, home)
	for _, kind := range []string{"muse", "claude", "codex"} {
		decls := memoryRootDecls()
		for _, r := range decls {
			if r.Kind != kind {
				continue
			}
			for _, rel := range []string{"personal/../../x.md", "-s/memory/../../x.md", "-s/memory/MEMORY.md" + memoryRestoreTmpSuffix, "a/b.md" + memoryRestoreTmpSuffix} {
				if memoryAllowed(r, rel) {
					t.Errorf("%s: memoryAllowed(%q) = true", kind, rel)
				}
			}
		}
	}
}

func TestMemoryReplaceFileKeepsMode(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "s.md"), filepath.Join(dir, "d.md")
	memoryWrite(t, src, "new\n")
	memoryWrite(t, dst, "old\n")
	if err := os.Chmod(dst, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := memoryReplaceFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", st.Mode().Perm())
	}
}

func TestMemoryMuseSnapshotAndKindRestore(t *testing.T) {
	home, cfg, slug := memoryTestEnv(t)
	mem := museMemoryDir(t, home)

	v1, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v1.Committed {
		t.Fatalf("snapshot: %+v %v", v1, err)
	}
	files, err := memoryGitRun("ls-tree", "-r", "--name-only", v1.Rev, "--", "muse")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files, "lock") || strings.Contains(files, "notes.txt") {
		t.Errorf("non-note files entered the history:\n%s", files)
	}
	if !strings.Contains(files, "muse/projects/demo-0123456789abcdef/sub/m.md") || !strings.Contains(files, "muse/personal/p.md") {
		t.Errorf("notes missing from the history:\n%s", files)
	}

	// Change muse and claude after v1.
	memoryWrite(t, filepath.Join(mem, "personal", "p.md"), "personal v2\n")
	memoryWrite(t, filepath.Join(mem, "personal", "new.md"), "added\n")
	_ = os.Remove(filepath.Join(mem, "projects", "demo-0123456789abcdef", "n.md"))
	claudeNote := filepath.Join(cfg, "projects", slug, "memory", "a.md")
	memoryWrite(t, claudeNote, "claude moved on\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}

	res, err := memoryRestore(memoryRestoreScope{Kinds: []string{"muse"}}, v1.Rev, "", time.Now())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "personal", "p.md")); got != "personal v1\n" {
		t.Errorf("p.md = %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "projects", "demo-0123456789abcdef", "n.md")); got != "project v1\n" {
		t.Errorf("n.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(mem, "personal", "new.md")); !os.IsNotExist(err) {
		t.Errorf("note added after the rev survived (err=%v)", err)
	}
	// Outside the allowlist: untouched.
	if got := memoryReadLive(t, filepath.Join(mem, "personal", ".muse-memory.lock")); got != "pid 1\n" {
		t.Errorf("lock file touched: %q", got)
	}
	if got := memoryReadLive(t, filepath.Join(mem, "personal", "notes.txt")); got != "not a note\n" {
		t.Errorf("non-note touched: %q", got)
	}
	// Another kind is not crossed.
	if got := memoryReadLive(t, claudeNote); got != "claude moved on\n" {
		t.Errorf("muse-kind restore reached claude: %q", got)
	}
	if len(res.Scopes) != 1 || res.Scopes[0] != "muse" {
		t.Errorf("scopes = %v", res.Scopes)
	}
	// No temp file is left behind by the atomic write.
	_ = filepath.WalkDir(mem, func(p string, d os.DirEntry, _ error) error {
		if strings.HasSuffix(p, memoryRestoreTmpSuffix) {
			t.Errorf("temp file left: %s", p)
		}
		return nil
	})
}

// A restore replaces a file by rename, so a reader that opened it earlier keeps the complete old
// content instead of seeing it truncated in place.
func TestMemoryRestoreReplacesFileAtomically(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.md"), filepath.Join(dir, "dst.md")
	memoryWrite(t, src, "new content\n")
	memoryWrite(t, dst, "old content\n")
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := memoryReplaceFile(src, dst); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 64)
	n, _ := f.Read(b)
	if string(b[:n]) != "old content\n" {
		t.Errorf("open handle saw %q, want the intact old content", b[:n])
	}
	if got := memoryReadLive(t, dst); got != "new content\n" {
		t.Errorf("dst = %q", got)
	}
}

// Through memoryRestore itself: a handle opened on a note before the restore still reads the
// intact old content afterwards, and the note on disk is the restored one.
func TestMemoryMuseRestoreKeepsOpenHandleIntact(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	mem := museMemoryDir(t, home)
	v1, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !v1.Committed {
		t.Fatalf("snapshot: %+v %v", v1, err)
	}
	note := filepath.Join(mem, "personal", "p.md")
	memoryWrite(t, note, "personal v2, longer than v1\n")
	f, err := os.Open(note)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := memoryRestore(memoryRestoreScope{Kinds: []string{"muse"}}, v1.Rev, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 128)
	n, _ := f.Read(b)
	if string(b[:n]) != "personal v2, longer than v1\n" {
		t.Errorf("open handle saw %q", b[:n])
	}
	if got := memoryReadLive(t, note); got != "personal v1\n" {
		t.Errorf("note = %q", got)
	}
}
