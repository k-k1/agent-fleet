package gitx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// gitShim puts a `git` on PATH that appends a line per invocation to a log and then runs the
// real one, so a test can count the git processes a call started.
func gitShim(t *testing.T) (count func() int) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := fmt.Sprintf("#!/bin/sh\necho x >> %q\nexec %q \"$@\"\n", log, real)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "\n")
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir,
		"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false",
		"-c", "protocol.file.allow=always"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// cacheFixture is a bare "origin", a clone of it (the copy under test) and a second clone
// used to push from, so fetch/ahead/behind can be driven.
type cacheFixture struct {
	origin, copyDir, other string
	global                 gitGlobals
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(os.Getenv("HOME"), ".gitconfig"))
	root := t.TempDir()
	f := &cacheFixture{origin: filepath.Join(root, "origin.git"), copyDir: filepath.Join(root, "copy"), other: filepath.Join(root, "other")}
	mustGit(t, root, "init", "--bare", "-b", "main", f.origin)
	mustGit(t, root, "clone", f.origin, f.other)
	writeFile(t, filepath.Join(f.other, "a.txt"), "a\n")
	mustGit(t, f.other, "add", ".")
	mustGit(t, f.other, "commit", "-m", "first")
	mustGit(t, f.other, "push", "origin", "HEAD:main")
	mustGit(t, root, "clone", f.origin, f.copyDir)
	f.global = globalConfigPaths()
	return f
}

func (f *cacheFixture) fresh(t *testing.T, dir string) (RepoStatus, repoIdent) {
	t.Helper()
	st, err := GitStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := computeIdent(dir, nil)
	return st, id
}

// check asserts the cached answer equals a from-scratch one.
func (f *cacheFixture) check(t *testing.T, dir, what string) {
	t.Helper()
	gotSt, gotID := listCache.inspect(dir, f.global)
	wantSt, wantID := f.fresh(t, dir)
	if gotSt != wantSt {
		t.Errorf("%s: status = %+v, want %+v", what, gotSt, wantSt)
	}
	if gotID != wantID {
		t.Errorf("%s: ident = %+v, want %+v", what, gotID, wantID)
	}
}

func TestRepoCacheWarmCallStartsNoGit(t *testing.T) {
	count := gitShim(t)
	f := newCacheFixture(t)
	listCache.inspect(f.copyDir, f.global)
	cold := count()
	if cold == 0 {
		t.Fatal("cold call started no git; the shim is not in the path")
	}
	listCache.inspect(f.copyDir, f.global)
	listCache.inspect(f.copyDir, f.global)
	if got := count(); got != cold {
		t.Errorf("warm calls started %d git processes, want 0", got-cold)
	}
}

// Each mutation below is something a user or the Agent does to a working copy. After it, the
// cached answer must equal a fresh one — never the answer from before.
func TestRepoCacheInvalidatesOnGitChanges(t *testing.T) {
	f := newCacheFixture(t)
	d := f.copyDir
	f.check(t, d, "initial")

	mustGit(t, d, "checkout", "-b", "feature/x")
	f.check(t, d, "checkout -b")
	if st, _ := listCache.inspect(d, f.global); st.Branch != "feature/x" {
		t.Fatalf("branch = %q after checkout", st.Branch)
	}

	writeFile(t, filepath.Join(d, "b.txt"), "b\n")
	mustGit(t, d, "add", "b.txt")
	f.check(t, d, "add")
	mustGit(t, d, "commit", "-m", "b")
	f.check(t, d, "commit")

	mustGit(t, d, "push", "-u", "origin", "feature/x")
	f.check(t, d, "push -u (upstream appears)")
	writeFile(t, filepath.Join(d, "c.txt"), "c\n")
	mustGit(t, d, "add", "c.txt")
	mustGit(t, d, "commit", "-m", "c")
	if st, _ := listCache.inspect(d, f.global); st.Ahead != 1 {
		t.Fatalf("ahead = %d after a local commit, want 1", st.Ahead)
	}
	f.check(t, d, "commit ahead")
	mustGit(t, d, "push")
	if st, _ := listCache.inspect(d, f.global); st.Ahead != 0 {
		t.Fatalf("ahead = %d after push, want 0", st.Ahead)
	}

	// Someone else pushes to the same branch; only a fetch makes it visible here.
	mustGit(t, f.other, "fetch", "origin")
	mustGit(t, f.other, "checkout", "-b", "feature/x", "origin/feature/x")
	writeFile(t, filepath.Join(f.other, "d.txt"), "d\n")
	mustGit(t, f.other, "add", ".")
	mustGit(t, f.other, "commit", "-m", "d")
	mustGit(t, f.other, "push")
	f.check(t, d, "remote moved, not fetched")
	mustGit(t, d, "fetch", "origin")
	if st, _ := listCache.inspect(d, f.global); st.Behind != 1 {
		t.Fatalf("behind = %d after fetch, want 1", st.Behind)
	}
	f.check(t, d, "fetch")
	// A fetch that packs refs (gc) moves them to packed-refs.
	mustGit(t, d, "pack-refs", "--all")
	f.check(t, d, "pack-refs")
	mustGit(t, d, "merge", "--ff-only", "origin/feature/x")
	f.check(t, d, "merge")

	mustGit(t, d, "checkout", "--detach")
	f.check(t, d, "detach")
	mustGit(t, d, "remote", "set-url", "origin", "https://github.com/acme/widgets.git")
	f.check(t, d, "set-url")
	if _, id := listCache.inspect(d, f.global); id.provider != "github" || id.remotePath != "acme/widgets" {
		t.Fatalf("ident after set-url = %+v", id)
	}
	// A global insteadOf rewrites what `remote get-url` reports.
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"), "[url \"https://gitlab.com/\"]\n\tinsteadOf = https://github.com/\n")
	f.check(t, d, "global insteadOf")
	if _, id := listCache.inspect(d, f.global); id.provider != "gitlab" {
		t.Fatalf("provider = %q after a global insteadOf, want gitlab", id.provider)
	}
}

func TestRepoCacheDirtyFollowsTheTreeAfterMaxAge(t *testing.T) {
	f := newCacheFixture(t)
	d := f.copyDir
	f.check(t, d, "clean")
	old := maxStatusAge
	t.Cleanup(func() { maxStatusAge = old })
	maxStatusAge = 0
	writeFile(t, filepath.Join(d, "a.txt"), "edited\n") // no git metadata moves
	if st, _ := listCache.inspect(d, f.global); !st.Dirty {
		t.Fatal("dirty = false after editing a tracked file")
	}
	writeFile(t, filepath.Join(d, "new.txt"), "x\n")
	f.check(t, d, "untracked")
}

func TestRepoCacheLinkedWorktree(t *testing.T) {
	f := newCacheFixture(t)
	wt := filepath.Join(filepath.Dir(f.copyDir), "copy@wt")
	f.check(t, f.copyDir, "parent before")
	mustGit(t, f.copyDir, "worktree", "add", "-b", "temp/wt", wt)
	f.check(t, wt, "worktree add")
	if _, id := listCache.inspect(wt, f.global); !id.worktree || id.parent != "copy" {
		t.Fatalf("worktree ident = %+v", id)
	}
	in := listCache.integration(wt, f.copyDir, "main", f.global)
	if want := GitWorktreeIntegration(f.copyDir, wt, "main"); in != want {
		t.Fatalf("integration = %+v, want %+v", in, want)
	}
	if in.Relation != "same" {
		t.Fatalf("relation = %q, want same", in.Relation)
	}
	writeFile(t, filepath.Join(wt, "w.txt"), "w\n")
	mustGit(t, wt, "add", ".")
	f.check(t, wt, "worktree add (index only; .git is a file there)")
	if st, _ := listCache.inspect(wt, f.global); st.Dirty != true {
		t.Fatal("staged change not reported dirty")
	}
	mustGit(t, wt, "commit", "-m", "w")
	if got := listCache.integration(wt, f.copyDir, "main", f.global); got.Relation != "unmerged" || got.WorktreeUnique != 1 {
		t.Fatalf("integration after a worktree commit = %+v", got)
	}
	// The parent's upstream moves: the worktree is now diverged from it.
	mustGit(t, f.other, "pull", "origin", "main")
	writeFile(t, filepath.Join(f.other, "o.txt"), "o\n")
	mustGit(t, f.other, "add", ".")
	mustGit(t, f.other, "commit", "-m", "o")
	mustGit(t, f.other, "push", "origin", "HEAD:main")
	mustGit(t, f.copyDir, "fetch", "origin")
	got := listCache.integration(wt, f.copyDir, "main", f.global)
	if want := GitWorktreeIntegration(f.copyDir, wt, "main"); got != want || got.Relation != "diverged" {
		t.Fatalf("integration after the parent's upstream moved = %+v, want %+v (diverged)", got, want)
	}
	// Remove and re-add under the same folder name: a new generation, never the old answer.
	mustGit(t, f.copyDir, "worktree", "remove", "--force", wt)
	mustGit(t, f.copyDir, "worktree", "add", "-b", "temp/wt2", wt)
	f.check(t, wt, "worktree re-added")
	if st, _ := listCache.inspect(wt, f.global); st.Branch != "temp/wt2" {
		t.Fatalf("branch = %q after re-adding the worktree", st.Branch)
	}
}

func TestRepoCacheSingleFlight(t *testing.T) {
	count := gitShim(t)
	f := newCacheFixture(t)
	listCache.inspect(f.copyDir, f.global)
	listCache.prune(map[string]bool{})

	before := count()
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); listCache.inspect(f.copyDir, f.global) }()
	}
	wg.Wait()
	cold := count() - before
	listCache.prune(map[string]bool{})
	b2 := count()
	listCache.inspect(f.copyDir, f.global)
	oneCold := count() - b2
	if cold != oneCold {
		t.Errorf("24 concurrent cold calls started %d git processes, one cold call starts %d", cold, oneCold)
	}
}

func TestRepoCacheUncacheableConfigIsComputedLive(t *testing.T) {
	count := gitShim(t)
	f := newCacheFixture(t)
	mustGit(t, f.copyDir, "config", "include.path", "extra.cfg") // an include the fingerprint cannot follow
	listCache.inspect(f.copyDir, f.global)
	b := count()
	listCache.inspect(f.copyDir, f.global)
	if count() == b {
		t.Error("a copy with a config include was served from the cache")
	}
}

func TestRepoCacheIsBounded(t *testing.T) {
	c := &repoCache{entries: map[string]*copyEntry{}, sem: make(chan struct{}, 1)}
	for i := 0; i < maxCacheEntries+50; i++ {
		c.entry(fmt.Sprintf("/x/%d", i))
	}
	if n := len(c.entries); n > maxCacheEntries {
		t.Errorf("%d entries, cap is %d", n, maxCacheEntries)
	}
	c.prune(map[string]bool{"/x/60": true})
	if len(c.entries) > 1 {
		t.Errorf("prune left %d entries, want at most 1", len(c.entries))
	}
}

func TestUpstreamRef(t *testing.T) {
	cfg := "[remote \"origin\"]\n\turl = x\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n"
	cases := []struct {
		name, cfg, branch, want string
		ok                      bool
	}{
		{"tracked", cfg, "main", "refs/remotes/origin/main", true},
		{"no upstream", cfg, "other", "", true},
		{"local upstream", "[branch \"a\"]\n\tremote = .\n\tmerge = refs/heads/b\n", "a", "refs/heads/b", true},
		{"include", cfg + "[include]\n\tpath = x\n", "main", "", false},
		{"custom refspec", strings.Replace(cfg, "refs/remotes/origin/*", "refs/remotes/o/*", 1), "main", "", false},
		{"other-case branch", strings.Replace(cfg, `branch "main"`, `branch "Main"`, 1), "main", "", true},
		{"reftable", cfg + "[extensions]\n\trefStorage = reftable\n", "main", "", false},
	}
	for _, c := range cases {
		got, ok := upstreamRef(c.cfg, c.branch)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: upstreamRef = %q,%v want %q,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// Config that git reads but the fingerprint does not watch must not be cached: an include
// file, a per-worktree config and a global [branch] section all change what git reports.
func TestRepoCacheFollowsConfigTheFingerprintCannotWatch(t *testing.T) {
	f := newCacheFixture(t)
	d := f.copyDir
	extra := filepath.Join(filepath.Dir(d), "extra.cfg")

	mustGit(t, d, "remote", "set-url", "origin", "https://github.com/acme/one.git")
	writeFile(t, extra, "[url \"https://gitlab.com/\"]\n\tinsteadOf = https://github.com/\n")
	mustGit(t, d, "config", "include.path", extra)
	f.check(t, d, "include, first")
	writeFile(t, extra, "[url \"https://bitbucket.org/\"]\n\tinsteadOf = https://github.com/\n")
	f.check(t, d, "include, changed only in the included file")
	if _, id := listCache.inspect(d, f.global); id.provider != "bitbucket" {
		t.Fatalf("ident = %+v after the included file changed", id)
	}
}

func TestRepoCacheFollowsPerWorktreeConfig(t *testing.T) {
	f := newCacheFixture(t)
	wt := filepath.Join(filepath.Dir(f.copyDir), "copy@wt")
	mustGit(t, f.copyDir, "config", "extensions.worktreeConfig", "true")
	mustGit(t, f.copyDir, "worktree", "add", "-b", "temp/wt", wt)
	mustGit(t, wt, "remote", "set-url", "origin", "https://github.com/acme/one.git")
	mustGit(t, wt, "config", "--worktree", "url.https://gitlab.com/.insteadOf", "https://github.com/")
	f.check(t, wt, "config.worktree insteadOf, first")
	mustGit(t, wt, "config", "--worktree", "url.https://bitbucket.org/.insteadOf", "https://github.com/")
	mustGit(t, wt, "config", "--worktree", "--unset", "url.https://gitlab.com/.insteadOf")
	f.check(t, wt, "config.worktree insteadOf, changed")
}

func TestRepoCacheFollowsAGlobalUpstream(t *testing.T) {
	f := newCacheFixture(t)
	d := f.copyDir
	// The upstream is named only in ~/.gitconfig; the repo config has no [branch] section.
	mustGit(t, d, "config", "--remove-section", "branch.main")
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"), "[branch \"main\"]\n\tremote = origin\n\tmerge = refs/heads/main\n")
	f.global = globalConfigPaths()
	mustGit(t, d, "fetch", "origin")
	f.check(t, d, "warm after the first fetch (FETCH_HEAD exists now)")
	writeFile(t, filepath.Join(f.other, "n.txt"), "n\n")
	mustGit(t, f.other, "add", ".")
	mustGit(t, f.other, "commit", "-m", "n")
	mustGit(t, f.other, "push", "origin", "HEAD:main")
	mustGit(t, d, "fetch", "origin")
	f.check(t, d, "fetch moved the globally named upstream")
	if st, _ := listCache.inspect(d, f.global); st.Behind != 1 {
		t.Fatalf("behind = %d, want 1", st.Behind)
	}
}

// A git call that failed is not an answer: it must not be remembered for minutes.
func TestRepoCacheDoesNotRememberAFailedGit(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	f := newCacheFixture(t)
	mustGit(t, f.copyDir, "remote", "set-url", "origin", "https://github.com/acme/one.git")
	dir := t.TempDir()
	flag := filepath.Join(dir, "failed")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *get-url*) if [ ! -e %q ]; then : > %q; exit 1; fi;; esac\nexec %q \"$@\"\n", flag, flag, real)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, id := listCache.inspect(f.copyDir, f.global); id.provider != "" {
		t.Fatalf("the shim did not fail the first get-url: %+v", id)
	}
	if _, id := listCache.inspect(f.copyDir, f.global); id.provider != "github" || id.remotePath != "acme/one" {
		t.Fatalf("a failed get-url was cached: %+v", id)
	}
	// A remote that is simply not configured IS an answer and is remembered.
	mustGit(t, f.copyDir, "remote", "remove", "origin")
	f.check(t, f.copyDir, "no origin")
}

// The entry cap is a performance bound: with fewer slots than copies, evictions between the
// two phases of a listing must not change any answer (a lost parent dir once became "").
func TestFillGitFactsSurvivesEvictionBetweenPhases(t *testing.T) {
	f := newCacheFixture(t)
	root := filepath.Dir(f.copyDir)
	repos := []Repo{{Name: "copy", Path: f.copyDir}}
	for _, n := range []string{"a", "b", "c", "d"} {
		wt := filepath.Join(root, "copy@"+n)
		mustGit(t, f.copyDir, "worktree", "add", "-b", "temp/"+n, wt)
		repos = append(repos, Repo{Name: "copy@" + n, Path: wt})
	}
	idx := []int{0, 1, 2, 3, 4}
	c := &repoCache{entries: map[string]*copyEntry{}, sem: make(chan struct{}, 2), max: 2}
	fillGitFacts(c, repos, idx, f.global)
	if len(c.entries) > 2 {
		t.Fatalf("%d entries, cap is 2", len(c.entries))
	}
	for _, r := range repos[1:] {
		want := GitWorktreeIntegration(f.copyDir, r.Path, repos[0].Branch)
		if r.Integration == nil || *r.Integration != want || want.Relation != "same" {
			t.Errorf("%s: integration = %+v, want %+v", r.Name, r.Integration, want)
		}
		if r.Parent != "copy" {
			t.Errorf("%s: parent = %q", r.Name, r.Parent)
		}
	}
}

// A fetch refspec in the global config remaps where the upstream lives; the fingerprint
// would keep watching refs/remotes/origin/*.
func TestRepoCacheFollowsAGlobalFetchRefspec(t *testing.T) {
	f := newCacheFixture(t)
	d := f.copyDir
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"), "[remote \"origin\"]\n\tfetch = +refs/heads/*:refs/remotes/custom/*\n")
	f.global = globalConfigPaths()
	mustGit(t, d, "fetch", "origin")
	f.check(t, d, "warm after the first fetch")
	writeFile(t, filepath.Join(f.other, "n.txt"), "n\n")
	mustGit(t, f.other, "add", ".")
	mustGit(t, f.other, "commit", "-m", "n")
	mustGit(t, f.other, "push", "origin", "HEAD:main")
	// --refmap= stops git's opportunistic update of origin/main, moving only the real upstream.
	mustGit(t, d, "fetch", "--refmap=", "origin", "main:refs/remotes/custom/main")
	f.check(t, d, "fetch moved the remapped upstream only")
}
