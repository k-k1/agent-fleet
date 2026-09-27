package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirtyWorktree adds a worktree of parent on branch with one commit of its own, then leaves
// it modified, with a file deleted, an untracked file and an ignored one.
func dirtyWorktree(t *testing.T, parent, name, branch string) (dir, head string) {
	t.Helper()
	dir = filepath.Join(filepath.Dir(parent), name)
	gitAt(t, parent, "worktree", "add", "-q", "-b", branch, dir, "main")
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(dir, "gone.txt"), "tracked, to be deleted")
	gitAt(t, dir, "add", "-A")
	gitAt(t, dir, "commit", "-q", "-m", "wip")
	head = strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(dir, "f"), "edited")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "new", "untracked.txt"), "untracked")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "ignored")
	gitAt(t, dir, "add", "f") // one change staged, the rest not
	return dir, head
}

func TestPrepareTombstoneSnapshotsTheWorkingTree(t *testing.T) {
	parent := recreateFixture(t)
	dir, head := dirtyWorktree(t, parent, "app@wip", "wip")
	statusBefore := gitAt(t, dir, "status", "--porcelain")

	tomb, ok, err := PrepareTombstone(dir, parent)
	if err != nil || !ok {
		t.Fatalf("PrepareTombstone = %v, %v", ok, err)
	}
	if tomb.Head != head || tomb.Branch != "wip" || tomb.Name != "app@wip" || tomb.Parent != parent || tomb.Snapshot == "" {
		t.Fatalf("tombstone = %+v", tomb)
	}
	if p := strings.TrimSpace(gitAt(t, dir, "rev-parse", tomb.Snapshot+"^")); p != head {
		t.Errorf("snapshot parent = %s, want HEAD %s", p, head)
	}
	files := strings.Fields(gitAt(t, dir, "ls-tree", "-r", "--name-only", tomb.Snapshot))
	want := []string{".gitignore", "f", "new/untracked.txt"}
	if strings.Join(files, " ") != strings.Join(want, " ") {
		t.Errorf("snapshot files = %v, want %v (deleted and ignored ones left out)", files, want)
	}
	if got := gitAt(t, dir, "show", tomb.Snapshot+":f"); got != "edited" {
		t.Errorf("snapshot f = %q", got)
	}
	// Built in a temporary index: the worktree's own staging is exactly as it was.
	if after := gitAt(t, dir, "status", "--porcelain"); after != statusBefore {
		t.Errorf("status changed by the snapshot:\n%s\nwas\n%s", after, statusBefore)
	}
	if stash := gitAt(t, parent, "stash", "list"); stash != "" {
		t.Errorf("the shared stash was used: %s", stash)
	}
}

func TestPrepareTombstoneCleanDetachedAndUnborn(t *testing.T) {
	parent := recreateFixture(t)
	root := filepath.Dir(parent)

	clean := filepath.Join(root, "app@clean")
	gitAt(t, parent, "worktree", "add", "-q", "-b", "clean", clean, "main")
	if tomb, ok, err := PrepareTombstone(clean, parent); err != nil || !ok || tomb.Snapshot != "" || tomb.Branch != "clean" {
		t.Errorf("clean = %+v, %v, %v; want no snapshot", tomb, ok, err)
	}

	detached := filepath.Join(root, "app@det")
	gitAt(t, parent, "worktree", "add", "-q", "--detach", detached, "main")
	if tomb, ok, err := PrepareTombstone(detached, parent); err != nil || !ok || tomb.Branch != "" {
		t.Errorf("detached = %+v, %v, %v; want no branch", tomb, ok, err)
	}

	unborn := filepath.Join(root, "app@unborn")
	gitAt(t, parent, "worktree", "add", "-q", "--orphan", "-b", "unborn", unborn)
	if tomb, ok, err := PrepareTombstone(unborn, parent); err != nil || ok {
		t.Errorf("unborn = %+v, %v, %v; want nothing to record", tomb, ok, err)
	}
}

// The whole way round: delete a dirty worktree whose branch is then deleted too and whose
// objects are gc'd, and bring it back from the tombstone alone.
func TestRecreateFromTombstoneSurvivesGC(t *testing.T) {
	parent := recreateFixture(t)
	dir, head := dirtyWorktree(t, parent, "app@wip", "wip")
	tomb, _, err := PrepareTombstone(dir, parent)
	if err != nil {
		t.Fatal(err)
	}
	tomb.Ref = DeletedWorktreeRefPrefix + "app-wip-1"
	if err := PinDeletedWorktree(tomb); err != nil {
		t.Fatal(err)
	}
	gitAt(t, parent, "worktree", "remove", "--force", dir)
	gitAt(t, parent, "branch", "-q", "-D", "wip")
	gitAt(t, parent, "reflog", "expire", "--expire=now", "--all")
	gitAt(t, parent, "gc", "-q", "--prune=now")

	cands := ResolveRecreate(parent, "app@wip", []string{"wip"}, nil, &tomb)
	if len(cands) == 0 || cands[0] != (RecreateCandidate{Source: RecreateDeleted, Branch: "wip", SHA: head, Snapshot: tomb.Snapshot}) {
		t.Fatalf("candidates = %+v", cands)
	}
	if err := RecreateWorktreeAt(parent, dir, cands[0], ""); err != nil {
		t.Fatal(err)
	}
	if b := GitCurrentBranch(dir); b != "wip" || strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD")) != head {
		t.Errorf("recreated on %s, want wip at %s", b, head)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f")); string(got) != "edited" {
		t.Errorf("f = %q, want the uncommitted edit", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "new", "untracked.txt")); string(got) != "untracked" {
		t.Errorf("untracked file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("a file deleted before the delete came back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ignored.txt")); !os.IsNotExist(err) {
		t.Errorf("an ignored file came back: %v", err)
	}
	// Everything comes back unstaged: the index is HEAD's.
	if staged := gitAt(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("staged after recreate: %s", staged)
	}

	// Unpinned, the snapshot is no longer protected.
	UnpinDeletedWorktree(tomb)
	if OK(parent, "show-ref", "--verify", "--quiet", tomb.Ref) {
		t.Error("the pin survived UnpinDeletedWorktree")
	}
}

func TestDeletedCandidateBranchState(t *testing.T) {
	parent := recreateFixture(t)
	root := filepath.Dir(parent)
	dir, head := dirtyWorktree(t, parent, "app@wip", "wip")
	tomb, _, _ := PrepareTombstone(dir, parent)
	gitAt(t, parent, "worktree", "remove", "--force", dir)

	// The branch moved on after the delete: taking it would not be what was deleted.
	gitAt(t, parent, "branch", "-q", "-f", "wip", "main")
	c := deletedCandidate(parent, &tomb)
	if len(c) != 1 || !c[0].Moved || !c[0].NeedsNewBranch() {
		t.Fatalf("moved branch = %+v", c)
	}
	if err := RecreateWorktreeAt(parent, dir, c[0], "wip-restored"); err != nil {
		t.Fatal(err)
	}
	if b := GitCurrentBranch(dir); b != "wip-restored" || strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD")) != head {
		t.Errorf("on a new branch = %s, want wip-restored at %s", b, head)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f")); string(got) != "edited" {
		t.Errorf("snapshot not laid over the new branch: f = %q", got)
	}

	// Checked out elsewhere: in use, by the copy that holds it.
	gitAt(t, parent, "worktree", "add", "-q", filepath.Join(root, "app@holder"), "wip")
	if c := deletedCandidate(parent, &tomb); len(c) != 1 || c[0].InUse != "app@holder" {
		t.Fatalf("in use = %+v", c)
	}
	// Another repository's tombstone is not a candidate here.
	other := tomb
	other.Parent = filepath.Join(root, "other")
	if c := deletedCandidate(parent, &other); c != nil {
		t.Errorf("foreign tombstone = %+v", c)
	}
}
