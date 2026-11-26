package gitx

import (
	"os"
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
// invisible to the launch. Each launch therefore reserves the folder it runs in inside the launch
// gate, which a delete holds for its whole decision (inside the deletion gate), and releases it
// inside that gate once its process and its meta are published. One of the two always sees the
// other: the launch meets the mark and is refused, or the delete meets the reservation, or the
// running session it has become, and is refused.
//
// The launch gate is not the deletion gate: that one is also held across `git worktree add` by a
// worktree recreate or restore, whose hooks may start a session through the Agent's own API and
// wait for it. A launch never waits for git.

var launchGate sync.Mutex

// WithLaunchGate runs fn inside the launch gate. A delete takes it inside the deletion gate, for
// its decision only; a launch takes it alone. Keep fn short, and never run git or wait for a
// session inside it.
func WithLaunchGate(fn func()) {
	launchGate.Lock()
	defer launchGate.Unlock()
	fn()
}

var launchRes = struct {
	mu   sync.Mutex
	next uint64
	m    map[uint64][]launchPath
}{m: map[uint64][]launchPath{}}

// launchPath is one folder a launch holds, in the forms a delete's folder is compared with: raw
// as the launch spelled it (a delete of any folder the spelling passes through breaks it, `..`
// or not), real as the kernel resolves it, and clean, the lexical form, only when the path does
// not resolve (a lexical clean of `link/..` names another folder than the one the kernel enters).
type launchPath struct{ raw, real, clean string }

func newLaunchPath(p string) launchPath {
	real, resolved := launchRealPath(p)
	lp := launchPath{raw: p, real: real}
	if !resolved {
		lp.clean = filepath.Clean(p)
	}
	return lp
}

// under reports whether the launch path lies in dir, given as spelled and resolved.
func (lp launchPath) under(dirRaw, dirReal string) bool {
	return underPath(lp.raw, dirRaw) || underPath(lp.real, dirReal) || (lp.clean != "" && underPath(lp.clean, dirRaw))
}

// ReserveLaunch reserves the folders a launch runs in (its Meta.Dir and its cwd). It refuses
// (ok=false) when one of them lies in a copy being deleted, or when the delete is settling the
// session by name. Call it, and release, inside the launch gate (WithLaunchGate): a delete reads
// the reservations and then the running sessions, and a release between the two reads would hide
// a session that has just been published. release is safe to call more than once.
func ReserveLaunch(name string, paths ...string) (release func(), ok bool) {
	var held []launchPath
	for _, p := range paths {
		if p == "" {
			continue
		}
		held = append(held, newLaunchPath(p))
	}
	deleteMarks.mu.Lock()
	refused := sessionNamedLocked(name)
	for key, raw := range deleteMarks.raw {
		for _, lp := range held {
			if lp.under(raw, key) {
				refused = true
			}
		}
	}
	deleteMarks.mu.Unlock()
	if refused {
		return nil, false
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

// sessionNamedLocked reports whether a delete in flight recorded name among the sessions it
// settles. deleteMarks.mu is held.
func sessionNamedLocked(name string) bool {
	for _, names := range deleteMarks.sessions {
		for _, n := range names {
			if n == name {
				return true
			}
		}
	}
	return false
}

// launchRealPath resolves p the way the kernel enters it, component by component: a symlink is
// replaced by its target, and `..` leaves the folder reached so far, so `alias/jump/../kept`
// with jump a link names the link target's parent, not alias. A link whose target is gone (a
// folder a delete has just moved away) is still followed, and a component that does not exist is
// kept as spelled, so the answer names where the path leads even once it no longer resolves.
// resolved reports whether every component existed.
func launchRealPath(p string) (real string, resolved bool) {
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	const maxLinks = 40
	sep := string(filepath.Separator)
	todo := strings.Split(p, sep)
	cur, links, resolved := sep, 0, true
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, c)
		fi, err := os.Lstat(next)
		if err != nil {
			resolved = false
			cur = next
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 || links >= maxLinks {
			cur = next
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			resolved = false
			cur = next
			continue
		}
		links++
		if filepath.IsAbs(target) {
			cur = sep
		}
		todo = append(strings.Split(target, sep), todo...)
	}
	return cur, resolved
}

// LaunchingUnder reports whether a launch holds a reservation on dir or on a folder inside it. A
// delete asks it inside the launch gate, before its running-session guard: such a launch is a
// running session the guard cannot see yet.
func LaunchingUnder(dir string) bool {
	real, _ := launchRealPath(dir)
	raw := filepath.Clean(dir)
	launchRes.mu.Lock()
	defer launchRes.mu.Unlock()
	for _, held := range launchRes.m {
		for _, lp := range held {
			if lp.under(raw, real) {
				return true
			}
		}
	}
	return false
}
