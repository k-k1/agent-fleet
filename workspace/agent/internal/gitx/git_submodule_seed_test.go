package gitx

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// TestSeedSubmodulesFromParentNested is the same proof one level down, for a submodule that
// itself has a submodule: with both remotes gone, everything that ends up on disk came from the
// parent. It also pins the git behaviour that forces the descent — a nested submodule cannot be
// seeded in the same pass as its parent, because it is not declared anywhere until its parent
// is checked out.
func TestSeedSubmodulesFromParentNested(t *testing.T) {
	git := gitTestEnv(t)
	f := nestedFixture(t, git)

	if submodulePathEmpty(nestedInnerIn(f.parent)) {
		t.Fatal("setup: the parent's nested submodule should be checked out")
	}

	// Both remotes vanish. Everything below therefore comes from the parent's store or not at all.
	for _, p := range []string{f.inner, f.outer} {
		if err := os.Rename(p, p+".gone"); err != nil {
			t.Fatal(err)
		}
	}

	control := filepath.Join(f.root, "control")
	git(t, f.parent, "worktree", "add", "-q", control, "-b", "control")
	if out, err := exec.Command("git", "-C", control, "-c", "protocol.file.allow=always",
		"submodule", "update", "--init", "--recursive").CombinedOutput(); err == nil {
		t.Fatalf("a fresh worktree resolved its nested submodules without the remotes: %s", out)
	}

	wt := filepath.Join(f.root, "wt")
	git(t, f.parent, "worktree", "add", "-q", wt, "-b", "feat")
	seedSubmodulesFromParent(wt, f.parent)

	if submodulePathEmpty(filepath.Join(wt, "libs", "outer")) {
		t.Fatal("the top-level submodule was not seeded")
	}
	if submodulePathEmpty(nestedInnerIn(wt)) {
		t.Fatal("the NESTED submodule was not seeded — seedSubmodulesFrom did not descend into it")
	}
	if gaps := submoduleGaps(wt); len(gaps) != 0 {
		t.Errorf("submoduleGaps after seeding = %+v, want none", gaps)
	}
	origin, err := Run(nestedInnerIn(wt), "remote", "get-url", "origin")
	if err != nil {
		t.Fatal(err)
	}
	if origin != f.inner {
		t.Errorf("nested submodule origin = %q, want the declared remote %q", origin, f.inner)
	}
}

// TestGitSubmodulesUpdateInitsNested covers the fallback the seeding leaves behind: a submodule
// the parent has no copy of is still fetched from the remote, and if it is nested it has to be
// initialized on the way. That is what the --init in runSubmoduleUpdate is for, and this drives
// the agent's own code path rather than git directly.
func TestGitSubmodulesUpdateInitsNested(t *testing.T) {
	git := gitTestEnv(t)
	f := nestedFixture(t, git)
	// The fixture's submodule URLs are local paths, which git refuses to clone by default
	// (CVE-2022-39253). Real submodule URLs are HTTPS and need no such permission, so allowing
	// it here is fixture plumbing, not part of what is under test.
	git(t, "", "config", "--global", "protocol.file.allow", "always")

	wt := filepath.Join(f.root, "wt")
	git(t, f.parent, "worktree", "add", "-q", wt, "-b", "feat")
	if out := gitSubmodulesUpdate(wt); out != submoduleDone {
		t.Fatalf("gitSubmodulesUpdate = %v, want submoduleDone", out)
	}
	if submodulePathEmpty(nestedInnerIn(wt)) {
		t.Error("gitSubmodulesUpdate left the nested submodule empty — `submodule update " +
			"--recursive` skips one that is not initialized, so it needs --init")
	}
}

// TestSubmoduleUpdateRecursiveNeedsInit pins the git behaviour the flag above compensates for:
// without --init, `--recursive` clones the top-level submodule, descends into it, finds the
// nested entry uninitialized and skips it — exit 0 and no output, so nothing looks wrong. If a
// future git initializes nested submodules on its own, this fails and --init can be dropped.
func TestSubmoduleUpdateRecursiveNeedsInit(t *testing.T) {
	git := gitTestEnv(t)
	f := nestedFixture(t, git)

	clone := filepath.Join(f.root, "clone")
	git(t, "", "clone", "-q", f.super, clone)
	git(t, clone, "submodule", "init")
	git(t, clone, "-c", "protocol.file.allow=always", "submodule", "update", "--recursive")

	if !submodulePathEmpty(nestedInnerIn(clone)) {
		t.Fatal("`submodule update --recursive` initialized a nested submodule on its own; " +
			"the --init in runSubmoduleUpdate can be reconsidered")
	}
	// ...and with --init it is populated, so --init really is the difference.
	git(t, clone, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")
	if submodulePathEmpty(nestedInnerIn(clone)) {
		t.Error("even --init --recursive left the nested submodule empty")
	}
}

// TestSeedSubmodulesFromCycles pins that the descent terminates, and that maxSeedDepth is what
// terminates it. The fixture is the worst case that can be built: a `path = .` entry is
// accepted by `submodule init` (measured) and makes filepath.Join(dir, path) return dir itself,
// so the working-copy side of the walk never moves, while a symlink makes <store>/self/modules
// resolve back to <store>, so the store side never moves either.
//
// Even that cannot run forever — the store PATH still grows a segment per level, and measured,
// without the cap this fixture unwinds at depth 41 after ~3 s, where Linux refuses the 41st
// symlink traversal. So the assertion carrying the weight is not the timeout but that the cap
// is what fired: falling back on the filesystem's own limits is not a bound to rely on.
func TestSeedSubmodulesFromCycles(t *testing.T) {
	git := gitTestEnv(t)
	root := t.TempDir()
	wt, store := filepath.Join(root, "wt"), filepath.Join(root, "store")

	git(t, "", "init", "-q", "-b", "main", wt)
	writeFile(t, filepath.Join(wt, "f.txt"), "f\n")
	git(t, wt, "add", "-A")
	git(t, wt, "commit", "-qm", "init")
	writeFile(t, filepath.Join(wt, ".gitmodules"),
		"[submodule \"self\"]\n\tpath = .\n\turl = https://example.invalid/self.git\n")
	writeFile(t, filepath.Join(store, "self", "HEAD"), "ref: refs/heads/main\n")
	if err := os.MkdirAll(filepath.Join(store, "self", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(store, "self", "modules")); err != nil {
		t.Fatal(err)
	}
	// Both halves of the loop have to be real or the test proves nothing.
	if specs := submoduleSpecs(wt); len(specs) != 1 || filepath.Join(wt, specs[0].Path) != wt {
		t.Fatalf("setup: the submodule path does not resolve back to its own working copy: %+v", specs)
	}
	if !isGitDir(filepath.Join(store, "self", "modules", "self")) {
		t.Fatal("setup: the store does not actually loop, so nothing recurses")
	}

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	done := make(chan struct{})
	go func() { defer close(done); seedSubmodulesFrom(wt, store, 0) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("seedSubmodulesFrom did not terminate on a cyclic store — maxSeedDepth is not bounding the descent")
	}
	if !strings.Contains(logs.String(), "nested deeper than") {
		t.Errorf("the depth cap never fired, so the cycle was stopped by something else:\n%s", logs.String())
	}
}

// nestedFixture builds inner ⊂ outer ⊂ super, plus a parent clone with everything checked out
// recursively — the shape the nested tests all need.
type nested struct{ root, inner, outer, super, parent string }

func nestedFixture(t *testing.T, git gitRunner) nested {
	t.Helper()
	f := nested{root: t.TempDir()}
	f.inner = filepath.Join(f.root, "inner")
	f.outer = filepath.Join(f.root, "outer")
	f.super = filepath.Join(f.root, "super")
	f.parent = filepath.Join(f.root, "parent")

	repo := func(dir, file string) {
		git(t, "", "init", "-q", "-b", "main", dir)
		writeFile(t, filepath.Join(dir, file), file+"\n")
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-qm", "init")
	}
	// Local-path submodules need protocol.file.allow (git's CVE-2022-39253 hardening).
	addSub := func(dir, url, path string) {
		git(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", url, path)
		git(t, dir, "commit", "-qm", "add "+path)
	}
	repo(f.inner, "i.txt")
	repo(f.outer, "o.txt")
	addSub(f.outer, f.inner, "nested/inner")
	repo(f.super, "s.txt")
	addSub(f.super, f.outer, "libs/outer")

	git(t, "", "clone", "-q", f.super, f.parent)
	git(t, f.parent, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "-q")
	return f
}

func nestedInnerIn(dir string) string { return filepath.Join(dir, "libs", "outer", "nested", "inner") }

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
