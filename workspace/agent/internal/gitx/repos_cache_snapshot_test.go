package gitx

import (
	"os"
	"path/filepath"
	"testing"
)

const insteadOfGitlab = "[url \"https://gitlab.com/\"]\n\tinsteadOf = https://github.com/\n"

// noUpstreamClone builds a clone whose branch has no upstream in the repo's own config.
// Only a global [branch "main"] section can give it one.
func noUpstreamClone(t *testing.T) (parent, wt string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	parent = filepath.Join(root, "copy")
	wt = filepath.Join(root, "copy@wt")
	mustGit(t, root, "init", "--bare", "-b", "main", origin)
	mustGit(t, root, "clone", origin, seed)
	writeFile(t, filepath.Join(seed, "a.txt"), "a\n")
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-m", "first")
	mustGit(t, seed, "push", "origin", "HEAD:main")
	mustGit(t, root, "clone", origin, parent)
	mustGit(t, parent, "config", "--unset", "branch.main.remote")
	mustGit(t, parent, "config", "--unset", "branch.main.merge")
	mustGit(t, parent, "worktree", "add", "-b", "temp/wt", wt)
	return parent, wt
}

const globalUpstream = "[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n"

// url.*.insteadOf in the snapshot rewrites `remote get-url` exactly as the real file does,
// and the git calls read the snapshot, not the home files.
func TestComputeIdentThroughSnapshotResolvesInsteadOf(t *testing.T) {
	home := isolateGlobalConfig(t)
	dir := filepath.Join(t.TempDir(), "copy")
	gitInit(t, dir)
	mustGit(t, dir, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	cfg := filepath.Join(home, ".gitconfig")
	writeFile(t, cfg, insteadOfGitlab)

	want, complete := computeIdent(dir, nil)
	if !complete || want.provider != "gitlab" {
		t.Fatalf("real files: %+v complete=%v, want gitlab", want, complete)
	}
	g := globalConfigPaths()
	defer g.done()
	if g.statusEnv == nil {
		t.Fatal("no snapshot")
	}
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	got, complete := computeIdent(dir, g.statusEnv)
	if !complete || got != want {
		t.Errorf("through the snapshot: %+v, want %+v", got, want)
	}
	if plain, _ := computeIdent(dir, nil); plain.provider != "github" {
		t.Errorf("without the snapshot the removed file still applied: %+v", plain)
	}
}

func TestIntegrationThroughSnapshotReadsGlobalConfig(t *testing.T) {
	home := isolateGlobalConfig(t)
	parent, wt := noUpstreamClone(t)
	cfg := filepath.Join(home, ".gitconfig")
	writeFile(t, cfg, globalUpstream)
	g := globalConfigPaths()
	defer g.done()
	if g.statusEnv == nil {
		t.Fatal("no snapshot")
	}
	want, _ := worktreeIntegration(parent, wt, "main", nil)
	if !want.TargetUpstream {
		t.Fatalf("real files: %+v, want the global [branch] upstream to apply", want)
	}
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := worktreeIntegration(parent, wt, "main", g.statusEnv)
	if got != want {
		t.Errorf("through the snapshot: %+v, want %+v", got, want)
	}
	if plain := GitWorktreeIntegration(parent, wt, "main"); plain.TargetUpstream {
		t.Errorf("without the snapshot the removed file still applied: %+v", plain)
	}
	if got := newTestCache().integration(wt, parent, "main", g); got != want {
		t.Errorf("cache integration = %+v, want %+v (its git did not get the snapshot)", got, want)
	}
}

// An edit between the request's read of the global config and its git run must not leave
// the old config's answer cached under the new config's fingerprint.
func TestSnapshotIdentNotCachedAcrossAnEdit(t *testing.T) {
	home := isolateGlobalConfig(t)
	dir := filepath.Join(t.TempDir(), "copy")
	gitInit(t, dir)
	mustGit(t, dir, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	cfg := filepath.Join(home, ".gitconfig")
	writeFile(t, cfg, insteadOfGitlab)
	c := newTestCache()
	g := globalConfigPaths()
	defer g.done()
	writeFile(t, cfg, "[user]\n\tname = x\n")
	if _, id := c.inspect(dir, g); id.provider != "gitlab" {
		t.Fatalf("old snapshot should answer gitlab, got %q", id.provider)
	}
	g2 := globalConfigPaths()
	defer g2.done()
	if _, id := c.inspect(dir, g2); id.provider != "github" {
		t.Errorf("after the edit provider = %q, want github (stale ident was cached)", id.provider)
	}
}

// The global config here is one the fingerprint can follow (insteadOf, no [branch]), so the
// integration slot is cacheable and only the snapshotCurrent guard keeps a result computed
// from the old snapshot out of it.
func TestSnapshotIntegrationNotCachedAcrossAnEdit(t *testing.T) {
	home := isolateGlobalConfig(t)
	parent, wt := noUpstreamClone(t)
	mustGit(t, parent, "config", "branch.main.remote", "origin")
	mustGit(t, parent, "config", "branch.main.merge", "refs/heads/main")
	cfg := filepath.Join(home, ".gitconfig")
	writeFile(t, cfg, insteadOfGitlab)
	cached := func(edit bool) bool {
		c := newTestCache()
		g := globalConfigPaths()
		defer g.done()
		if !g.simple || g.statusEnv == nil {
			t.Fatalf("setup: simple=%v snapshot=%v, want a cacheable config with a snapshot", g.simple, g.statusEnv != nil)
		}
		if edit {
			writeFile(t, cfg, "[user]\n\tname = x\n")
			defer writeFile(t, cfg, insteadOfGitlab)
		}
		if got := c.integration(wt, parent, "main", g); got.Relation == "unknown" {
			t.Fatalf("integration = %+v", got)
		}
		e := c.entry(wt)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.integ.valid
	}
	if !cached(false) {
		t.Fatal("control: an unedited config must be cached")
	}
	if cached(true) {
		t.Error("a result from the old snapshot was cached under the new fingerprint")
	}
}
