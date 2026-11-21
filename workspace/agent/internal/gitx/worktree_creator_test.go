package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorktreeCreatorRecordedOnceAndListed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	wt, created, err := EnsureWorktreeNew(parent, "main", "feat-a", "")
	if err != nil || !created {
		t.Fatalf("first call: created=%v err=%v", created, err)
	}
	if got := ReadWorktreeCreator(wt); got != "" {
		t.Fatalf("creator before it is recorded = %q", got)
	}
	if err := WriteWorktreeCreator(wt, "sess-a"); err != nil {
		t.Fatal(err)
	}
	// Write-once: a second session reusing the copy must not take it over.
	if err := WriteWorktreeCreator(wt, "sess-b"); err != nil {
		t.Fatal(err)
	}
	if got := ReadWorktreeCreator(wt); got != "sess-a" {
		t.Fatalf("creator = %q, want sess-a", got)
	}
	// A relaunch reuses the copy and reports that it did not create it.
	if _, created, err := EnsureWorktreeNew(parent, "feat-a", "", ""); err != nil || created {
		t.Fatalf("reuse: created=%v err=%v", created, err)
	}
	// The parent clone and a copy with no record carry nothing.
	if got := ReadWorktreeCreator(parent); got != "" {
		t.Fatalf("parent creator = %q", got)
	}
	other, _, err := EnsureWorktreeNew(parent, "main", "feat-b", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ReadWorktreeCreator(other); got != "" {
		t.Fatalf("unrecorded copy creator = %q", got)
	}
	// The list wire carries it.
	repos := []Repo{{Name: filepath.Base(wt), Path: wt}, {Name: filepath.Base(other), Path: other}}
	fillGitFacts(listCache, repos, []int{0, 1}, gitGlobals{})
	if repos[0].CreatedBy != "sess-a" || repos[1].CreatedBy != "" {
		t.Fatalf("createdBy = %q, %q", repos[0].CreatedBy, repos[1].CreatedBy)
	}
}

func TestWorktreeCreatorPlaceholderUnreadableAndTombstone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	wt, _, err := EnsureWorktreeNew(parent, "main", "feat-a", "")
	if err != nil {
		t.Fatal(err)
	}
	// Created with no session yet (a launch that failed afterwards).
	if s, st := ReadWorktreeCreatorState(wt); s != "" || st != CreatorStateNone {
		t.Fatalf("placeholder = %q,%q", s, st)
	}
	if raw := worktreeCreatorRaw(wt); raw != "-" {
		t.Fatalf("raw = %q", raw)
	}
	// The launch's own session replaces the placeholder; a later one cannot replace that.
	if err := WriteWorktreeCreator(wt, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorktreeCreator(wt, "sess-b"); err != nil {
		t.Fatal(err)
	}
	if s, st := ReadWorktreeCreatorState(wt); s != "sess-a" || st != "" {
		t.Fatalf("after upgrade = %q,%q", s, st)
	}
	// A record that cannot be read is told apart from none: an empty file stands for a torn write,
	// a directory in its place for an I/O error.
	p := worktreeCreatorPath(wt)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, st := ReadWorktreeCreatorState(wt); st != CreatorStateUnreadable {
		t.Fatalf("empty marker state = %q", st)
	}
	_ = os.Remove(p)
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, st := ReadWorktreeCreatorState(wt); st != CreatorStateUnreadable {
		t.Fatalf("unreadable marker state = %q", st)
	}
	_ = os.Remove(p)
	// No temp files are left behind by the writes.
	if m, _ := filepath.Glob(p + ".tmp-*"); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
	// The tombstone keeps the record and a restore puts it back.
	if err := WriteWorktreeCreator(wt, "sess-a"); err != nil {
		t.Fatal(err)
	}
	tomb, ok, err := PrepareTombstone(wt, parent)
	if err != nil || !ok || tomb.Creator != "sess-a" {
		t.Fatalf("tombstone ok=%v err=%v creator=%q", ok, err, tomb.Creator)
	}
	dir2 := filepath.Join(home, "repos", "restored")
	if _, err := Run(parent, "worktree", "add", "-b", "feat-r", dir2); err != nil {
		t.Fatal(err)
	}
	RestoreWorktreeCreator(dir2, tomb.Creator)
	if got := ReadWorktreeCreator(dir2); got != "sess-a" {
		t.Fatalf("restored creator = %q", got)
	}
	dir3 := filepath.Join(home, "repos", "restored2")
	if _, err := Run(parent, "worktree", "add", "-b", "feat-r2", dir3); err != nil {
		t.Fatal(err)
	}
	RestoreWorktreeCreator(dir3, "-")
	if _, st := ReadWorktreeCreatorState(dir3); st != CreatorStateNone {
		t.Fatalf("restored placeholder state = %q", st)
	}
}

// A filesystem without hard links must still record the creator, as the exclusive create always did.
func TestWorktreeCreatorWithoutHardLinks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	prev := linkFn
	linkFn = func(string, string) error { return errors.New("operation not supported") }
	t.Cleanup(func() { linkFn = prev })
	wt, _, err := EnsureWorktreeNew(parent, "main", "feat-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, st := ReadWorktreeCreatorState(wt); st != CreatorStateNone {
		t.Fatalf("placeholder state = %q", st)
	}
	if err := WriteWorktreeCreator(wt, "sess-a"); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorktreeCreator(wt, "sess-b"); err != nil {
		t.Fatal(err)
	}
	if got := ReadWorktreeCreator(wt); got != "sess-a" {
		t.Fatalf("creator = %q, want sess-a", got)
	}
}

// The placeholder is written inside the worktree creation, so a launch refused afterwards leaves it.
func TestEnsureWorktreeNewLeavesAPlaceholder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	wt, created, err := EnsureWorktreeNew(parent, "main", "feat-a", "")
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, st := ReadWorktreeCreatorState(wt); st != CreatorStateNone {
		t.Fatalf("state = %q, want none", st)
	}
	// A relaunch reusing the copy does not touch it.
	if _, _, err := EnsureWorktreeNew(parent, "feat-a", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, st := ReadWorktreeCreatorState(wt); st != CreatorStateNone {
		t.Fatalf("state after reuse = %q", st)
	}
}
