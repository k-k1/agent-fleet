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
// gate, and releases it inside that gate once its process and its meta are published. A delete
// counts the releases (LaunchEpoch), reads the running sessions, and then, inside the gate,
// marks the folder only if no launch there holds a reservation or has released one since
// (MarkAgainstLaunches). One of the two always sees the other: the launch meets the mark and is
// refused, or the delete meets the reservation, or the running session it has become.
//
// The launch gate is not the deletion gate: that one is also held across `git worktree add` by a
// worktree recreate or restore, whose hooks may start a session through the Agent's own API and
// wait for it. Nor does it cover the delete's read of the running sessions, which waits for tmux.
// A launch never waits for git or tmux.

var launchGate sync.Mutex

// WithLaunchGate runs fn inside the launch gate. A delete takes it inside the deletion gate, to
// mark; a launch takes it alone. Keep fn short: never run git or tmux, or wait for a session,
// inside it.
func WithLaunchGate(fn func()) {
	launchGate.Lock()
	defer launchGate.Unlock()
	fn()
}

var launchRes = struct {
	mu   sync.Mutex
	next uint64
	m    map[uint64][]launchPath
	// released counts the releases so far (LaunchEpoch), and recent holds the folders of the
	// latest ones, numbered by that count (LaunchedUnderSince).
	released uint64
	recent   []releasedLaunch
}{m: map[uint64][]launchPath{}}

type releasedLaunch struct {
	n    uint64
	held []launchPath
}

// recentReleases bounds the releases LaunchedUnderSince can look back over; a delete that falls
// further behind reads the running sessions again.
const recentReleases = 256

// LaunchEpoch counts the reservations released so far. A delete takes it before it reads the
// running sessions, which it does outside the launch gate (that read waits for tmux, and no
// launch may wait for it), and asks LaunchedUnderSince inside the gate.
func LaunchEpoch() uint64 {
	launchRes.mu.Lock()
	defer launchRes.mu.Unlock()
	return launchRes.released
}

// LaunchedUnderSince reports whether a launch at or under dir released its reservation after
// epoch (LaunchEpoch): it published its session after the delete may have read the running
// sessions, so they have to be read again. Also true when too many releases happened since to
// tell.
func LaunchedUnderSince(dir string, epoch uint64) bool {
	raw, real := filepath.Clean(dir), launchRealPath(dir)
	launchRes.mu.Lock()
	defer launchRes.mu.Unlock()
	if launchRes.released-epoch > uint64(len(launchRes.recent)) {
		return true
	}
	for _, r := range launchRes.recent {
		if r.n <= epoch {
			continue
		}
		for _, lp := range r.held {
			if lp.under(raw, real) {
				return true
			}
		}
	}
	return false
}

// launchPath is one folder a launch holds, in the forms a delete's folder is compared with: raw
// as the launch spelled it (a delete of any folder the spelling passes through breaks it, `..`
// or not), via, every folder the kernel passes through on the way, the last of which is where it
// lands (a link into a copy followed by `..` back out of it still needs the copy), and clean, the
// lexical form, only when the path does not resolve (a lexical clean of `link/..` names another
// folder than the one the kernel enters).
type launchPath struct {
	raw, clean string
	via        []string
}

func newLaunchPath(p string) launchPath {
	via, resolved := launchWalk(p)
	lp := launchPath{raw: p, via: via}
	if !resolved {
		lp.clean = filepath.Clean(p)
	}
	return lp
}

// under reports whether the launch path lies in dir, or passes through it, given as spelled and
// resolved.
func (lp launchPath) under(dirRaw, dirReal string) bool {
	if underPath(lp.raw, dirRaw) || (lp.clean != "" && underPath(lp.clean, dirRaw)) {
		return true
	}
	for _, v := range lp.via {
		if underPath(v, dirReal) {
			return true
		}
	}
	return false
}

// ReserveLaunch reserves the folders a launch runs in (its Meta.Dir and its cwd). It refuses
// (ok=false) when one of them lies in a copy being deleted: by its spelling, which covers the
// string match a delete settles sessions by (sessionsToSettleUnder), or by where the kernel
// takes it. The session's name is not matched: the names a mark records (sessionNamesUnder) also
// take a lexical clean of Meta.Dir, which names another folder when a link precedes `..`. Call it, and release, inside the launch gate (WithLaunchGate), so a delete
// that marks sees every reservation and every release (MarkAgainstLaunches). release is safe to
// call more than once.
func ReserveLaunch(paths ...string) (release func(), ok bool) {
	var held []launchPath
	for _, p := range paths {
		if p == "" {
			continue
		}
		held = append(held, newLaunchPath(p))
	}
	deleteMarks.mu.Lock()
	refused := false
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
			launchRes.released++
			launchRes.recent = append(launchRes.recent, releasedLaunch{n: launchRes.released, held: held})
			if len(launchRes.recent) > recentReleases {
				launchRes.recent = launchRes.recent[len(launchRes.recent)-recentReleases:]
			}
			launchRes.mu.Unlock()
		})
	}, true
}

// launchRealPath is where launchWalk lands.
func launchRealPath(p string) string {
	via, _ := launchWalk(p)
	return via[len(via)-1]
}

// launchWalk resolves p the way the kernel enters it, component by component, and answers every
// folder it passes through, the last being where it lands: a symlink is replaced by its target,
// and `..` leaves the folder reached so far, so `alias/jump/../kept` with jump a link lands in
// the link target's parent, not alias. A link whose target is gone (a folder a delete has just
// moved away) is still followed, and a component that does not exist is kept as spelled, so the
// answer names where the path leads even once it no longer resolves. resolved reports whether
// every component existed.
func launchWalk(p string) (via []string, resolved bool) {
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	const maxLinks = 40
	sep := string(filepath.Separator)
	todo := strings.Split(p, sep)
	cur, links := sep, 0
	resolved = true
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			via = append(via, cur)
			continue
		}
		next := filepath.Join(cur, c)
		fi, err := os.Lstat(next)
		if err != nil {
			resolved = false
			cur = next
			via = append(via, cur)
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 || links >= maxLinks {
			cur = next
			via = append(via, cur)
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			resolved = false
			cur = next
			via = append(via, cur)
			continue
		}
		links++
		if filepath.IsAbs(target) {
			cur = sep
		}
		todo = append(strings.Split(target, sep), todo...)
	}
	return append(via, cur), resolved
}

// LaunchingUnder reports whether a launch holds a reservation on dir or on a folder inside it
// (or one its cwd passes through): a running session a delete's guard cannot see yet.
func LaunchingUnder(dir string) bool {
	real := launchRealPath(dir)
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

// MarkOutcome is what MarkAgainstLaunches did.
type MarkOutcome int

const (
	// MarkTaken: the mark is held; call end when the delete is over.
	MarkTaken MarkOutcome = iota
	// MarkLaunching: a launch holds a reservation in the folder; refuse as for a running session.
	MarkLaunching
	// MarkReread: a launch in the folder was published since epoch; read the running sessions
	// again (up to MaxLaunchRereads times, then refuse as MarkLaunching).
	MarkReread
	// MarkBusy: BeginDelete refused; why says why.
	MarkBusy
)

// MaxLaunchRereads bounds how often a delete reads the running sessions again (MarkReread).
const MaxLaunchRereads = 4

// MarkAgainstLaunches is the end of a delete's decision: inside the launch gate, it refuses while a
// launch holds a reservation at or under dir or has released one since epoch (taken with
// LaunchEpoch before the running sessions were read, outside the gate), and otherwise marks dir
// (BeginDelete, with the session names sessions answers). A launch then either meets the mark or
// has been seen by the delete.
func MarkAgainstLaunches(dir, parent string, epoch uint64, sessions func() []string) (end func(), out MarkOutcome, why string) {
	WithLaunchGate(func() {
		switch {
		case LaunchingUnder(dir):
			out = MarkLaunching
		case LaunchedUnderSince(dir, epoch):
			out = MarkReread
		default:
			var ok bool
			if end, ok, why = BeginDelete(dir, parent, sessions()...); !ok {
				out = MarkBusy
			}
		}
	})
	return end, out, why
}
