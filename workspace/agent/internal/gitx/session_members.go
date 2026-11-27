package gitx

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Which sessions live in a folder. A working-copy or space delete asks it at every step (the
// running and locked guards, the names its mark records, the shell sessions it trashes, the
// tombstone's shelf list, the shelving after the move, the restore), and so do the guards and
// counts outside a delete (sessionx.LiveSessionsInDir, LockedSessionsInDir). With one rule a
// session cannot be refused by one step and missed by the next because of how its path is spelt.
//
// There are two answers, one wider than the other. SessionLivesIn is where the session's paths
// lead now, and decides what a delete settles: shelving or trashing a session whose folder is
// elsewhere would take it from a folder that stays. SessionMayRunIn adds the lexical clean of
// those paths and decides what a delete or a checkout refuses: a session started through a link
// that has been re-pointed since still runs in the folder its process entered, which the clean
// names when the link preceded `..`, and refusing for one too many is the safe side.

// SessionLivesIn reports whether m lives in dir: its Meta.Dir, or the folder its process starts in
// (Dir joined with the stored Subdir), lies at or under dir, by its spelling or by where the kernel
// takes it. It is the comparison a launch's reservation is judged by (launchPath.under), so a
// session a delete settles is one whose launch that delete would have refused.
func SessionLivesIn(m session.Meta, dir string) bool {
	if dir == "" {
		return false
	}
	return newDirMatch(dir).has(m)
}

// SessionMayRunIn is SessionLivesIn, or a lexical clean of m's paths, resolved, lies in dir: what
// a guard refuses on.
func SessionMayRunIn(m session.Meta, dir string) bool {
	if dir == "" {
		return false
	}
	return newDirMatch(dir).mayRun(m)
}

// MayRunIn is SessionMayRunIn with dir resolved once, for a caller that tests many sessions.
func MayRunIn(dir string) func(session.Meta) bool {
	if dir == "" {
		return func(session.Meta) bool { return false }
	}
	return newDirMatch(dir).mayRun
}

type dirMatch struct{ raw, real, key string }

func newDirMatch(dir string) dirMatch {
	real := launchRealPath(dir)
	// A folder that is itself a link is removed as the link alone (rmtree, a rename): what lives in
	// its target stays, so only the link's own spelling is the folder here. The guards still resolve
	// it (key), and refuse while a session runs in the target, as before.
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		real = filepath.Join(launchRealPath(filepath.Dir(filepath.Clean(dir))), filepath.Base(dir))
	}
	return dirMatch{raw: filepath.Clean(dir), real: real, key: markKey(dir)}
}

// sessionPaths is the folders m holds: its Dir, and the Subdir beneath it as stored, not Meta.CWD,
// which falls back to Dir once the Subdir is gone (reserveLaunch reserves the same two).
func sessionPaths(m session.Meta) []string {
	if m.Dir == "" {
		return nil
	}
	if m.Subdir == "" {
		return []string{m.Dir}
	}
	return []string{m.Dir, filepath.Join(m.Dir, filepath.FromSlash(m.Subdir))}
}

func (d dirMatch) has(m session.Meta) bool {
	for _, p := range sessionPaths(m) {
		if newLaunchPath(p).under(d.raw, d.real) {
			return true
		}
	}
	return false
}

// spelt reports whether m's Meta.Dir, as written, lies in dir: the rule a delete settled by before
// SessionLivesIn, kept for what appears in the folder while a delete runs.
func (d dirMatch) spelt(m session.Meta) bool {
	return m.Dir != "" && underPath(m.Dir, d.raw)
}

func (d dirMatch) mayRun(m session.Meta) bool {
	if d.has(m) {
		return true
	}
	for _, p := range sessionPaths(m) {
		if underPath(markKey(p), d.key) {
			return true
		}
	}
	return false
}

// DirSessions is one read of the sessions in a folder, with whether each runs.
type DirSessions struct {
	// members live in the folder (SessionLivesIn); guarded may run in it (SessionMayRunIn), a
	// superset.
	members, guarded []session.Meta
	live             map[string]bool
}

// unpublishedLaunches covers a launch whose meta write and subsequent stop both failed.
// The launch reservation is still held when the entry is recorded. A later checked
// observation releases it once the process is gone.
var unpublishedLaunches sync.Map

func NoteUnpublishedLaunch(m session.Meta) {
	unpublishedLaunches.Store(session.MetaPath(m.Name), &m)
}

// ReadDirSessions reads every session meta and tmux's running sessions once and keeps the sessions
// in dir. It fails rather than answer a short list when the sessions directory cannot be listed,
// when a meta file exists but cannot be read (it may be a session running or locked in dir), or
// when tmux cannot say what runs: a delete that took any of these for "nothing there" would remove
// a folder under a running or locked session.
func ReadDirSessions(dir string) (DirSessions, error) {
	// Snapshot before checking tmux, so a probe started before the failure cannot discard
	// a record for a process it did not yet see.
	var unpublished []*session.Meta
	unpublishedLaunches.Range(func(key, value any) bool {
		m := value.(*session.Meta)
		if key == session.MetaPath(m.Name) {
			unpublished = append(unpublished, m)
		}
		return true
	})
	metas, unreadable, err := session.ListMetasChecked()
	if err != nil {
		return DirSessions{}, fmt.Errorf("the sessions could not be listed: %w", err)
	}
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		return DirSessions{}, fmt.Errorf("the record of session(s) %s cannot be read, so whether they live here is unknown; repair or remove %s",
			strings.Join(unreadable, ", "), filepath.Join(session.MetaDir(), "<name>.json"))
	}
	live, err := tmuxx.LiveSessionNamesChecked()
	if err != nil {
		return DirSessions{}, fmt.Errorf("could not tell which sessions are running: %w", err)
	}
	d := newDirMatch(dir)
	out := DirSessions{live: live}
	for _, m := range metas {
		switch {
		case d.has(m):
			out.members = append(out.members, m)
			out.guarded = append(out.guarded, m)
		case d.mayRun(m):
			out.guarded = append(out.guarded, m)
		}
	}
	for _, m := range unpublished {
		if !out.alive(*m) {
			unpublishedLaunches.CompareAndDelete(session.MetaPath(m.Name), m)
		} else if d.mayRun(*m) {
			out.guarded = append(out.guarded, *m)
		}
	}
	return out, nil
}

func (s DirSessions) alive(m session.Meta) bool {
	return s.live[m.Name] || (m.DriverKind() == session.DriverManaged && managedAlive(m))
}

// Running is the display names of the sessions that may run in the folder and are running,
// archived ones aside.
func (s DirSessions) Running() []string {
	var out []string
	for _, m := range s.guarded {
		if !m.Archived && s.alive(m) {
			out = append(out, session.Display(m))
		}
	}
	sort.Strings(out)
	return out
}

// Locked is the display names of the deletion-locked sessions that may run in the folder, running
// or not, archived included.
func (s DirSessions) Locked() []string {
	var out []string
	for _, m := range s.guarded {
		if m.Locked {
			out = append(out, session.Display(m))
		}
	}
	sort.Strings(out)
	return out
}

// Names is the names of every session that may run in the folder: what a delete's mark records,
// so a lock on any of them is refused while the delete runs.
func (s DirSessions) Names() []string {
	out := make([]string, 0, len(s.guarded))
	for _, m := range s.guarded {
		out = append(out, m.Name)
	}
	return out
}

// ToSettle is the sessions a delete of the folder settles: living in it, not running, not locked,
// not already on the shelf.
func (s DirSessions) ToSettle() []session.Meta {
	var out []session.Meta
	for _, m := range s.members {
		if s.alive(m) || m.Locked || m.Archived {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ErrSessionLeftFolder is what a trash for a folder's sake answers for a session that no longer
// lives in the folder by the time it would be removed (a link it is spelt through was re-pointed):
// the session is left as it is, and the delete goes on.
var ErrSessionLeftFolder = errors.New("the session no longer lives in the folder being deleted")

// RemoveWithShellTrash prepares the decided shell / ssm sessions' archives before remove runs.
// Each must still belong to dir when its meta goes. On a failed remove their metas come back.
// A nonempty failed name identifies a shell whose trash preparation failed.
func RemoveWithShellTrash(dir string, settle []session.Meta, remove func() error) (failed string, err error) {
	var shells []session.Meta
	in := newDirMatch(dir)
	for _, m := range settle {
		if m.Kind == session.KindShell || m.Kind == session.KindSSM {
			shells = append(shells, m)
		}
	}
	return deps.RemoveWithShellTrash(shells, in.has, remove)
}

// settleAfterRemoval settles the stopped sessions of dir once it has been removed or moved: AI
// sessions to the shelf, a shell / ssm one left (or come in) since the trash step to the trash.
// The sessions the delete decided before the removal (before) are taken as their meta is now and
// only while they still live in dir (a link into the folder is followed even once it dangles), so
// one re-pointed elsewhere meanwhile stays where it is. Beyond them, unless onlyDecided, a session
// whose Meta.Dir is spelt inside dir is taken (one restored into the folder while the delete ran,
// say), as before: a link re-pointed into the folder after the decision is not a session anyone
// decided to settle. onlyDecided is for a delete whose client was shown the sessions (a space
// dialog's expect): nothing it was not shown is settled. When tmux cannot be asked, the decided
// sessions are settled all the same, since the mark keeps every launch out of dir (ReserveLaunch)
// and they are still stopped; only the search for others is given up.
func settleAfterRemoval(dir string, before []session.Meta, onlyDecided bool) error {
	decided := map[string]bool{}
	for _, b := range before {
		decided[b.Name] = true
	}
	live, err := tmuxx.LiveSessionNamesChecked()
	var metas []session.Meta
	if err != nil {
		log.Printf("delete %s: could not tell which sessions run, so only the sessions decided before the removal are settled: %v", dir, err)
		live = map[string]bool{}
	} else {
		metas = session.ListMetas()
	}
	d := newDirMatch(dir)
	belongs := func(m session.Meta) bool {
		if decided[m.Name] {
			return d.has(m)
		}
		return !onlyDecided && d.spelt(m)
	}
	s := DirSessions{live: live}
	for _, m := range metas {
		if !decided[m.Name] && belongs(m) {
			s.members = append(s.members, m)
		}
	}
	var settleErr error
	for _, b := range before {
		switch m, st := session.ReadMetaState(b.Name); st {
		case session.MetaUnreadable:
			settleErr = errors.Join(settleErr, fmt.Errorf("could not read session %s", b.Name))
		case session.MetaOK:
			if belongs(m) {
				s.members = append(s.members, m)
			}
		}
	}
	for _, m := range s.ToSettle() {
		if m.Kind == session.KindShell || m.Kind == session.KindSSM {
			if err := trashSession(m, belongs); err != nil && !errors.Is(err, ErrSessionLeftFolder) {
				if _, st := session.ReadMetaState(m.Name); st != session.MetaAbsent {
					settleErr = errors.Join(settleErr, fmt.Errorf("could not trash session %s: %w", m.Name, err))
				}
			}
		} else if err := deps.ShelveSession(m); err != nil {
			settleErr = errors.Join(settleErr, fmt.Errorf("could not shelve session %s: %w", m.Name, err))
		}
	}
	return settleErr
}
