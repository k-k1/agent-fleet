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
	// A repository nested in a working copy's content is the working copy's own business.
	nested := filepath.Join(repos, "r000", "node_modules", "p1", "lib")
	liveScanGit(t, nested, "init", "--quiet", "-b", "main")

	start := time.Now()
	live, complete := agentMemLiveProjects()
	t.Logf("live search: %v for %d working copies", time.Since(start), len(copies))
	if !complete {
		t.Fatal("the search must be complete")
	}
	if len(live) != 40 {
		t.Fatalf("found %d projects, want 40 (a clone and its worktree are one project)", len(live))
	}
	for _, c := range copies {
		if p := agentMemProjectFor(c); p == nil || live[p.ID] == nil {
			t.Fatalf("working copy %s is not live", c)
		}
	}
}

// Below a working copy the walk reads a bounded number of levels, never its whole content.
func TestAgentMemoryLiveSearchDoesNotWalkAWorkingCopyWhole(t *testing.T) {
	home, _, _ := memoryTestEnv(t)
	repos := filepath.Join(home, "repos")
	clone := filepath.Join(repos, "app")
	memoryMkdirAll(t, clone)
	liveScanGit(t, clone, "init", "--quiet", "-b", "main")
	inner := filepath.Join(clone, "a", "b", "inner")
	memoryMkdirAll(t, inner)
	liveScanGit(t, inner, "init", "--quiet", "-b", "main")
	// Budget 3 = ~/repos, app and app/a. A walk that went on into app/a/b would be incomplete.
	oldB := agentMemLiveBudget
	agentMemLiveBudget = 3
	t.Cleanup(func() { agentMemLiveBudget = oldB })
	live, complete := agentMemLiveProjects()
	if !complete {
		t.Fatal("the walk went below the working copy")
	}
	if len(live) != 1 {
		t.Fatalf("live = %d projects, want only app", len(live))
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
	perDir := map[string]int{}
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
	}
}
