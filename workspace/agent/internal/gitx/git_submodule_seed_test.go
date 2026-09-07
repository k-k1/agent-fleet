package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSubmoduleSpecs pins that the name and the path are read as separate things. git keys
// .git/config and .git/modules by the NAME, so a repo whose submodule is named differently
// from where it sits would otherwise be seeded from a directory that does not exist.
func TestSubmoduleSpecs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "-q")
	writeFile(t, filepath.Join(dir, ".gitmodules"), ""+
		"[submodule \"lib-core\"]\n\tpath = vendor/core\n\turl = https://example.invalid/core.git\n"+
		"[submodule \"libs/big\"]\n\tpath = libs/big\n\turl = https://example.invalid/big.git\n")

	got := submoduleSpecs(dir)
	want := []submoduleSpec{
		{Name: "lib-core", Path: "vendor/core"},
		{Name: "libs/big", Path: "libs/big"},
	}
	if len(got) != len(want) {
		t.Fatalf("submoduleSpecs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("spec %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestSeedSubmodulesFromParent is the whole point of the seeding, pinned by making the
// submodule's remote unreachable: a fresh worktree's submodule store is separate from the
// parent's, so the plain update cannot get the content (the negative control below), while
// seeding gets it from the parent on local disk alone. It also checks the two things that
// would otherwise be silently wrong afterwards — the submodule's origin must be the real
// remote, not the parent's store, and the objects must be shared rather than copied.
func TestSeedSubmodulesFromParent(t *testing.T) {
	git := gitTestEnv(t)
	root := t.TempDir()
	sub, parent := filepath.Join(root, "sub"), filepath.Join(root, "parent")

	git(t, "", "init", "-q", "-b", "main", sub)
	writeFile(t, filepath.Join(sub, "lib.txt"), "lib\n")
	git(t, sub, "add", "-A")
	git(t, sub, "commit", "-qm", "init")

	git(t, "", "init", "-q", "-b", "main", parent)
	writeFile(t, filepath.Join(parent, "p.txt"), "p\n")
	git(t, parent, "add", "-A")
	git(t, parent, "commit", "-qm", "init")
	// Local-path submodules need protocol.file.allow (git's CVE-2022-39253 hardening).
	git(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "libs/sub")
	git(t, parent, "commit", "-qm", "add submodule")

	// From here on the submodule's remote does not exist. Anything that still works is
	// working off the parent's copy and nothing else.
	if err := os.Rename(sub, sub+".gone"); err != nil {
		t.Fatal(err)
	}

	// Negative control: without seeding, the parent's fully populated store is of no use to a
	// new worktree at all. If a future git ever starts sharing it, this fails and the seeding
	// can be reconsidered.
	control := filepath.Join(root, "control")
	git(t, parent, "worktree", "add", "-q", control, "-b", "control")
	if out, err := exec.Command("git", "-C", control, "-c", "protocol.file.allow=always",
		"submodule", "update", "--init", "--recursive").CombinedOutput(); err == nil {
		t.Fatalf("a fresh worktree resolved its submodule without the remote: %s", out)
	}

	wt := filepath.Join(root, "wt")
	git(t, parent, "worktree", "add", "-q", wt, "-b", "feat")
	smDir := filepath.Join(wt, "libs", "sub")

	seedSubmodulesFromParent(wt, parent)

	if submodulePathEmpty(smDir) {
		t.Fatal("seedSubmodulesFromParent left the submodule empty — the parent's store was not used")
	}
	if gaps := submoduleGaps(wt); len(gaps) != 0 {
		t.Errorf("submoduleGaps after seeding = %+v, want none", gaps)
	}

	// origin must be the declared remote. If it were left as the parent's module store, every
	// later fetch — including the wedge repair — would silently follow the parent instead.
	origin, err := Run(smDir, "remote", "get-url", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if origin != sub {
		t.Errorf("submodule origin = %q, want the declared remote %q", origin, sub)
	}
	// The shared .git/config must never have been pointed at the local store: the parent and
	// every sibling worktree read the same file.
	if url, err := Run(wt, "config", "--get", "submodule.libs/sub.url"); err != nil || url != sub {
		t.Errorf("shared config submodule url = %q (err %v), want %q", url, err, sub)
	}

	// Hardlinked, not copied: this is what keeps N worktrees from costing N times the
	// submodule's size on disk.
	if !sharesInode(t, filepath.Join(parent, ".git", "modules", "libs/sub"), gitDirOf(t, smDir)) {
		t.Error("the seeded submodule's objects are copies, not hardlinks into the parent's store")
	}
}

// gitDirOf returns the absolute git directory of a working copy.
func gitDirOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := Run(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sharesInode reports whether the two object stores have at least one loose object file in
// common by inode — the signature of git's local-clone hardlinking.
func sharesInode(t *testing.T, a, b string) bool {
	t.Helper()
	inodes := map[uint64]bool{}
	for _, p := range looseObjects(t, filepath.Join(a, "objects")) {
		inodes[inodeOf(t, p)] = true
	}
	if len(inodes) == 0 {
		t.Fatal("the parent's store holds no loose objects, so the check proves nothing")
	}
	for _, p := range looseObjects(t, filepath.Join(b, "objects")) {
		if inodes[inodeOf(t, p)] {
			return true
		}
	}
	return false
}

func looseObjects(t *testing.T, objects string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(objects, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	return found
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no inode information on this platform")
	}
	return st.Ino
}
