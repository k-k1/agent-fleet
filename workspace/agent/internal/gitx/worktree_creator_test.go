package gitx

import (
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
