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
// a process named comm whose cwd is cwd, with no open files and an empty command line and
// environment. The returned process dir can be edited further.
func fakeProc(t *testing.T) func(pid, comm, cwd string) string {
	t.Helper()
	root := t.TempDir()
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
	return func(pid, comm, cwd string) string {
		d := filepath.Join(root, pid)
		if err := os.MkdirAll(filepath.Join(d, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "comm"), comm+"\n")
		writeFile(t, filepath.Join(d, "cmdline"), comm+"\x00")
		writeFile(t, filepath.Join(d, "environ"), "HOME=/nowhere\x00")
		if err := os.Symlink(cwd, filepath.Join(d, "cwd")); err != nil {
			t.Fatal(err)
		}
		return d
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

// TestStaleIndexLockSeesHoldersOutsideTheWorkingCopy covers owners the cwd alone misses, and
// processes that cannot be observed.
func TestStaleIndexLockSeesHoldersOutsideTheWorkingCopy(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), "app")
	gitInit(t, dir)
	elsewhere := t.TempDir()
	lock := leaveIndexLock(t, dir, time.Hour)

	cases := map[string]func(add func(pid, comm, cwd string) string){
		"git --git-dir from another cwd": func(add func(pid, comm, cwd string) string) {
			d := add("300", "git", elsewhere)
			writeFile(t, filepath.Join(d, "cmdline"), "git\x00--git-dir="+filepath.Join(dir, ".git")+"\x00update-index\x00")
		},
		"git with GIT_INDEX_FILE": func(add func(pid, comm, cwd string) string) {
			d := add("301", "git", elsewhere)
			writeFile(t, filepath.Join(d, "environ"), "GIT_INDEX_FILE="+filepath.Join(dir, ".git", "index")+"\x00")
		},
		"non-git writer with the lock open": func(add func(pid, comm, cwd string) string) {
			d := add("302", "java", elsewhere)
			if err := os.Symlink(lock, filepath.Join(d, "fd", "7")); err != nil {
				t.Fatal(err)
			}
		},
		"git whose cwd cannot be read": func(add func(pid, comm, cwd string) string) {
			d := add("303", "git", elsewhere)
			if err := os.Remove(filepath.Join(d, "cwd")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(d, "cwd"), "") // readlink on a regular file: EINVAL, not "gone"
		},
		"git with a relative GIT_INDEX_FILE": func(add func(pid, comm, cwd string) string) {
			d := add("307", "git", filepath.Join(filepath.Dir(dir), "outside"))
			writeFile(t, filepath.Join(d, "environ"), "GIT_INDEX_FILE=../app/.git/index\x00")
		},
		"non-git process with hidden open files": func(add func(pid, comm, cwd string) string) {
			d := add("308", "java", elsewhere)
			if err := os.Remove(filepath.Join(d, "fd")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(d, "fd"), "")
		},
		"git whose open files cannot be listed": func(add func(pid, comm, cwd string) string) {
			d := add("304", "git", elsewhere)
			if err := os.Remove(filepath.Join(d, "fd")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(d, "fd"), "")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			add := fakeProc(t)
			add("1", "init", "/")
			setup(add)
			if got := StaleIndexLock(dir); got != "" {
				t.Fatalf("StaleIndexLock = %q, want empty (the lock may be held)", got)
			}
		})
	}

	t.Run("non-git process with hidden open files does not count", func(t *testing.T) {
		add := fakeProc(t)
		d := add("306", "tmux: client", elsewhere)
		if err := os.Remove(filepath.Join(d, "fd")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "fd"), "")
		if got := StaleIndexLock(dir); got != lock {
			t.Fatalf("StaleIndexLock = %q, want %q (a tmux client must not block recovery)", got, lock)
		}
	})

	t.Run("sibling worktree path does not count", func(t *testing.T) {
		add := fakeProc(t)
		d := add("305", "git", elsewhere)
		writeFile(t, filepath.Join(d, "cmdline"), "git\x00-C\x00"+dir+"@wip-x\x00status\x00")
		if got := StaleIndexLock(dir); got != lock {
			t.Fatalf("StaleIndexLock = %q, want %q", got, lock)
		}
	})
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

// TestStaleIndexLockRealWriterOutsideCwd runs a real git that holds the lock from another
// cwd (update-index waiting on stdin) and reads the real /proc.
func TestStaleIndexLockRealWriterOutsideCwd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc")
	}
	dir := filepath.Join(t.TempDir(), "app")
	gitInit(t, dir)
	cmd := exec.Command("git", "--git-dir="+filepath.Join(dir, ".git"), "--work-tree="+dir, "update-index", "--index-info")
	cmd.Dir = t.TempDir()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	lock := filepath.Join(dir, ".git", "index.lock")
	for i := 0; ; i++ {
		if _, err := os.Stat(lock); err == nil {
			break
		}
		if i > 200 {
			t.Fatal("git never took the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if got := StaleIndexLock(dir); got != "" {
		t.Fatalf("StaleIndexLock = %q while a live git holds it", got)
	}
}

// TestStaleIndexLockRealCommitWithAlternateIndex holds the lock the way a commit waiting on
// its editor does (file closed), from a sibling repository through GIT_INDEX_FILE spelled
// relative and through a symlinked alias, and reads the real /proc.
func TestStaleIndexLockRealCommitWithAlternateIndex(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	gitInit(t, target)
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	for name, indexFile := range map[string]string{
		"relative":         "../target/.git/index",
		"absolute symlink": filepath.Join(alias, ".git", "index"),
	} {
		t.Run(name, func(t *testing.T) {
			outside := filepath.Join(t.TempDir(), "outside")
			gitInit(t, outside)
			commitIntegrationFile(t, outside, "f")
			writeFile(t, filepath.Join(outside, "f"), "changed")
			if indexFile[0] != '/' {
				// The relative spelling is from a sibling of target.
				sib := filepath.Join(base, "outside-"+name)
				if err := os.Rename(outside, sib); err != nil {
					t.Fatal(err)
				}
				outside = sib
			}
			cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-a")
			cmd.Dir = outside
			cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexFile, "GIT_EDITOR=sleep 10 #")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(target, ".git", "index.lock")
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = os.Remove(lock) })
			for i := 0; ; i++ {
				if _, err := os.Stat(lock); err == nil {
					break
				}
				if i > 300 {
					t.Fatal("git never took the lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(lock, old, old); err != nil {
				t.Fatal(err)
			}
			if got := StaleIndexLock(target); got != "" {
				t.Fatalf("StaleIndexLock = %q while a commit holds it through GIT_INDEX_FILE=%s", got, indexFile)
			}
		})
	}
}
