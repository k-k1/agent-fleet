package gitx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeProc points procRoot at an empty tree for the test and returns a function that adds
// a process named comm whose cwd is cwd.
func fakeProc(t *testing.T) func(pid, comm, cwd string) {
	t.Helper()
	root := t.TempDir()
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
	return func(pid, comm, cwd string) {
		d := filepath.Join(root, pid)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "comm"), comm+"\n")
		if err := os.Symlink(cwd, filepath.Join(d, "cwd")); err != nil {
			t.Fatal(err)
		}
	}
}

// leaveIndexLock creates dir's index.lock as a killed git leaves it: empty, aged by age.
func leaveIndexLock(t *testing.T, dir string, age time.Duration) string {
	t.Helper()
	p := indexLockPath(dir)
	if p == "" {
		t.Fatalf("no index.lock path for %s", dir)
	}
	writeFile(t, p, "")
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStaleIndexLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), "app")
	gitInit(t, dir)
	addProc := fakeProc(t)

	if got := StaleIndexLock(dir); got != "" {
		t.Fatalf("no lock: StaleIndexLock = %q, want empty", got)
	}
	p := leaveIndexLock(t, dir, time.Second)
	if got := StaleIndexLock(dir); got != "" {
		t.Fatalf("fresh lock: StaleIndexLock = %q, want empty (a git may have just taken it)", got)
	}
	leaveIndexLock(t, dir, time.Hour)
	addProc("100", "git", t.TempDir())
	addProc("101", "bash", dir)
	if got := StaleIndexLock(dir); got != p {
		t.Fatalf("old lock, no git in the working copy: StaleIndexLock = %q, want %q", got, p)
	}
	addProc("102", "git", filepath.Join(dir, "sub"))
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := StaleIndexLock(dir); got != "" {
		t.Fatalf("old lock, git running in the working copy: StaleIndexLock = %q, want empty", got)
	}
}

func TestGitEnvSkipsOptionalLocks(t *testing.T) {
	if !slices.Contains(Cmd("", "status").Env, "GIT_OPTIONAL_LOCKS=0") {
		t.Fatal("Cmd does not set GIT_OPTIONAL_LOCKS=0; background status polls take index.lock again")
	}
}

// TestParentFFStaleIndexLock drives the parent fast-forward endpoint through a stale lock:
// the first try is answered with the stale-lock code, the confirmed retry removes it and
// fast-forwards, and a lock a running git holds is never removed.
func TestParentFFStaleIndexLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "repos", "app")
	gitInit(t, parent)
	forge := setupIntegrationUpstream(t, parent)
	worktree, err := EnsureWorktree(parent, "main", "feature-lock", "")
	if err != nil {
		t.Fatalf("ensureWorktree: %v", err)
	}
	commitIntegrationFile(t, forge, "upstream-change")
	runIntegrationGit(t, forge, "push", "origin", "main")
	runIntegrationGit(t, parent, "fetch", "origin")
	want, _ := Run(parent, "rev-parse", "origin/main")
	before, _ := Run(worktree, "rev-parse", "HEAD")

	addProc := fakeProc(t)
	lock := leaveIndexLock(t, worktree, time.Hour)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{name}/parent-ff", HandleRepoParentFF)
	post := func(body string) (int, map[string]string) {
		req := httptest.NewRequest(http.MethodPost, "/repos/"+filepath.Base(worktree)+"/parent-ff", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var res struct {
			Error map[string]string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		return rec.Code, res.Error
	}

	code, e := post("{}")
	if code != http.StatusConflict || e["code"] != errCodeIndexLockStale || e["lock"] != lock {
		t.Fatalf("stale lock: status %d error %v, want 409 %s with lock %s", code, e, errCodeIndexLockStale, lock)
	}
	if head, _ := Run(worktree, "rev-parse", "HEAD"); head != before {
		t.Fatalf("HEAD moved to %s on a refused fast-forward", head)
	}

	// A git running in the worktree owns the lock: the retry must leave it alone.
	addProc("200", "git", worktree)
	if code, e := post(`{"removeStaleLock":true}`); code == http.StatusOK || e["code"] == errCodeIndexLockStale {
		t.Fatalf("live lock: status %d error %v, want the plain failure", code, e)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("a lock held by a running git was removed: %v", err)
	}

	fakeProc(t)
	if code, e := post(`{"removeStaleLock":true}`); code != http.StatusOK {
		t.Fatalf("confirmed retry: status %d error %v, want 200", code, e)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("stale lock still present after the confirmed retry: %v", err)
	}
	if head, _ := Run(worktree, "rev-parse", "HEAD"); head != want {
		t.Fatalf("worktree HEAD = %s, want origin/main %s", head, want)
	}
}
