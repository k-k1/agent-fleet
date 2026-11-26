package gitx

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// Which sessions live in a folder. A working-copy or space delete asks it at every step (the
// running and locked guards, the names its mark records, the shell sessions it trashes, the
// tombstone's shelf list, the shelving after the move), and so do the Console's counts
// (sessionx.LiveSessionsInDir, LockedSessionsInDir). With one rule a session cannot be refused by
// one step and missed by the next because of how its path is spelled.

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

// LivesIn is SessionLivesIn with dir resolved once, for a caller that tests many sessions.
func LivesIn(dir string) func(session.Meta) bool {
	if dir == "" {
		return func(session.Meta) bool { return false }
	}
	return newDirMatch(dir).has
}

type dirMatch struct{ raw, real string }

func newDirMatch(dir string) dirMatch {
	return dirMatch{raw: filepath.Clean(dir), real: launchRealPath(dir)}
}

func (d dirMatch) has(m session.Meta) bool {
	if m.Dir == "" {
		return false
	}
	if newLaunchPath(m.Dir).under(d.raw, d.real) {
		return true
	}
	// The Subdir as stored, not Meta.CWD, which falls back to Dir once the Subdir is gone: a
	// session started at home with its Subdir in a space lives in that space (reserveLaunch
	// reserves the same two paths).
	if m.Subdir == "" {
		return false
	}
	return newLaunchPath(filepath.Join(m.Dir, filepath.FromSlash(m.Subdir))).under(d.raw, d.real)
}

// DirSessions is one read of the sessions living in a folder, with whether each runs.
type DirSessions struct {
	members []session.Meta
	live    map[string]bool
}

// ReadDirSessions reads every session meta and tmux's running sessions once and keeps the sessions
// living in dir (SessionLivesIn). It fails rather than answer a short list when the sessions
// directory cannot be listed, when a meta file exists but cannot be read (it may be a session
// running or locked in dir), or when tmux cannot say what runs: a delete that took any of these
// for "nothing there" would remove a folder under a running or locked session.
func ReadDirSessions(dir string) (DirSessions, error) {
	metas, unreadable, err := session.ListMetasChecked()
	if err != nil {
		return DirSessions{}, fmt.Errorf("the sessions could not be listed: %w", err)
	}
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		return DirSessions{}, fmt.Errorf("the record of session(s) %s cannot be read, so whether they live here is unknown; repair or remove it (an Agent restart sets an unreadable record aside when its session is not running)",
			strings.Join(unreadable, ", "))
	}
	live, err := tmuxx.LiveSessionNamesChecked()
	if err != nil {
		return DirSessions{}, fmt.Errorf("could not tell which sessions are running: %w", err)
	}
	return collectDirSessions(metas, live, dir, nil), nil
}

// collectDirSessions keeps the metas living in dir, and those named in also whatever their path
// says now.
func collectDirSessions(metas []session.Meta, live map[string]bool, dir string, also map[string]bool) DirSessions {
	d := newDirMatch(dir)
	out := DirSessions{live: live}
	for _, m := range metas {
		if also[m.Name] || d.has(m) {
			out.members = append(out.members, m)
		}
	}
	return out
}

func (s DirSessions) alive(m session.Meta) bool {
	return s.live[m.Name] || (m.DriverKind() == session.DriverManaged && managedAlive(m))
}

// Running is the display names of the sessions running in the folder, archived ones aside.
func (s DirSessions) Running() []string {
	var out []string
	for _, m := range s.members {
		if !m.Archived && s.alive(m) {
			out = append(out, session.Display(m))
		}
	}
	sort.Strings(out)
	return out
}

// Locked is the display names of the deletion-locked sessions in the folder, running or not,
// archived included.
func (s DirSessions) Locked() []string {
	var out []string
	for _, m := range s.members {
		if m.Locked {
			out = append(out, session.Display(m))
		}
	}
	sort.Strings(out)
	return out
}

// Names is the names of every session in the folder: what a delete's mark records.
func (s DirSessions) Names() []string {
	out := make([]string, 0, len(s.members))
	for _, m := range s.members {
		out = append(out, m.Name)
	}
	return out
}

// ToSettle is the sessions a delete of the folder settles: not running, not locked, not already on
// the shelf.
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
