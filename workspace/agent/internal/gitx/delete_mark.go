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
	// sessions maps a copy being deleted to the names of the sessions living in it, taken inside
	// the gate with the same string match the delete settles them by. A session is matched by name,
	// so a path that reaches the copy through a symlink (inside it or outside it), or that no
	// longer resolves once the folder is gone, cannot make a lock slip past.
	sessions map[string][]string
	// raw maps a copy being deleted to the path the delete request named it by, which is what a
	// session's own Meta.Dir is matched against (string prefix) the way the delete's guards do.
	raw map[string]string
}{m: map[string]string{}, sessions: map[string][]string{}, raw: map[string]string{}}

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
func BeginDelete(dir, parent string, sessions ...string) (end func(), ok bool, why string) {
	rawDir := filepath.Clean(dir)
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
	deleteMarks.sessions[dir] = sessions
	deleteMarks.raw[dir] = rawDir
	var once sync.Once
	return func() {
		once.Do(func() {
			deleteMarks.mu.Lock()
			delete(deleteMarks.m, dir)
			delete(deleteMarks.sessions, dir)
			delete(deleteMarks.raw, dir)
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
	return underMarkLocked(p)
}

// underMarkLocked is UnderDeletion for a canonical path, with deleteMarks.mu held.
func underMarkLocked(p string) bool {
	for d := range deleteMarks.m {
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// SessionUnderDeletion reports whether the session is one a delete in flight is settling: one
// recorded by name when the delete decided, or one whose own Meta.Dir (dir, as stored) lies under
// the path the delete was asked for, so a session that came back after the names were taken
// (restored from the trash on its own) is covered too.
//
// dir is matched both as stored and cleaned. The delete's guards match the stored string, and
// cleaning alone is not enough: `<copy>/link/../../kept` with link -> `<copy>/a/b` cleans to a
// path outside the copy while the OS resolves it to `<copy>/kept`.
func SessionUnderDeletion(name, dir string) bool {
	deleteMarks.mu.Lock()
	defer deleteMarks.mu.Unlock()
	if dir != "" {
		clean := filepath.Clean(dir)
		for _, raw := range deleteMarks.raw {
			if underPath(dir, raw) || underPath(clean, raw) {
				return true
			}
		}
	}
	for _, names := range deleteMarks.sessions {
		for _, n := range names {
			if n == name {
				return true
			}
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
	// Inside a copy being deleted: a base clone reached through a symlink into the delete's target
	// would lose its registry under a worktree created now.
	if underMarkLocked(dir) || (parent != "" && underMarkLocked(parent)) {
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

// Launch reservations (#5122). A launch reads its folder, and a delete reads the running
// sessions, at moments of their own: a session whose process and meta are not published yet is
// invisible to the delete's guards, and a delete that decided after the launch read its folder is
// invisible to the launch. Each launch therefore reserves the folder it runs in inside the
// deletion gate, the same gate a delete decides in, and holds the reservation until its process
// and its meta are published. Under the gate one of the two always sees the other: the launch
// meets the mark and is refused, or the delete meets the reservation (and, once it is released,
// the running session) and is refused.

var launchRes = struct {
	mu   sync.Mutex
	next uint64
	// m maps a reservation to the folders it holds, each both as named (cleaned) and as its real
	// path, the two ways the delete's guards match a session's Meta.Dir.
	m map[uint64][]launchPath
}{m: map[uint64][]launchPath{}}

type launchPath struct{ raw, real string }

// ReserveLaunch reserves the folders a launch runs in (its Meta.Dir and its effective cwd). It
// refuses (ok=false) when one of them is a copy being deleted or lies inside one, matched the way
// a lock is (UnderDeletion, SessionUnderDeletion's string match). Call it inside the deletion
// gate. release is safe to call more than once.
func ReserveLaunch(name string, paths ...string) (release func(), ok bool) {
	var held []launchPath
	for _, p := range paths {
		if p == "" {
			continue
		}
		if UnderDeletion(p) || SessionUnderDeletion(name, p) {
			return nil, false
		}
		held = append(held, launchPath{raw: filepath.Clean(p), real: markKey(p)})
	}
	launchRes.mu.Lock()
	launchRes.next++
	id := launchRes.next
	launchRes.m[id] = held
	launchRes.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			launchRes.mu.Lock()
			delete(launchRes.m, id)
			launchRes.mu.Unlock()
		})
	}, true
}

// LaunchingUnder reports whether a launch holds a reservation on dir or on a folder inside it. A
// delete asks it inside the gate, beside its running-session guard: such a launch is a running
// session the guard cannot see yet.
func LaunchingUnder(dir string) bool {
	raw, real := filepath.Clean(dir), markKey(dir)
	launchRes.mu.Lock()
	defer launchRes.mu.Unlock()
	for _, held := range launchRes.m {
		for _, p := range held {
			if underPath(p.raw, raw) || underPath(p.real, real) {
				return true
			}
		}
	}
	return false
}
