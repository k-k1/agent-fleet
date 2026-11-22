package gitx

import (
	"path/filepath"
	"strings"
	"sync"
)

// The "being deleted" marks of working-copy deletes (ADR 0101, amended for #4833).
//
// The deletion gate (sessionx.WithDeletionGate) is held only for a delete's decision: the
// guards and the mark taken here. The slow work (tombstone, trash, `git worktree remove`) runs
// after the gate is released, with the mark standing in for it. The invariant the gate protects
// is unchanged: a lock request takes the gate, so it either lands before the guards read it
// (and the delete is refused) or finds the mark and is refused itself. Both are taken under the
// gate; mu below only keeps the map itself race-free for readers outside it.

// errCodeDeleteInProgress is the 409 code for a delete or a lock that meets a delete of the
// same copy (or of its base clone / its worktree) already in flight.
const errCodeDeleteInProgress = "delete_in_progress"

var deleteMarks = struct {
	mu sync.Mutex
	// m maps a working copy being deleted to its base clone ("" for a clone or a plain folder).
	m map[string]string
}{m: map[string]string{}}

// markKey is the identity a mark is compared by: the real path, the way the lock ledger
// (sessionx.AbsPath) and git itself identify a working copy. A symlink to a worktree, or a
// session whose Meta.Dir goes through one, must meet the same mark. A path that is gone (the
// delete has already removed it) resolves through its nearest existing ancestor, so the key
// stays the one the mark was taken under.
func markKey(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		up := filepath.Dir(cur)
		if up == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = up
	}
}

// BeginDelete marks dir as being deleted. parent is its base clone when dir is a linked
// worktree. It refuses (ok=false, with a message for the 409) when the same copy is already
// being deleted, when dir's base clone is, or when dir is a base clone one of whose worktrees
// is: those deletes must not overlap, because the base clone's worktree registry has to stay
// consistent under LinkedWorktreeCount. Call it inside the deletion gate. end clears the mark
// and is safe to call more than once.
func BeginDelete(dir, parent string) (end func(), ok bool, why string) {
	dir, parent = markKey(dir), markKeyOrEmpty(parent)
	deleteMarks.mu.Lock()
	defer deleteMarks.mu.Unlock()
	if _, busy := deleteMarks.m[dir]; busy {
		return nil, false, "this working copy is already being deleted"
	}
	if parent != "" {
		if _, busy := deleteMarks.m[parent]; busy {
			return nil, false, "the base clone of this worktree is being deleted"
		}
	}
	for _, p := range deleteMarks.m {
		if p == dir {
			return nil, false, "a worktree of this working copy is being deleted; try again once it is gone"
		}
	}
	deleteMarks.m[dir] = parent
	var once sync.Once
	return func() {
		once.Do(func() {
			deleteMarks.mu.Lock()
			delete(deleteMarks.m, dir)
			deleteMarks.mu.Unlock()
		})
	}, true, ""
}

func markKeyOrEmpty(p string) string {
	if p == "" {
		return ""
	}
	return markKey(p)
}

// UnderDeletion reports whether p is a working copy being deleted, or lies inside one. The
// lock endpoints use it, under the gate, to refuse a lock on something that is going away.
func UnderDeletion(p string) bool {
	if p == "" {
		return false
	}
	p = markKey(p)
	deleteMarks.mu.Lock()
	defer deleteMarks.mu.Unlock()
	for d := range deleteMarks.m {
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ErrCodeDeleteInProgress is the code lock endpoints answer with (409) while UnderDeletion.
func ErrCodeDeleteInProgress() string { return errCodeDeleteInProgress }

// DeleteTouches reports whether a delete in flight would collide with a git registry operation
// on dir whose base clone is parent (recreating or restoring a worktree): the copy itself or its
// base clone is being deleted, or another worktree of that base clone is, in which case the two
// would run `git worktree prune` / `add` against one registry at once.
func DeleteTouches(dir, parent string) bool {
	dir, parent = markKey(dir), markKeyOrEmpty(parent)
	deleteMarks.mu.Lock()
	defer deleteMarks.mu.Unlock()
	if _, busy := deleteMarks.m[dir]; busy {
		return true
	}
	if parent == "" {
		return false
	}
	if _, busy := deleteMarks.m[parent]; busy {
		return true
	}
	for _, p := range deleteMarks.m {
		if p == parent {
			return true
		}
	}
	return false
}
