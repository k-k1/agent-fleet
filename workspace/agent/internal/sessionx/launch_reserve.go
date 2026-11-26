package sessionx

import (
	"net/http"
	"path/filepath"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Every path that starts a process for a session reserves the folder it runs in against a
// working-copy or space delete (gitx.ReserveLaunch) and holds the reservation until the process
// and the meta are published. Without it a delete that decides in between sees no running
// session, moves the folder away and settles the sessions it found, and the launch then runs in a
// folder that is gone (tmux falls back to home without a word) under a meta the delete never saw.

// reserveLaunch reserves the folder m will run in: its Dir, and the Subdir beneath it as stored
// (not Meta.CWD, which falls back to Dir when the Subdir is gone, as it is once a delete has moved
// it away).
//
// src says what is checked again under the gate. nil is a create: the folder, and the Subdir when
// there is one, must exist, since the create read them before a delete that has finished since.
// Otherwise src is the session the launch starts or derives from (a start, a fork's source, the
// slot a recreate replaces), as the caller read it: it is read again, and the launch is refused
// when a delete has settled it since (trashed, or shelved after its folder was moved away).
//
// It answers a refusal, with nothing held, or a release to call once the process and the meta are
// published (more than once is safe, and only the first waits for the launch gate).
func reserveLaunch(m session.Meta, src *session.Meta) (release func(), ref *SpawnRefusal) {
	return reserveLaunchChecked(m, src)
}

// launchUnrecorded is the src of a start whose session has no meta on disk (a Managed resume of a
// session that is still being created, or that a test holds in memory): only the marks are
// checked, as a missing meta proves nothing about a delete.
var launchUnrecorded = new(session.Meta)

func reserveLaunchChecked(m session.Meta, src *session.Meta) (release func(), ref *SpawnRefusal) {
	cwd := ""
	if m.Subdir != "" {
		cwd = filepath.Join(m.Dir, filepath.FromSlash(m.Subdir))
	}
	gitx.WithLaunchGate(func() {
		rel, ok := gitx.ReserveLaunch(m.Dir, cwd)
		if !ok {
			ref = &SpawnRefusal{Status: http.StatusConflict, Code: gitx.ErrCodeDeleteInProgress(),
				Message: "the folder of this session is being deleted, so it was not started"}
			return
		}
		switch {
		case src == launchUnrecorded:
		case src == nil:
			if !session.DirExists(m.Dir) || (cwd != "" && !session.DirExists(cwd)) {
				rel()
				missing := m.Dir
				if cwd != "" {
					missing = cwd
				}
				ref = &SpawnRefusal{Status: http.StatusBadRequest, Code: "bad_dir",
					Message: "dir does not exist: " + missing}
				return
			}
		case settledSince(*src):
			rel()
			ref = &SpawnRefusal{Status: http.StatusConflict, Code: gitx.ErrCodeDeleteInProgress(),
				Message: "the session was deleted or shelved while it was being started, so it was not started"}
			return
		}
		release = rel
	})
	if release == nil {
		return nil, ref
	}
	held := release
	var once sync.Once
	return func() { once.Do(func() { gitx.WithLaunchGate(held) }) }, nil
}

// settledSince reports whether the session snap was read as has been settled by a delete since:
// its meta is gone (a shell / ssm session is trashed with its folder), or it is shelved now and was
// not then. A session that was shelved already is not judged by its folder: a shelved shell whose
// folder was removed by hand starts at home, as it always has. Callers pass the earliest snapshot
// the request read, so a delete that finished before the launch read the meta again is still seen.
func settledSince(snap session.Meta) bool {
	cur, ok := session.ReadMeta(snap.Name)
	if !ok {
		return true
	}
	return cur.Archived && !snap.Archived
}
