package memoryx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// agentMemTestEnv's main clone with one linked worktree is built once per test binary and
// copied into each test: building it costs three git processes per test (init, commit,
// worktree add; the worktree checkout is the heavy one), and ~180 tests use it (measured: the
// package went from 105 s to 90-93 s serially).
//
// A copy differs from a fresh build only in what git itself never compares: the commit's
// timestamp and hash, file mtimes and index stat data (git refreshes those), and file modes
// (CopyFS writes 0666 / 0777 minus umask, plus the source's execute bits). The template's
// directory names match the test's ("demo", "demo@feature-x"), so git's worktree name and the
// relative commondir link stay valid; the two absolute links between clone and worktree are
// rewritten for the copy.
var (
	agentMemCloneOnce sync.Once
	agentMemCloneDir  string // holds demo/ and demo@feature-x/; empty when it could not be built
	agentMemCloneErr  error
)

const (
	agentMemCloneName = "demo"
	agentMemWtName    = "demo@feature-x"
)

// agentMemGit runs a fixture git command, isolated from the user's and the system's config.
func agentMemGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return nil
}

// agentMemBuildClone builds the clone and its worktree at clone and wt.
func agentMemBuildClone(clone, wt string) error {
	if err := os.MkdirAll(clone, 0o700); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "--quiet", "-b", "main"},
		{"commit", "--quiet", "--allow-empty", "-m", "init"},
		{"worktree", "add", "--quiet", "-b", "feature-x", wt},
	} {
		if err := agentMemGit(clone, args...); err != nil {
			return err
		}
	}
	return nil
}

// agentMemCloneTemplate returns the template directory, building it on first use inside the
// test guard's scratch root, which the guard removes when the binary exits; each process (a
// sharded child included) builds its own there and never reads another's. Without a guard root
// it returns "" and every test builds its own clone.
func agentMemCloneTemplate() (string, error) {
	agentMemCloneOnce.Do(func() {
		root := os.Getenv("AF_TESTGUARD_ROOT")
		if root == "" {
			return
		}
		dir, err := os.MkdirTemp(root, "memx-clone-template-")
		if err != nil {
			agentMemCloneErr = err
			return
		}
		if err := agentMemBuildClone(filepath.Join(dir, agentMemCloneName), filepath.Join(dir, agentMemWtName)); err != nil {
			agentMemCloneErr = err
			return
		}
		agentMemCloneDir = dir
	})
	return agentMemCloneDir, agentMemCloneErr
}

// agentMemCloneFixture places the clone at clone and its worktree at wt, siblings named like the
// template's.
func agentMemCloneFixture(t *testing.T, clone, wt string) {
	t.Helper()
	if filepath.Base(clone) != agentMemCloneName || filepath.Base(wt) != agentMemWtName || filepath.Dir(clone) != filepath.Dir(wt) {
		t.Fatalf("clone fixture: %s and %s are not the template's layout", clone, wt)
	}
	tpl, err := agentMemCloneTemplate()
	if err != nil {
		t.Fatalf("clone template: %v", err)
	}
	if tpl == "" {
		if err := agentMemBuildClone(clone, wt); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.CopyFS(filepath.Dir(clone), os.DirFS(tpl)); err != nil {
		t.Fatalf("copy clone template: %v", err)
	}
	// The only absolute paths git wrote: the worktree's pointer to its admin directory and the
	// admin directory's pointer back to the worktree.
	admin := filepath.Join(clone, ".git", "worktrees", agentMemWtName)
	for p, s := range map[string]string{
		filepath.Join(wt, ".git"):      "gitdir: " + admin + "\n",
		filepath.Join(admin, "gitdir"): filepath.Join(wt, ".git") + "\n",
	} {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatalf("relink clone fixture: %v", err)
		}
	}
}

// agentMemTreeMentions lists the files under dir whose content contains s.
func agentMemTreeMentions(t *testing.T, dir, s string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err == nil && strings.Contains(string(b), s) {
			hits = append(hits, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func agentMemGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// agentMemRelFiles lists the files under dir, relative to it, without the loose objects: the
// commit's name depends on its timestamp (count-objects compares how many there are).
func agentMemRelFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			if !strings.HasPrefix(rel, filepath.Join("demo", ".git", "objects")+"/") {
				files = append(files, rel)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Two copies share nothing: each worktree is linked to its own clone, nothing in either names
// the template or the other copy, and a commit in one is seen by neither the other nor the
// template.
func TestAgentMemCloneFixtureCopiesAreIndependent(t *testing.T) {
	memoryTestGitForeground(t)
	tpl, err := agentMemCloneTemplate()
	if err != nil || tpl == "" {
		t.Fatalf("clone template = %q, %v; want one inside the test guard's root", tpl, err)
	}
	var clones, wts []string
	for range 2 {
		repos := filepath.Join(t.TempDir(), "repos")
		clone, wt := filepath.Join(repos, agentMemCloneName), filepath.Join(repos, agentMemWtName)
		agentMemCloneFixture(t, clone, wt)
		clones, wts = append(clones, clone), append(wts, wt)
	}
	for i := range 2 {
		other := filepath.Dir(clones[1-i])
		for _, s := range []string{tpl, other} {
			if hits := agentMemTreeMentions(t, filepath.Dir(clones[i]), s); len(hits) > 0 {
				t.Errorf("copy %d names %s in %v", i, s, hits)
			}
		}
		if got := agentMemGitOut(t, wts[i], "rev-parse", "--path-format=absolute", "--git-common-dir"); got != filepath.Join(clones[i], ".git") {
			t.Errorf("worktree %d's common dir = %s, want its own clone's", i, got)
		}
		if got := agentMemGitOut(t, clones[i], "worktree", "list", "--porcelain"); !strings.Contains(got, "worktree "+wts[i]+"\n") || strings.Contains(got, "prunable") {
			t.Errorf("clone %d's worktrees:\n%s", i, got)
		}
		if got := agentMemGitOut(t, wts[i], "status", "--porcelain"); got != "" {
			t.Errorf("worktree %d not clean: %q", i, got)
		}
	}
	tplHead := agentMemGitOut(t, filepath.Join(tpl, agentMemCloneName), "rev-parse", "feature-x")
	agentMemGitOut(t, wts[0], "commit", "--quiet", "--allow-empty", "-m", "only in copy 0")
	if got := agentMemGitOut(t, clones[1], "rev-parse", "feature-x"); got != tplHead {
		t.Errorf("copy 1's feature-x moved to %s with a commit made in copy 0", got)
	}
	if got := agentMemGitOut(t, filepath.Join(tpl, agentMemCloneName), "rev-parse", "feature-x"); got != tplHead {
		t.Errorf("the template's feature-x moved to %s with a commit made in a copy", got)
	}
	if got := agentMemGitOut(t, clones[0], "rev-parse", "feature-x"); got == tplHead {
		t.Error("copy 0's own clone does not see the commit made in its worktree")
	}
}

// A copy holds what a fresh build holds: the same files, config, refs and links (only the
// absolute paths differ, and they point into the copy), the same number of objects, no hooks.
func TestAgentMemCloneFixtureMatchesAFreshBuild(t *testing.T) {
	memoryTestGitForeground(t)
	freshRepos, copyRepos := filepath.Join(t.TempDir(), "repos"), filepath.Join(t.TempDir(), "repos")
	if err := agentMemBuildClone(filepath.Join(freshRepos, agentMemCloneName), filepath.Join(freshRepos, agentMemWtName)); err != nil {
		t.Fatal(err)
	}
	agentMemCloneFixture(t, filepath.Join(copyRepos, agentMemCloneName), filepath.Join(copyRepos, agentMemWtName))
	fresh, copied := agentMemRelFiles(t, freshRepos), agentMemRelFiles(t, copyRepos)
	if strings.Join(fresh, "\n") != strings.Join(copied, "\n") {
		t.Fatalf("files differ:\nfresh  %q\ncopied %q", fresh, copied)
	}
	for _, rel := range []string{
		"demo/.git/config", "demo/.git/HEAD", "demo/.git/description",
		"demo/.git/worktrees/demo@feature-x/HEAD", "demo/.git/worktrees/demo@feature-x/commondir",
		"demo/.git/worktrees/demo@feature-x/gitdir", "demo@feature-x/.git",
	} {
		f, _ := os.ReadFile(filepath.Join(freshRepos, rel))
		c, _ := os.ReadFile(filepath.Join(copyRepos, rel))
		if want := strings.ReplaceAll(string(f), freshRepos, copyRepos); string(c) != want {
			t.Errorf("%s = %q, want %q", rel, c, want)
		}
	}
	for _, repos := range []string{freshRepos, copyRepos} {
		if hooks, _ := os.ReadDir(filepath.Join(repos, "demo", ".git", "hooks")); len(hooks) > 0 {
			t.Errorf("%s has hooks: %v", repos, hooks)
		}
	}
	countObjects := func(repos string) string {
		var keep []string
		for _, l := range strings.Split(agentMemGitOut(t, filepath.Join(repos, "demo"), "count-objects", "-v"), "\n") {
			if !strings.HasPrefix(l, "size") { // bytes on disk, not content
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	if f, c := countObjects(freshRepos), countObjects(copyRepos); f != c {
		t.Errorf("objects differ:\nfresh\n%s\ncopied\n%s", f, c)
	}
}
