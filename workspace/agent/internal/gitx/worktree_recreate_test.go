package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// recreateFixture clones a local origin (main + feature) into ~/repos/app under a fresh HOME
// and returns the parent's path. origin/feature exists only as a remote-tracking branch.
func recreateFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	origin := filepath.Join(home, "origin")
	gitInit(t, origin)
	parent := filepath.Join(home, "repos", "app")
	if err := os.MkdirAll(filepath.Dir(parent), 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, home, "clone", "-q", origin, parent)
	return parent
}

// commitOn creates branch off main in dir with one commit and returns that commit.
func commitOn(t *testing.T, dir, branch string) string {
	t.Helper()
	gitAt(t, dir, "checkout", "-q", "-b", branch, "main")
	writeFile(t, filepath.Join(dir, strings.ReplaceAll(branch, "/", "_")), branch)
	gitAt(t, dir, "add", "-A")
	gitAt(t, dir, "commit", "-q", "-m", branch)
	sha := strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD"))
	gitAt(t, dir, "checkout", "-q", "main")
	return sha
}

func TestRecreateParent(t *testing.T) {
	parent := recreateFixture(t)
	root := filepath.Dir(parent)
	for _, name := range []string{"app@x", "app@wip-A@wip-B"} {
		if got, ok := RecreateParent(name); !ok || got != parent {
			t.Errorf("RecreateParent(%q) = %q, %v; want %q", name, got, ok, parent)
		}
	}
	// A surviving middle worktree resolves to the main working copy that holds the registry.
	gitAt(t, parent, "worktree", "add", "-q", filepath.Join(root, "app@wip-A"), "-b", "wip-A")
	if got, ok := RecreateParent("app@wip-A@wip-B"); !ok || got != parent {
		t.Errorf("RecreateParent via a live worktree = %q, %v; want %q", got, ok, parent)
	}
	for _, name := range []string{"app", "other@x", "@x"} {
		if got, ok := RecreateParent(name); ok {
			t.Errorf("RecreateParent(%q) = %q, want none", name, got)
		}
	}
}

func TestResolveRecreateSources(t *testing.T) {
	parent := recreateFixture(t)
	root := filepath.Dir(parent)

	localSHA := commitOn(t, parent, "loc")
	remoteSHA := strings.TrimSpace(gitAt(t, parent, "rev-parse", "origin/feature"))
	trashSHA := commitOn(t, parent, "gone")
	gitAt(t, parent, "branch", "-q", "-D", "gone")
	prSHA := commitOn(t, parent, "pr-x")
	gitAt(t, parent, "merge", "-q", "--no-ff", "-m", "Merge pull request #7 from owner/pr-x", "pr-x")
	gitAt(t, parent, "branch", "-q", "-D", "pr-x")
	gitSHA := commitOn(t, parent, "feat/plain")
	gitAt(t, parent, "merge", "-q", "--no-ff", "-m", "Merge branch 'feat/plain' into 'main'", "feat/plain")
	gitAt(t, parent, "branch", "-q", "-D", "feat/plain")
	busySHA := commitOn(t, parent, "busy")
	gitAt(t, parent, "worktree", "add", "-q", filepath.Join(root, "app@busy"), "busy")

	trash := func(b string) string {
		if b == "gone" {
			return trashSHA
		}
		if b == "nothing" {
			return strings.Repeat("0", 40) // recorded, but the commit is not in the repository
		}
		return ""
	}
	got := ResolveRecreate(parent, "app@whatever",
		[]string{"loc", "feature", "gone", "pr-x", "feat/plain", "busy", "loc", "", "(detached)", "-x", "nothing"}, trash, nil)
	want := []RecreateCandidate{
		{Source: RecreateLocal, Branch: "loc", SHA: localSHA},
		{Source: RecreateRemote, Branch: "feature", SHA: remoteSHA, Ref: "origin/feature"},
		{Source: RecreateTrash, Branch: "gone", SHA: trashSHA},
		{Source: RecreateMerged, Branch: "pr-x", SHA: prSHA, PR: 7},
		{Source: RecreateMerged, Branch: "feat/plain", SHA: gitSHA},
		{Source: RecreateLocal, Branch: "busy", SHA: busySHA, InUse: "app@busy"},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Nothing left of the branch: a new one of the same name off the parent's branch.
	if got := ResolveRecreate(parent, "app@nothing", []string{"nothing"}, trash, nil); len(got) != 1 ||
		got[0] != (RecreateCandidate{Source: RecreateNew, Branch: "nothing", Ref: "main"}) {
		t.Errorf("nothing left = %+v, want one new off main", got)
	}
	// A merge commit that names another branch whose name merely contains ours is not a match.
	if got := ResolveRecreate(parent, "app@x", []string{"pr"}, nil, nil); len(got) != 1 || got[0].Source != RecreateNew {
		t.Errorf("prefix of a merged branch = %+v, want new", got)
	}
}

func TestResolveRecreateFolderSegFallback(t *testing.T) {
	parent := recreateFixture(t)
	sha := commitOn(t, parent, "feat/y")
	if got := ResolveRecreate(parent, "app@feat-y", nil, nil, nil); len(got) != 1 ||
		got[0] != (RecreateCandidate{Source: RecreateLocal, Branch: "feat/y", SHA: sha}) {
		t.Errorf("seg matching a local branch = %+v", got)
	}
	if got := ResolveRecreate(parent, "app@wip-q", nil, nil, nil); len(got) != 1 ||
		got[0] != (RecreateCandidate{Source: RecreateNew, Branch: "wip-q", Ref: "main"}) {
		t.Errorf("seg matching nothing = %+v", got)
	}
}

func TestRecreateWorktreeAt(t *testing.T) {
	parent := recreateFixture(t)
	root := filepath.Dir(parent)
	head := func(dir string) (branch, sha string) {
		return GitCurrentBranch(dir), strings.TrimSpace(gitAt(t, dir, "rev-parse", "HEAD"))
	}

	// A nested name EnsureWorktree could not reproduce, from a local branch.
	localSHA := commitOn(t, parent, "wip-B")
	nested := filepath.Join(root, "app@wip-A@wip-B")
	if err := RecreateWorktreeAt(parent, nested, RecreateCandidate{Source: RecreateLocal, Branch: "wip-B", SHA: localSHA}, ""); err != nil {
		t.Fatal(err)
	}
	if b, s := head(nested); b != "wip-B" || s != localSHA || !IsLinkedWorktree(nested) {
		t.Errorf("nested = %s@%s linked=%v", b, s, IsLinkedWorktree(nested))
	}
	// Something at the path already: refused, and left alone.
	if err := RecreateWorktreeAt(parent, nested, RecreateCandidate{Source: RecreateNew, Branch: "zz"}, ""); !errors.Is(err, ErrRecreatePathExists) {
		t.Errorf("occupied path: err = %v", err)
	}

	// Remote-only: a local branch tracking it.
	remote := filepath.Join(root, "app@feature")
	if err := RecreateWorktreeAt(parent, remote, RecreateCandidate{Source: RecreateRemote, Branch: "feature", Ref: "origin/feature"}, ""); err != nil {
		t.Fatal(err)
	}
	if up := strings.TrimSpace(gitAt(t, remote, "rev-parse", "--abbrev-ref", "@{upstream}")); up != "origin/feature" {
		t.Errorf("remote recreate upstream = %q", up)
	}

	// A branch at a recorded commit (trash / merged).
	gone := commitOn(t, parent, "gone")
	gitAt(t, parent, "branch", "-q", "-D", "gone")
	trashDir := filepath.Join(root, "app@gone")
	if err := RecreateWorktreeAt(parent, trashDir, RecreateCandidate{Source: RecreateTrash, Branch: "gone", SHA: gone}, ""); err != nil {
		t.Fatal(err)
	}
	if b, s := head(trashDir); b != "gone" || s != gone {
		t.Errorf("trash = %s@%s, want gone@%s", b, s, gone)
	}

	// A branch checked out elsewhere: a new branch at its tip instead.
	alt := filepath.Join(root, "app@wip-B-2")
	if err := RecreateWorktreeAt(parent, alt, RecreateCandidate{Source: RecreateLocal, Branch: "wip-B", SHA: localSHA}, "wip-B-2"); err != nil {
		t.Fatal(err)
	}
	if b, s := head(alt); b != "wip-B-2" || s != localSHA {
		t.Errorf("new branch at tip = %s@%s", b, s)
	}

	// Deleted behind git's back: the leftover registration is pruned instead of refusing.
	if err := os.RemoveAll(trashDir); err != nil {
		t.Fatal(err)
	}
	if err := RecreateWorktreeAt(parent, trashDir, RecreateCandidate{Source: RecreateLocal, Branch: "gone", SHA: gone}, ""); err != nil {
		t.Fatalf("recreate over a stale registration: %v", err)
	}

	// Nothing left: a fresh branch off the base.
	fresh := filepath.Join(root, "app@fresh")
	if err := RecreateWorktreeAt(parent, fresh, RecreateCandidate{Source: RecreateNew, Branch: "fresh", Ref: "main"}, ""); err != nil {
		t.Fatal(err)
	}
	mainSHA := strings.TrimSpace(gitAt(t, parent, "rev-parse", "main"))
	if b, s := head(fresh); b != "fresh" || s != mainSHA {
		t.Errorf("new = %s@%s, want fresh@%s", b, s, mainSHA)
	}
}
