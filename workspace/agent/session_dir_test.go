package main

import (
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
