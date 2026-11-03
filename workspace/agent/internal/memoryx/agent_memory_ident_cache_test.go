package memoryx

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
)

// fresh derives the identity with no cache, the reference the cached answer must equal.
func agentMemIdentFresh(dir string) *agentMemProject {
	p, _ := agentMemProjectIdent(dir, dir, func(args ...string) (string, error) { return gitx.Run(dir, args...) })
	return p
}

func agentMemIdentGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func agentMemIdentSame(t *testing.T, dir string) *agentMemProject {
	t.Helper()
	got, want := agentMemProjectFor(dir), agentMemIdentFresh(dir)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cached %+v != fresh %+v", got, want)
	}
	return got
}

// The second call for an unchanged working copy spawns no git at all.
func TestAgentMemProjectForIsServedFromCache(t *testing.T) {
	_, clone, wt := agentMemTestEnv(t)
	for _, dir := range []string{clone, wt} {
		agentMemIdentSame(t, dir) // fills the cache
		before := gitx.SpawnCount()
		for i := 0; i < 5; i++ {
			agentMemProjectFor(dir)
		}
		if n := gitx.SpawnCount() - before; n != 0 {
			t.Errorf("%s: 5 cached calls spawned %d git processes, want 0", dir, n)
		}
	}
	if a, b := agentMemProjectFor(clone), agentMemProjectFor(wt); a.ID != b.ID {
		t.Errorf("worktree id %s != clone id %s", b.ID, a.ID)
	}
}

func TestAgentMemProjectForSeesARemoteChange(t *testing.T) {
	_, clone, wt := agentMemTestEnv(t)
	agentMemIdentGit(t, clone, "remote", "add", "origin", "https://example.com/a/one.git")
	for _, dir := range []string{clone, wt} {
		if r := agentMemIdentSame(t, dir).Remote; r == "" {
			t.Fatalf("%s: no remote seen", dir)
		}
	}
	// Same length on purpose: size alone must not be what notices.
	agentMemIdentGit(t, clone, "remote", "set-url", "origin", "https://example.com/a/two.git")
	for _, dir := range []string{clone, wt} {
		got := agentMemIdentSame(t, dir)
		if got.Remote != agentMemNormalizeRemote("https://example.com/a/two.git") {
			t.Errorf("%s: stale remote %q", dir, got.Remote)
		}
	}
	agentMemIdentGit(t, clone, "remote", "remove", "origin")
	for _, dir := range []string{clone, wt} {
		if r := agentMemIdentSame(t, dir).Remote; r != "" {
			t.Errorf("%s: remote %q survives its removal", dir, r)
		}
	}
}

// url.<base>.insteadOf in the user's config rewrites what `remote get-url` prints.
func TestAgentMemProjectForSeesGlobalConfigChange(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gc"))
	agentMemIdentGit(t, clone, "remote", "add", "origin", "https://example.com/a/one.git")
	before := agentMemIdentSame(t, clone).Remote
	if err := os.WriteFile(filepath.Join(home, "gc"), []byte("[url \"https://mirror.example.net/\"]\n\tinsteadOf = https://example.com/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := agentMemIdentSame(t, clone).Remote; got == before {
		t.Errorf("remote %q did not follow the insteadOf rule", got)
	}
}

func TestAgentMemProjectForWorktreeMovedOrRemoved(t *testing.T) {
	_, clone, wt := agentMemTestEnv(t)
	agentMemIdentSame(t, wt)
	moved := wt + "-moved"
	if err := os.Rename(wt, moved); err != nil {
		t.Fatal(err)
	}
	if got := agentMemProjectFor(wt); !reflect.DeepEqual(got, agentMemIdentFresh(wt)) {
		t.Errorf("old path still answers after the move: %+v", got)
	}
	agentMemIdentGit(t, clone, "worktree", "repair", moved)
	if a, b := agentMemIdentSame(t, moved), agentMemProjectFor(clone); a.ID != b.ID {
		t.Errorf("moved worktree id %s != clone id %s", a.ID, b.ID)
	}
	if err := os.RemoveAll(moved); err != nil {
		t.Fatal(err)
	}
	if got := agentMemProjectFor(moved); got != nil && got.VCS == "git" {
		t.Errorf("a removed worktree still resolves: %+v", got)
	}
}

func TestAgentMemProjectForNoRemoteAndNotGit(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	if r := agentMemIdentSame(t, clone).Remote; r != "" {
		t.Errorf("remote %q on a repo without one", r)
	}
	plain := filepath.Join(home, "repos", "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if p := agentMemIdentSame(t, plain); p == nil || p.VCS == "git" {
		t.Errorf("plain directory: %+v", p)
	}
	// It becomes a repository later: the cache must not have pinned "not git".
	agentMemIdentGit(t, plain, "init", "-q")
	if p := agentMemIdentSame(t, plain); p.VCS != "git" {
		t.Errorf("after git init: %+v", p)
	}
	if p := agentMemProjectFor(home); p != nil {
		t.Errorf("the home directory has a project: %+v", p)
	}
}

// Directories that differ only by '@' or are reached through a symlink never share a slot.
func TestAgentMemProjectForKeysArePerPath(t *testing.T) {
	home, clone, wt := agentMemTestEnv(t)
	agentMemIdentSame(t, clone)
	agentMemIdentSame(t, wt)
	link := filepath.Join(home, "repos", "alias")
	if err := os.Symlink(clone, link); err != nil {
		t.Fatal(err)
	}
	a := agentMemIdentSame(t, link)
	if b := agentMemProjectFor(clone); a.ID != b.ID {
		t.Errorf("symlinked path id %s != target id %s", a.ID, b.ID)
	}
	// Retarget the link at another repository: the old answer must not be served.
	other := filepath.Join(home, "repos", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	agentMemIdentGit(t, other, "init", "-q")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if got := agentMemIdentSame(t, link); got.ID == a.ID {
		t.Errorf("retargeted link still answers with %s", got.ID)
	}
}

func TestAgentMemIdentCacheIsBoundedAndExpires(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	c := &agentMemIdentCache
	now := time.Now()
	c.Lock()
	old := c.now
	c.now = func() time.Time { return now }
	clear(c.m)
	c.Unlock()
	t.Cleanup(func() { c.Lock(); c.now = old; clear(c.m); c.Unlock() })

	for i := 0; i < agentMemIdentMax+10; i++ {
		agentMemIdentStore("k"+string(rune('a'+i%26))+time.Duration(i).String(), "s", &agentMemProject{ID: "x"})
	}
	c.Lock()
	n := len(c.m)
	c.Unlock()
	if n > agentMemIdentMax {
		t.Errorf("cache holds %d entries, bound is %d", n, agentMemIdentMax)
	}

	agentMemProjectFor(clone)
	before := gitx.SpawnCount()
	agentMemProjectFor(clone)
	if gitx.SpawnCount() != before {
		t.Fatal("not cached")
	}
	c.Lock()
	c.now = func() time.Time { return now.Add(agentMemIdentTTL + time.Second) }
	c.Unlock()
	agentMemProjectFor(clone)
	if gitx.SpawnCount() == before {
		t.Error("an entry past its TTL was served")
	}
}

func TestAgentMemProjectForConcurrent(t *testing.T) {
	_, clone, wt := agentMemTestEnv(t)
	want := agentMemIdentFresh(clone)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir := clone
			if i%2 == 1 {
				dir = wt
			}
			for j := 0; j < 20; j++ {
				if p := agentMemProjectFor(dir); p == nil || p.ID != want.ID {
					t.Errorf("%s: got %+v", dir, p)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestMemoryGCAutoIsThrottled(t *testing.T) {
	g := &memoryGCAuto
	now := time.Now()
	g.Lock()
	old := g.now
	g.now = func() time.Time { return now }
	clear(g.m)
	g.Unlock()
	t.Cleanup(func() { g.Lock(); g.now = old; clear(g.m); g.Unlock() })

	if !memoryGCDue("r") {
		t.Fatal("the first write of a process must run gc")
	}
	for i := 1; i < memoryGCBatch; i++ {
		if memoryGCDue("r") {
			t.Fatalf("write %d ran gc inside the interval and batch", i)
		}
	}
	if !memoryGCDue("r") {
		t.Error("the batch boundary did not run gc")
	}
	if memoryGCDue("r") {
		t.Error("the call right after a run was not throttled")
	}
	now = now.Add(memoryGCInterval)
	if !memoryGCDue("r") {
		t.Error("an elapsed interval did not run gc")
	}
	if !memoryGCDue("other") {
		t.Error("another repository shares the throttle")
	}
	for i := 0; i < memoryGCMaxRepos*2; i++ {
		memoryGCDue(time.Duration(i).String())
	}
	g.Lock()
	n := len(g.m)
	g.Unlock()
	if n > memoryGCMaxRepos {
		t.Errorf("throttle map holds %d repos", n)
	}
}

// Through the real write path: many writes spawn far fewer gc processes than writes, and the
// first one is not skipped. A logging git on PATH counts the processes.
func TestAgentMemoryWritesRunGCAutoAtMostOncePerBatch(t *testing.T) {
	agentMemTestEnv(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in gc) echo gc >> '" + log + "';; esac; done\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	g := &memoryGCAuto
	g.Lock()
	clear(g.m)
	g.Unlock()
	if err := memoryEnsureRepo(); err != nil {
		t.Fatal(err)
	}
	const writes = 3 * memoryGCBatch
	for i := 0; i < writes; i++ {
		memoryRunGCAuto()
	}
	raw, _ := os.ReadFile(log)
	n := strings.Count(string(raw), "gc\n")
	if n < 1 {
		t.Fatal("the first write did not run gc --auto")
	}
	if want := 1 + writes/memoryGCBatch; n > want {
		t.Errorf("%d gc processes for %d writes, want at most %d", n, writes, want)
	}
}

// GIT_DIR picks the repository, so the files under dir/.git say nothing about the answer.
func TestAgentMemProjectForBypassesCacheUnderGitDirOverride(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	other := filepath.Join(home, "repos", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	agentMemIdentGit(t, other, "init", "-q")
	link := filepath.Join(home, "gitdir-link")
	if err := os.Symlink(filepath.Join(clone, ".git"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", link)
	first := agentMemIdentSame(t, clone)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".git"), link); err != nil {
		t.Fatal(err)
	}
	if got := agentMemIdentSame(t, clone); got.ID == first.ID {
		t.Errorf("GIT_DIR retargeted yet the id stayed %s", got.ID)
	}
}

// An include target (and its condition) is outside the stamp, so a config with includes is
// never cached.
func TestAgentMemProjectForSeesAnIncludedConfigChange(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	inc := filepath.Join(home, "inc.cfg")
	write := func(url string) {
		if err := os.WriteFile(inc, []byte("[remote \"origin\"]\n\turl = "+url+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("https://example.com/a/one.git")
	agentMemIdentGit(t, clone, "config", "include.path", inc)
	first := agentMemIdentSame(t, clone).Remote
	if first == "" {
		t.Fatal("include not seen")
	}
	write("https://example.com/b/two.git")
	if got := agentMemIdentSame(t, clone).Remote; got == first {
		t.Errorf("remote %q did not follow the included file", got)
	}
}

// Without HEAD git no longer accepts the directory as a repository.
func TestAgentMemProjectForSeesHEADRemoved(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	agentMemIdentSame(t, clone)
	if err := os.Remove(filepath.Join(clone, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	agentMemIdentSame(t, clone) // equal to the fresh (fallback) answer
}
