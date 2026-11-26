package gitx

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

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
	return dirMatch{raw: filepath.Clean(dir), real: launchRealPath(dir), key: markKey(dir)}
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

// ReadDirSessions reads every session meta and tmux's running sessions once and keeps the sessions
// in dir. It fails rather than answer a short list when the sessions directory cannot be listed,
// when a meta file exists but cannot be read (it may be a session running or locked in dir), or
// when tmux cannot say what runs: a delete that took any of these for "nothing there" would remove
// a folder under a running or locked session.
func ReadDirSessions(dir string) (DirSessions, error) {
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

// TrashShellsBeforeRemoval moves the stopped shell / ssm sessions among settle to the trash before
// dir is removed, so a trash that fails (a full disk) stops the delete with nothing removed rather
// than leave a session listed with no folder. Each is read again first: one deleted on its own
// since the list was read is left out (nothing is left listed), and one that no longer lives in dir
// (a link it is spelt through was re-pointed) is left alone. It answers the session that could not
// be trashed, and why.
func TrashShellsBeforeRemoval(dir string, settle []session.Meta) (failed string, err error) {
	in := newDirMatch(dir)
	for _, m := range settle {
		if m.Kind != session.KindShell && m.Kind != session.KindSSM {
			continue
		}
		cur, st := session.ReadMetaState(m.Name)
		if st == session.MetaAbsent || (st == session.MetaOK && !in.has(cur)) {
			continue
		}
		if err := trashSession(m); err != nil {
			if _, st := session.ReadMetaState(m.Name); st == session.MetaAbsent {
				continue
			}
			return m.Name, err
		}
	}
	return "", nil
}

// settleAfterRemoval settles the stopped sessions of dir once it has been removed or moved: AI
// sessions to the shelf, a shell / ssm one that appeared since the trash step to the trash. Each
// is taken as its meta is now and only while it still lives in dir (a link into the folder is
// followed even once it dangles), so one re-pointed elsewhere meanwhile stays where it is. When
// tmux cannot be asked, the sessions decided before the removal (before) are settled all the same,
// since the mark keeps every launch out of dir (ReserveLaunch) and they are still stopped; only the
// search for others is given up.
func settleAfterRemoval(dir string, before []session.Meta) {
	live, err := tmuxx.LiveSessionNamesChecked()
	var metas []session.Meta
	if err != nil {
		log.Printf("delete %s: could not tell which sessions run, so only the sessions decided before the removal are settled: %v", dir, err)
		live = map[string]bool{}
		for _, b := range before {
			if m, ok := session.ReadMeta(b.Name); ok {
				metas = append(metas, m)
			}
		}
	} else {
		// An unreadable meta is left out: nothing can be settled for a record that cannot be read.
		metas = session.ListMetas()
	}
	d := newDirMatch(dir)
	s := DirSessions{live: live}
	for _, m := range metas {
		if d.has(m) {
			s.members = append(s.members, m)
		}
	}
	for _, m := range s.ToSettle() {
		if m.Kind == session.KindShell || m.Kind == session.KindSSM {
			if err := trashSession(m); err != nil {
				log.Printf("delete %s: could not move %s to the trash, leaving it listed: %v", dir, m.Name, err)
			}
			continue
		}
		shelveSession(m)
	}
}
