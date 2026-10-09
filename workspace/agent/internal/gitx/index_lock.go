package gitx

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// indexLockStaleAfter is how old an index.lock must be before it may be called stale.
// The process scan alone is not enough: a git started between the scan and the remove
// would lose a lock it just took, and such a lock is seconds old.
const indexLockStaleAfter = 30 * time.Second

// procRoot is /proc; tests point it at a fake tree.
var procRoot = "/proc"

// indexLockPath is the index.lock git would take for the working copy at dir. A linked
// worktree's lives under the parent's .git/worktrees/<name>/, not in the worktree.
func indexLockPath(dir string) string {
	p, err := Run(dir, "rev-parse", "--git-path", "index.lock")
	if err != nil || p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p
}

// StaleIndexLock returns the path of dir's index.lock when it is left over from a writer that
// no longer runs. A git killed while holding the lock (a container stop, an OOM kill) leaves
// it behind, and every later write in the working copy then fails with "index.lock: File
// exists" until someone removes it by hand. When in doubt this answers "", so a live
// writer's lock is never touched.
func StaleIndexLock(dir string) string {
	p, _ := staleIndexLock(dir)
	return p
}

func staleIndexLock(dir string) (string, os.FileInfo) {
	p := indexLockPath(dir)
	if p == "" {
		return "", nil
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || time.Since(fi.ModTime()) < indexLockStaleAfter {
		return "", nil
	}
	if lockMayBeHeld(dir, p, fi) {
		return "", nil
	}
	return p, fi
}

// lockMayBeHeld reports whether any process could be the lock's owner:
//   - any process with the lock file open (git while it writes, and libgit2 / JGit writers,
//     whose process name says nothing);
//   - a git or git-* process whose cwd, command line or environment names the working copy
//     or its git dir. git keeps holding the lock with the file closed (a commit waiting on
//     its editor), and --git-dir / GIT_INDEX_FILE reach the index from any cwd.
//
// A process that vanished mid-scan is skipped; for what else counts when an entry cannot be
// read, see procMayHold. This assumes every writer runs in this PID namespace: the working copies
// belong to this workspace's container alone.
func lockMayBeHeld(dir, lock string, lockFI os.FileInfo) bool {
	marks := pathMarks(dir, filepath.Dir(lock))
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return true
	}
	self := strconv.Itoa(os.Getpid())
	for _, e := range ents {
		if !isPID(e.Name()) || e.Name() == self {
			continue
		}
		held, gone := procMayHold(filepath.Join(procRoot, e.Name()), lockFI, marks)
		if held && !gone {
			return true
		}
	}
	return false
}

// procMayHold checks one /proc/<pid>. gone=true means the process exited while it was read.
//
// A process can hide its open files from its own user: tmux clients make themselves
// non-dumpable, and the Console keeps one attached per open terminal, so refusing on every
// unreadable fd list would refuse always. Such a process counts as a possible owner only
// when its name is git's; a non-dumpable non-git writer is the gap this leaves.
func procMayHold(pd string, lockFI os.FileInfo, marks []string) (held, gone bool) {
	fds, err := os.ReadDir(filepath.Join(pd, "fd"))
	fdsHidden := err != nil
	if fdsHidden && procGone(err) {
		return false, true
	}
	for _, fd := range fds {
		if fi, err := os.Stat(filepath.Join(pd, "fd", fd.Name())); err == nil && os.SameFile(fi, lockFI) {
			return true, false
		}
	}
	comm, err := os.ReadFile(filepath.Join(pd, "comm"))
	if err != nil {
		return true, procGone(err)
	}
	name := strings.TrimSpace(string(comm))
	if name != "git" && !strings.HasPrefix(name, "git-") {
		return false, false
	}
	if fdsHidden {
		return true, false
	}
	cwd, err := os.Readlink(filepath.Join(pd, "cwd"))
	if err != nil {
		return true, procGone(err)
	}
	for _, f := range []string{"cmdline", "environ"} {
		b, err := os.ReadFile(filepath.Join(pd, f))
		if err != nil {
			return true, procGone(err)
		}
		if mentionsAny(string(b), marks) {
			return true, false
		}
	}
	return mentionsAny(cwd, marks) || mentionsAny(resolvedPath(cwd), marks), false
}

// procGone tells "the process exited while it was read" from "it cannot be observed".
func procGone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// pathMarks is every spelling of the working copy and of the git dir that holds its index.
func pathMarks(paths ...string) []string {
	var out []string
	for _, p := range paths {
		for _, q := range []string{filepath.Clean(p), resolvedPath(p)} {
			if !slices.Contains(out, q) {
				out = append(out, q)
			}
		}
	}
	return out
}

// mentionsAny reports whether s names one of marks as a whole path or a path under it.
// "/r/app" must not match "/r/app@wip-x", a sibling worktree with its own index.
func mentionsAny(s string, marks []string) bool {
	for _, m := range marks {
		for i := 0; ; {
			j := strings.Index(s[i:], m)
			if j < 0 {
				break
			}
			end := i + j + len(m)
			if end == len(s) || strings.ContainsRune("/\x00\n=:", rune(s[end])) {
				return true
			}
			i = i + j + 1
		}
	}
	return false
}

func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func isPID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// staleLockMu serializes removals. Two confirmed retries racing on one lock would otherwise
// both judge the old lock stale, and the later one could delete the lock the earlier one's
// fast-forward has just taken.
var staleLockMu sync.Mutex

// removeStaleIndexLock deletes dir's index.lock only when it is stale at this moment; the
// client's earlier answer is never trusted on its own. Right before the remove it checks
// that the path still names the file it judged: while that file exists no git can take the
// lock, so only another remover could have swapped it, and that is serialized here.
func removeStaleIndexLock(dir string) {
	staleLockMu.Lock()
	defer staleLockMu.Unlock()
	p, fi := staleIndexLock(dir)
	if p == "" {
		return
	}
	if now, err := os.Lstat(p); err != nil || !os.SameFile(now, fi) || !now.ModTime().Equal(fi.ModTime()) {
		return
	}
	_ = os.Remove(p)
}

// ffReq is the optional body of the fast-forward endpoints. RemoveStaleLock is the
// Console's retry after the user confirmed removing a stale index.lock.
type ffReq struct {
	RemoveStaleLock bool `json:"removeStaleLock"`
}

// writeIfStaleIndexLock answers a failed git write whose output names index.lock with
// errCodeIndexLockStale when the lock is stale, so the Console can offer to remove it.
// It reports whether it wrote the response.
func writeIfStaleIndexLock(w http.ResponseWriter, dir, out string) bool {
	if !strings.Contains(out, "index.lock") {
		return false
	}
	p := StaleIndexLock(dir)
	if p == "" {
		return false
	}
	httpx.WriteJSON(w, http.StatusConflict, indexLockStaleResp{Error: indexLockStaleErr{
		Code:    errCodeIndexLockStale,
		Message: "a stale index.lock is left in this working copy and no git process is running in it: " + out,
		Lock:    p,
	}})
	return true
}

// indexLockStaleResp is the shared error envelope plus the lock's path.
type indexLockStaleResp struct {
	Error indexLockStaleErr `json:"error"`
}

type indexLockStaleErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Lock    string `json:"lock"`
}
