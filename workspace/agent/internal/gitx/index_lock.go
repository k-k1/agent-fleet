package gitx

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// StaleIndexLock returns the path of dir's index.lock when it is left over from a git that
// no longer runs: old enough, and no git process has its cwd in this working copy. A git
// killed while holding the lock (a container stop, an OOM kill) leaves it behind, and every
// later write in the working copy then fails with "index.lock: File exists" until someone
// removes it by hand. When in doubt this answers "", so a live git's lock is never touched.
func StaleIndexLock(dir string) string {
	p := indexLockPath(dir)
	if p == "" {
		return ""
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || time.Since(fi.ModTime()) < indexLockStaleAfter {
		return ""
	}
	if gitRunningIn(dir) {
		return ""
	}
	return p
}

// gitRunningIn reports whether a git process (git itself or a git-* helper) has its cwd at
// or under dir. git chdirs to the working tree before it takes the index lock, so this is
// where its holder shows up. A process whose cwd cannot be read counts as not there; the
// age check above still guards against the lock of a git that has just started.
func gitRunningIn(dir string) bool {
	top := resolvedPath(dir)
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return true
	}
	for _, e := range ents {
		if !isPID(e.Name()) {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "comm"))
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name != "git" && !strings.HasPrefix(name, "git-") {
			continue
		}
		cwd, err := os.Readlink(filepath.Join(procRoot, e.Name(), "cwd"))
		if err != nil {
			continue
		}
		cwd = resolvedPath(cwd)
		if cwd == top || strings.HasPrefix(cwd, top+string(filepath.Separator)) {
			return true
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

// removeStaleIndexLock deletes dir's index.lock only when StaleIndexLock still says it is
// stale at this moment; the client's earlier answer is never trusted on its own.
func removeStaleIndexLock(dir string) {
	if p := StaleIndexLock(dir); p != "" {
		_ = os.Remove(p)
	}
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
