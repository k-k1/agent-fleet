package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TestSessionsInDir covers the branch-switch guard's core: which running sessions
// count as "occupying" a working copy. dir equality and strict-subdir match; siblings
// with a shared prefix, archived, stopped, and parent-dir sessions must not.
func TestSessionsInDir(t *testing.T) {
	metas := []session.Meta{
		{Name: "a", Dir: "/repos/foo", Title: "root"},       // exact match, live
		{Name: "b", Dir: "/repos/foo/sub", Title: "subdir"}, // strict subdir, live
		{Name: "c", Dir: "/repos/foobar", Title: "sibling"}, // shared prefix, NOT under foo
		{Name: "d", Dir: "/repos/foo", Title: "stopped"},    // under foo but not live
		{Name: "e", Dir: "/repos/foo", Title: "archived", Archived: true},
		{Name: "f", Dir: "/repos", Title: "parent"}, // parent dir, not under foo
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

// TestAnnotateSessions covers session enrichment: Worktree flag (always applied) and
// branch drift (only when a recorded start branch differs from the current one). Dir
// info is cached per dir (one lookup even when two sessions share a working copy).
func TestAnnotateSessions(t *testing.T) {
	info := map[string]dirInfo{
		"/repos/foo":  {branch: "feature-x", worktree: true}, // drifted from main, is a worktree
		"/repos/bar":  {branch: "main", worktree: false},     // unchanged, plain clone
		"/repos/gone": {branch: "", worktree: false},         // dir not a git tree
	}
	calls := 0
	resolve := func(dir string) dirInfo { calls++; return info[dir] }

	sessions := []session.Session{
		{Name: "a", Dir: "/repos/foo", Branch: "main"},  // drift → main→feature-x; worktree
		{Name: "b", Dir: "/repos/foo", Branch: "main"},  // same dir, cached (no 2nd call)
		{Name: "c", Dir: "/repos/bar", Branch: "main"},  // no drift, not worktree
		{Name: "d", Dir: "/repos/foo", Branch: ""},      // no start branch → no drift, but worktree
		{Name: "e", Dir: "/repos/gone", Branch: "main"}, // unresolvable → no drift, not worktree
	}
	annotateSessions(sessions, resolve)

	for _, s := range sessions {
		wantDrift := s.Name == "a" || s.Name == "b"
		if s.BranchDrift != wantDrift {
			t.Errorf("%s: BranchDrift = %v, want %v", s.Name, s.BranchDrift, wantDrift)
		}
		wantWt := s.Dir == "/repos/foo"
		if s.Worktree != wantWt {
			t.Errorf("%s: Worktree = %v, want %v", s.Name, s.Worktree, wantWt)
		}
	}
	// foo, bar, gone = 3 distinct dirs resolved once each (worktree needs the lookup
	// even for the "" start-branch session, so no short-circuit skips it).
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

	if got := gitx.GitCurrentBranch(dir); got != "main" {
		t.Fatalf("gitx.GitCurrentBranch(fresh) = %q, want main", got)
	}

	// Detach HEAD at the current commit → sentinel, not a branch name.
	sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "checkout", strings.TrimSpace(string(sha))).CombinedOutput(); err != nil {
		t.Fatalf("detach: %v: %s", err, out)
	}
	if got := gitx.GitCurrentBranch(dir); got != "(detached)" {
		t.Fatalf("gitx.GitCurrentBranch(detached) = %q, want (detached)", got)
	}

	// A path that isn't a git working tree resolves to "".
	if got := gitx.GitCurrentBranch(t.TempDir()); got != "" {
		t.Fatalf("gitx.GitCurrentBranch(non-git) = %q, want empty", got)
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
	dir, err := gitx.EnsureWorktree(parent, "main", "feat-x", "")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree new: %v", err)
	}
	if want := filepath.Join(home, "repos", "app@feat-x"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if b := gitx.GitCurrentBranch(dir); b != "feat-x" {
		t.Fatalf("worktree branch = %q, want feat-x", b)
	}

	// Idempotent: same call returns the same path without error.
	if again, err := gitx.EnsureWorktree(parent, "main", "feat-x", ""); err != nil || again != dir {
		t.Fatalf("gitx.EnsureWorktree reuse = (%q,%v), want (%q,nil)", again, err, dir)
	}

	// Existing branch, no new branch → ~/repos/app@feature on that branch.
	fdir, err := gitx.EnsureWorktree(parent, "feature", "", "")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree existing: %v", err)
	}
	if want := filepath.Join(home, "repos", "app@feature"); fdir != want {
		t.Fatalf("existing dir = %q, want %q", fdir, want)
	}
	if b := gitx.GitCurrentBranch(fdir); b != "feature" {
		t.Fatalf("existing worktree branch = %q, want feature", b)
	}

	// A slash in the new branch is sanitized in the folder but kept as the ref.
	sdir, err := gitx.EnsureWorktree(parent, "main", "fix/bug-1", "")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree slash: %v", err)
	}
	if want := filepath.Join(home, "repos", "app@fix-bug-1"); sdir != want {
		t.Fatalf("slash dir = %q, want %q", sdir, want)
	}
	if b := gitx.GitCurrentBranch(sdir); b != "fix/bug-1" {
		t.Fatalf("slash worktree branch = %q, want fix/bug-1", b)
	}

	// folderSeg lets the folder diverge from the branch: branch temp/abc in a wip-abc
	// folder (the auto-name convention).
	wdir, err := gitx.EnsureWorktree(parent, "main", "temp/abc", "wip-abc")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree folderSeg: %v", err)
	}
	if want := filepath.Join(home, "repos", "app@wip-abc"); wdir != want {
		t.Fatalf("folderSeg dir = %q, want %q", wdir, want)
	}
	if b := gitx.GitCurrentBranch(wdir); b != "temp/abc" {
		t.Fatalf("folderSeg worktree branch = %q, want temp/abc", b)
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

	wt, err := gitx.EnsureWorktree(parent, "main", "feat-x", "")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree: %v", err)
	}

	if !gitx.IsLinkedWorktree(wt) {
		t.Errorf("gitx.IsLinkedWorktree(worktree) = false, want true")
	}
	if gitx.IsLinkedWorktree(parent) {
		t.Errorf("gitx.IsLinkedWorktree(parent) = true, want false")
	}
	if got := gitx.WorktreeParent(wt); got != parent {
		t.Errorf("gitx.WorktreeParent = %q, want %q", got, parent)
	}
	// `git worktree list` reports the whole set regardless of which worktree it runs
	// from, so the count is 1 (one linked worktree) whether asked from parent or wt.
	// The delete handler only calls this on the main working copy, where 1 => refuse.
	if got := gitx.LinkedWorktreeCount(parent); got != 1 {
		t.Errorf("gitx.LinkedWorktreeCount(parent) = %d, want 1", got)
	}

	// Forced removal (the handler's core) drops the worktree and its registry entry.
	if out, err := exec.Command("git", "-C", parent, "worktree", "remove", "--force", wt).CombinedOutput(); err != nil {
		t.Fatalf("worktree remove: %v: %s", err, out)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Errorf("worktree dir still exists after remove")
	}
	if got := gitx.LinkedWorktreeCount(parent); got != 0 {
		t.Errorf("gitx.LinkedWorktreeCount after remove = %d, want 0", got)
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
	dirty, err := gitx.EnsureWorktree(parent, "main", "dirty-x", "")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree dirty: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirty, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitx.MaybePruneWorktree(dirty)
	if !gitx.IsGitRepo(dirty) {
		t.Errorf("dirty worktree was auto-removed; should be kept")
	}

	// Referenced worktree: clean, but a session meta points at it → kept.
	ref, err := gitx.EnsureWorktree(parent, "main", "ref-x", "")
	if err != nil {
		t.Fatalf("gitx.EnsureWorktree ref: %v", err)
	}
	session.WriteMeta(session.Meta{Name: "zz", Dir: ref, Kind: "shell"})
	gitx.MaybePruneWorktree(ref)
	if !gitx.IsGitRepo(ref) {
		t.Errorf("referenced worktree was auto-removed; should be kept while a meta points at it")
	}

	// A non-worktree (the parent) is never touched.
	gitx.MaybePruneWorktree(parent)
	if !gitx.IsGitRepo(parent) {
		t.Errorf("parent working copy was removed; gitx.MaybePruneWorktree must ignore non-worktrees")
	}
}

// TestUpdateSessionStartBranch verifies an intentional branch rename rewrites the
// recorded start branch for sessions in that working copy (so it isn't later seen as
// drift), while leaving other dirs and pre-existing ("") metas untouched.
func TestUpdateSessionStartBranch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	dir := filepath.Join(home, "repos", "app@wip-x")
	session.WriteMeta(session.Meta{Name: "a", Dir: dir, Branch: "wip-x"})
	session.WriteMeta(session.Meta{Name: "b", Dir: filepath.Join(dir, "sub"), Branch: "wip-x"}) // subdir
	session.WriteMeta(session.Meta{Name: "c", Dir: dir, Branch: ""})                            // pre-existing
	session.WriteMeta(session.Meta{Name: "d", Dir: filepath.Join(home, "repos", "other"), Branch: "main"})

	session.UpdateStartBranch(dir, "feat/login")

	get := func(n string) session.Meta { m, _ := session.ReadMeta(n); return m }
	if b := get("a").Branch; b != "feat/login" {
		t.Errorf("a.Branch = %q, want feat/login", b)
	}
	if b := get("b").Branch; b != "feat/login" {
		t.Errorf("b.Branch (subdir) = %q, want feat/login", b)
	}
	if b := get("c").Branch; b != "" {
		t.Errorf("c.Branch = %q, want empty (pre-existing untouched)", b)
	}
	if b := get("d").Branch; b != "main" {
		t.Errorf("d.Branch (other dir) = %q, want main (untouched)", b)
	}
}

// TestCleanBranchName checks the LLM branch-name sanitizer produces git-safe kebab-case
// from chatty/edge replies (quotes, prefixes, casing, punctuation, over-length, empty).
func TestCleanBranchName(t *testing.T) {
	cases := map[string]string{
		"Fix the login redirect bug": "fix-the-login-redirect-bug",
		"\"feature/login-redirect\"": "feature-login-redirect",
		"Add   Rate  Limiting!!":     "add-rate-limiting",
		"first line\nsecond line":    "first-line",
		"日本語のみ":                      "", // no ASCII word chars → empty
		"----":                       "",
		"a-very-long-branch-name-that-should-definitely-exceed-forty-chars": "a-very-long-branch-name-that-should-defi", // capped at 40 runes
	}
	for in, want := range cases {
		if got := cleanBranchName(in); got != want {
			t.Errorf("cleanBranchName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBranchNameStatus checks collision detection distinguishes a local branch, a
// remote-only (past) branch, and an unused name — the signal the worktree/rename guards
// use to refuse a name that would silently create a divergent branch.
func TestBranchNameStatus(t *testing.T) {
	if _, err := execLookPathGit(); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	run := func(dir string, args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// remote: has local branches main + feature, no remotes configured.
	remote := filepath.Join(base, "remote")
	gitInit(t, remote) // main + feature
	// work: a clone → main is local, feature is remote-only (origin/feature).
	work := filepath.Join(base, "work")
	run(base, "clone", remote, work)

	// Local branch (in the source repo).
	if l, r := gitx.BranchNameStatus(remote, "feature"); !l || r {
		t.Errorf("feature (local): local=%v remote=%v, want local=true remote=false", l, r)
	}
	// Remote-only branch (in the clone): the silent-divergence trap.
	if l, r := gitx.BranchNameStatus(work, "feature"); l || !r {
		t.Errorf("feature (remote-only in clone): local=%v remote=%v, want local=false remote=true", l, r)
	}
	// Unused name.
	if l, r := gitx.BranchNameStatus(work, "brand-new-name"); l || r {
		t.Errorf("brand-new-name: local=%v remote=%v, want both false", l, r)
	}
}
