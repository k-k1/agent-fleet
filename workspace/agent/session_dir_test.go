package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSessionsInDir covers the branch-switch guard's core: which running sessions
// count as "occupying" a working copy. dir equality and strict-subdir match; siblings
// with a shared prefix, archived, stopped, and parent-dir sessions must not.
func TestSessionsInDir(t *testing.T) {
	metas := []sessionMeta{
		{Name: "a", Dir: "/repos/foo", Title: "root"},           // exact match, live
		{Name: "b", Dir: "/repos/foo/sub", Title: "subdir"},     // strict subdir, live
		{Name: "c", Dir: "/repos/foobar", Title: "sibling"},     // shared prefix, NOT under foo
		{Name: "d", Dir: "/repos/foo", Title: "stopped"},        // under foo but not live
		{Name: "e", Dir: "/repos/foo", Title: "archived", Archived: true},
		{Name: "f", Dir: "/repos", Title: "parent"},             // parent dir, not under foo
	}
	live := map[string]bool{"a": true, "b": true, "c": true, "e": true, "f": true} // "d" stopped

	got := sessionsInDir(metas, live, "/repos/foo")
	want := []string{"root", "subdir"} // sorted display names
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionsInDir = %v, want %v", got, want)
	}

	// A clean working copy (no live sessions under it) must return empty so the
	// checkout guard lets the switch through.
	if got := sessionsInDir(metas, live, "/repos/baz"); len(got) != 0 {
		t.Fatalf("sessionsInDir(clean) = %v, want empty", got)
	}
}

// TestAnnotateBranchDrift covers branch-drift detection: a session is flagged only
// when its working copy's current branch differs from the one it started on. No start
// branch, a matching branch, or an unresolvable dir ("") must not flag. Resolution is
// cached per dir (one lookup even with two sessions sharing a working copy).
func TestAnnotateBranchDrift(t *testing.T) {
	cur := map[string]string{
		"/repos/foo": "feature-x", // drifted from main
		"/repos/bar": "main",      // unchanged
		"/repos/gone": "",         // dir not a git tree
	}
	calls := 0
	resolve := func(dir string) string { calls++; return cur[dir] }

	sessions := []Session{
		{Name: "a", Dir: "/repos/foo", Branch: "main"},   // drift → main→feature-x
		{Name: "b", Dir: "/repos/foo", Branch: "main"},   // same dir, cached (no 2nd call)
		{Name: "c", Dir: "/repos/bar", Branch: "main"},   // no drift
		{Name: "d", Dir: "/repos/foo", Branch: ""},       // no start branch → skip
		{Name: "e", Dir: "/repos/gone", Branch: "main"},  // unresolvable → no drift
	}
	annotateBranchDrift(sessions, resolve)

	for _, s := range sessions {
		wantDrift := s.Name == "a" || s.Name == "b"
		if s.BranchDrift != wantDrift {
			t.Errorf("%s: BranchDrift = %v, want %v", s.Name, s.BranchDrift, wantDrift)
		}
		if wantDrift && s.CurrentBranch != "feature-x" {
			t.Errorf("%s: CurrentBranch = %q, want feature-x", s.Name, s.CurrentBranch)
		}
		if !wantDrift && s.CurrentBranch != "" {
			t.Errorf("%s: CurrentBranch = %q, want empty", s.Name, s.CurrentBranch)
		}
	}
	// foo, bar, gone = 3 distinct dirs resolved once each; the "" start-branch session
	// short-circuits before resolving.
	if calls != 3 {
		t.Errorf("resolve called %d times, want 3 (cached per dir)", calls)
	}
}

// TestGitCurrentBranch exercises the real git primitive the drift check is built on:
// a normal branch name, the "(detached)" sentinel on a detached HEAD, and "" for a
// non-git dir. Reuses gitInit (git_ensure_test.go) which leaves the repo on "main".
func TestGitCurrentBranch(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), "app")
	gitInit(t, dir)

	if got := gitCurrentBranch(dir); got != "main" {
		t.Fatalf("gitCurrentBranch(fresh) = %q, want main", got)
	}

	// Detach HEAD at the current commit → sentinel, not a branch name.
	sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "checkout", strings.TrimSpace(string(sha))).CombinedOutput(); err != nil {
		t.Fatalf("detach: %v: %s", err, out)
	}
	if got := gitCurrentBranch(dir); got != "(detached)" {
		t.Fatalf("gitCurrentBranch(detached) = %q, want (detached)", got)
	}

	// A path that isn't a git working tree resolves to "".
	if got := gitCurrentBranch(t.TempDir()); got != "" {
		t.Fatalf("gitCurrentBranch(non-git) = %q, want empty", got)
	}
}

func execLookPathGit() (string, error) { return exec.LookPath("git") }

// TestEnsureWorktree exercises the worktree-then-start core against a real repo:
// forking a new branch off a base, idempotent reuse, checking out an existing branch,
// and the ~/repos/<repo>@<branch> naming (with a slash branch sanitized).
func TestEnsureWorktree(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent) // repo on "main", plus a "feature" branch

	// New branch off base main → ~/repos/app@feat-x, checked out to feat-x.
	dir, err := ensureWorktree(parent, "main", "feat-x")
	if err != nil {
		t.Fatalf("ensureWorktree new: %v", err)
	}
	if want := filepath.Join(home, "repos", "app@feat-x"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if b := gitCurrentBranch(dir); b != "feat-x" {
		t.Fatalf("worktree branch = %q, want feat-x", b)
	}

	// Idempotent: same call returns the same path without error.
	if again, err := ensureWorktree(parent, "main", "feat-x"); err != nil || again != dir {
		t.Fatalf("ensureWorktree reuse = (%q,%v), want (%q,nil)", again, err, dir)
	}

	// Existing branch, no new branch → ~/repos/app@feature on that branch.
	fdir, err := ensureWorktree(parent, "feature", "")
	if err != nil {
		t.Fatalf("ensureWorktree existing: %v", err)
	}
	if want := filepath.Join(home, "repos", "app@feature"); fdir != want {
		t.Fatalf("existing dir = %q, want %q", fdir, want)
	}
	if b := gitCurrentBranch(fdir); b != "feature" {
		t.Fatalf("existing worktree branch = %q, want feature", b)
	}

	// A slash in the new branch is sanitized in the folder but kept as the ref.
	sdir, err := ensureWorktree(parent, "main", "fix/bug-1")
	if err != nil {
		t.Fatalf("ensureWorktree slash: %v", err)
	}
	if want := filepath.Join(home, "repos", "app@fix-bug-1"); sdir != want {
		t.Fatalf("slash dir = %q, want %q", sdir, want)
	}
	if b := gitCurrentBranch(sdir); b != "fix/bug-1" {
		t.Fatalf("slash worktree branch = %q, want fix/bug-1", b)
	}
}

// TestWorktreeDeleteHelpers covers the worktree-aware delete path's building blocks
// against real git: worktree detection, parent resolution, linked-worktree counting
// (which guards deleting a parent), and `worktree remove --force` cleanup.
func TestWorktreeDeleteHelpers(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	wt, err := ensureWorktree(parent, "main", "feat-x")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}

	if !isLinkedWorktree(wt) {
		t.Errorf("isLinkedWorktree(worktree) = false, want true")
	}
	if isLinkedWorktree(parent) {
		t.Errorf("isLinkedWorktree(parent) = true, want false")
	}
	if got := worktreeParent(wt); got != parent {
		t.Errorf("worktreeParent = %q, want %q", got, parent)
	}
	// `git worktree list` reports the whole set regardless of which worktree it runs
	// from, so the count is 1 (one linked worktree) whether asked from parent or wt.
	// The delete handler only calls this on the main working copy, where 1 => refuse.
	if got := linkedWorktreeCount(parent); got != 1 {
		t.Errorf("linkedWorktreeCount(parent) = %d, want 1", got)
	}

	// Forced removal (the handler's core) drops the worktree and its registry entry.
	if out, err := exec.Command("git", "-C", parent, "worktree", "remove", "--force", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree remove: %v: %s", err, out)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Errorf("worktree dir still exists after remove")
	}
	if got := linkedWorktreeCount(parent); got != 0 {
		t.Errorf("linkedWorktreeCount after remove = %d, want 0", got)
	}
}

// TestMaybePruneWorktreeKeeps verifies auto-cleanup is conservative: it removes a
// worktree only when it is clean AND unreferenced. A dirty worktree, or one a session
// meta still points at, must be left in place. (The clean+unreferenced removal path is
// covered end-to-end by TestWorktreeGuardDriftFlow.)
func TestMaybePruneWorktreeKeeps(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)

	// Dirty worktree: an uncommitted file must NOT be auto-removed (work would be lost).
	dirty, err := ensureWorktree(parent, "main", "dirty-x")
	if err != nil {
		t.Fatalf("ensureWorktree dirty: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirty, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	maybePruneWorktree(dirty)
	if !isGitRepo(dirty) {
		t.Errorf("dirty worktree was auto-removed; should be kept")
	}

	// Referenced worktree: clean, but a session meta points at it → kept.
	ref, err := ensureWorktree(parent, "main", "ref-x")
	if err != nil {
		t.Fatalf("ensureWorktree ref: %v", err)
	}
	writeSessionMeta(sessionMeta{Name: "zz", Dir: ref, Kind: "shell"})
	maybePruneWorktree(ref)
	if !isGitRepo(ref) {
		t.Errorf("referenced worktree was auto-removed; should be kept while a meta points at it")
	}

	// A non-worktree (the parent) is never touched.
	maybePruneWorktree(parent)
	if !isGitRepo(parent) {
		t.Errorf("parent working copy was removed; maybePruneWorktree must ignore non-worktrees")
	}
}
