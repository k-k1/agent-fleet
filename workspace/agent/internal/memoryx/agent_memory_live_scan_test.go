package memoryx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func liveScanGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// liveScanFixture builds n clones, each with a linked worktree, and a fake node_modules of
// deepDirs directories inside the first three. It returns the walked directories.
func liveScanFixture(t *testing.T, repos string, n, deepDirs int) (copies []string) {
	t.Helper()
	for i := 0; i < n; i++ {
		clone := filepath.Join(repos, fmt.Sprintf("r%03d", i))
		memoryMkdirAll(t, clone)
		liveScanGit(t, clone, "init", "--quiet", "-b", "main")
		liveScanGit(t, clone, "commit", "--quiet", "--allow-empty", "-m", "init")
		wt := clone + "@wt"
		liveScanGit(t, clone, "worktree", "add", "--quiet", "-b", "wt", wt)
		copies = append(copies, clone, wt)
		if i < 3 {
			if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for j := 0; j < deepDirs; j++ {
				memoryMkdirAll(t, filepath.Join(clone, "node_modules", fmt.Sprintf("p%d", j), "lib"))
			}
		}
	}
	return copies
}

func TestAgentMemoryLiveSearchManyWorkingCopiesStaysFast(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	repos := filepath.Join(home, "repos")
	copies := liveScanFixture(t, repos, 40, 1500)
	// A clone inside a dependency store is not looked for.
	nested := filepath.Join(repos, "r000", "node_modules", "p1", "lib", "d", "e")
	memoryMkdirAll(t, nested)
	liveScanGit(t, nested, "init", "--quiet", "-b", "main")

	start := time.Now()
	live, complete := agentMemLiveProjects()
	t.Logf("live search: %v for %d working copies", time.Since(start), len(copies))
	if !complete {
		t.Fatal("the search must be complete")
	}
	if len(live) != 40 {
		t.Fatalf("found %d projects, want 40 (a clone and its worktree are one project; the one in node_modules is skipped)", len(live))
	}
	for _, c := range copies {
		if p := agentMemProjectFor(c); p == nil || live[p.ID] == nil {
			t.Fatalf("working copy %s is not live", c)
		}
	}
}

// What git does not ignore in a working copy is walked first and whole; ignored content after it,
// until agentMemLiveSoft. What that leaves unread makes the answer incomplete, never guessed empty.
func TestAgentMemoryLiveSearchWalksIgnoredContentLast(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	repos := filepath.Join(home, "repos")
	clone := filepath.Join(repos, "app")
	memoryMkdirAll(t, clone)
	liveScanGit(t, clone, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("node_modules/\n.worktrees/\nbuild-out/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(rel ...string) string {
		d := filepath.Join(append([]string{clone}, rel...)...)
		memoryMkdirAll(t, d)
		liveScanGit(t, d, "init", "--quiet", "-b", "main")
		return d
	}
	plain := mk("a", "b", "c", "hidden")
	// A clone inside a clone inside a clone, none of them ignored.
	mk("inner")
	chain := mk("inner", "x", "y", "deep")
	wt := mk(".worktrees", "x")
	ignoredDeep := mk("build-out", "p", "q", "r", "s", "deep")
	liveOf := func(live map[string]*agentMemProject, d string) bool {
		p := agentMemProjectFor(d)
		return p != nil && live[p.ID] != nil
	}
	live, complete := agentMemLiveProjects()
	if !complete {
		t.Fatal("incomplete")
	}
	for _, d := range []string{clone, plain, chain, wt, ignoredDeep} {
		if !liveOf(live, d) {
			t.Fatalf("%s is not live", d)
		}
	}
	old := agentMemLiveSoft
	agentMemLiveSoft = -time.Second // phase two never starts
	t.Cleanup(func() { agentMemLiveSoft = old })
	live, complete = agentMemLiveProjects()
	if complete {
		t.Fatal("ignored content was not read, yet the answer is complete")
	}
	for _, d := range []string{clone, plain, chain} {
		if !liveOf(live, d) {
			t.Fatalf("%s is lost with the partial answer", d)
		}
	}
	if liveOf(live, ignoredDeep) {
		t.Fatal("phase two ran")
	}
}

// git quotes names with non-ASCII characters unless asked for -z, and a quoted name matches nothing.
func TestAgentMemoryIgnoredDirsHandlesQuotedNames(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	clone := filepath.Join(home, "repos", "app")
	memoryMkdirAll(t, clone)
	liveScanGit(t, clone, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("*/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"plain", "build-é", " lead"} {
		memoryMkdirAll(t, filepath.Join(clone, d))
		if err := os.WriteFile(filepath.Join(clone, d, "file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ign, ok := agentMemIgnoredDirs(clone)
	if !ok {
		t.Fatal("git could not list")
	}
	for _, d := range []string{"plain", "build-é", " lead"} {
		if !ign[filepath.Join(clone, d)] {
			t.Errorf("%q is not in %v", d, ign)
		}
	}
}

// An id derived from the path (svn) differs between a symlink and its target: both are live.
func TestAgentMemoryLiveSearchKeepsPathDerivedIdsOfAliases(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	repos := filepath.Join(home, "repos")
	real := filepath.Join(repos, "z-real")
	memoryMkdirAll(t, filepath.Join(real, ".svn"))
	link := filepath.Join(repos, "a-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	live, _ := agentMemLiveProjects()
	for _, d := range []string{real, link} {
		if p := agentMemProjectFor(d); p == nil || live[p.ID] == nil {
			t.Fatalf("%s is not live", d)
		}
	}
}

// Every directory's identity is computed once per search, however many sessions point at it.
func TestAgentMemoryLiveSearchComputesIdentityOncePerDirectory(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	repos := filepath.Join(home, "repos")
	copies := liveScanFixture(t, repos, 6, 0)
	for i, c := range copies {
		session.WriteMeta(session.Meta{Name: fmt.Sprintf("claude-s%d", i), Dir: c, Kind: "claude"})
		session.WriteMeta(session.Meta{Name: fmt.Sprintf("codex-s%d", i), Dir: c + string(filepath.Separator), Kind: "codex"})
	}
	var mu sync.Mutex
	perDir, calls := map[string]int{}, map[string]int{}
	old := agentMemLiveRunner
	agentMemLiveRunner = func(dir string) agentMemGitRunner {
		return func(args ...string) (string, error) {
			if len(args) > 2 && args[2] == "--git-common-dir" {
				mu.Lock()
				perDir[dir]++
				mu.Unlock()
			}
			return gitx.Run(dir, args...)
		}
	}
	t.Cleanup(func() { agentMemLiveRunner = old })
	if _, complete := agentMemLiveProjects(); !complete {
		t.Fatal("incomplete")
	}
	if len(perDir) < len(copies) {
		t.Fatalf("only %d of %d directories resolved", len(perDir), len(copies))
	}
	for d, n := range perDir {
		if n != 1 {
			t.Errorf("identity of %s computed %d times", d, n)
		}
		// rev-parse (one process), worktree list for a linked worktree, remote get-url.
		if calls[d] > 3 {
			t.Errorf("%s took %d git processes", d, calls[d])
		}
	}
}

// The directory budget covers phase two as well: a tree of ignored content cannot run past it.
func TestAgentMemoryLiveSearchBudgetCoversIgnoredContent(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	app := filepath.Join(home, "repos", "app")
	memoryMkdirAll(t, app)
	liveScanGit(t, app, "init", "--quiet", "-b", "main")
	old := agentMemLiveBudget
	t.Cleanup(func() { agentMemLiveBudget = old })
	agentMemLiveBudget = 2 // ~/repos and app
	if _, complete := agentMemLiveProjects(); !complete {
		t.Fatal("positive control: two directories fit the budget")
	}
	if err := os.WriteFile(filepath.Join(app, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	memoryMkdirAll(t, filepath.Join(app, "ignored"))
	if _, complete := agentMemLiveProjects(); complete {
		t.Fatal("phase two read past the budget")
	}
}

// A working copy hidden only inside a dependency store is not looked for, and not looking does not
// make the answer incomplete; a copy under an ordinary ignored directory still is looked for.
func TestAgentMemoryLiveSearchSkipsDependencyStores(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	repos := filepath.Join(home, "repos")
	app := filepath.Join(repos, "app")
	memoryMkdirAll(t, app)
	liveScanGit(t, app, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(app, ".gitignore"), []byte("node_modules/\n.venv/\ncustom-out/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(parts ...string) string {
		d := filepath.Join(parts...)
		memoryMkdirAll(t, d)
		liveScanGit(t, d, "init", "--quiet", "-b", "main")
		return d
	}
	var stored []string
	for _, name := range []string{"node_modules", ".venv", "venv", ".gradle", ".pnpm-store", ".cache"} {
		stored = append(stored, mk(app, name, "pkg", "clone"))
		stored = append(stored, mk(repos, "plain", name, "clone")) // outside any working copy too
	}
	ordinary := mk(app, "custom-out", "a", "clone")
	liveOf := func(live map[string]*agentMemProject, d string) bool {
		p := agentMemProjectFor(d)
		return p != nil && live[p.ID] != nil
	}
	live, complete := agentMemLiveProjects()
	if !complete {
		t.Fatal("skipping a dependency store made the answer incomplete")
	}
	if !liveOf(live, app) || !liveOf(live, ordinary) {
		t.Fatal("a clone or a copy under an ordinary ignored directory was not found")
	}
	for _, d := range stored {
		if p := agentMemProjectFor(d); live[p.ID] != nil && p.Root == d {
			t.Errorf("the clone in %s was found: the store was entered", d)
		}
	}
	// The same copy under an ordinary ignored directory is unread once phase two does not run:
	// that stays incomplete.
	old := agentMemLiveSoft
	agentMemLiveSoft = -time.Second
	t.Cleanup(func() { agentMemLiveSoft = old })
	if _, complete := agentMemLiveProjects(); complete {
		t.Fatal("an unread ordinary ignored directory must keep the answer incomplete")
	}
}
