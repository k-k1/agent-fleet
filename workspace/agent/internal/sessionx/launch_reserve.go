package sessionx

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Every path that starts a process for a session reserves the folder it runs in against a
// working-copy or space delete (gitx.ReserveLaunch) and holds the reservation until the process
// and the meta are published. Without it a delete that decides in between sees no running
// session, moves the folder away and settles the sessions it found, and the launch then runs in a
// folder that is gone (tmux falls back to home without a word) under a meta the delete never saw.

// launchFresh says the launch makes a new session (create, fork, recreate): its folder must exist
// when the reservation is taken, since a create that read it earlier can meet a delete that has
// finished since. launchExisting is a start of a session that has a meta: the meta is read again
// instead, and a session the delete trashed or shelved meanwhile is not started.
const (
	launchFresh    = true
	launchExisting = false
)

// reserveLaunch reserves m's folder for a launch. It answers a refusal, with nothing held, when a
// delete of the folder has decided, when a fresh launch's folder is gone, or when an existing
// session was settled by a delete since the caller read it. Otherwise release must be called once
// the process and the meta are published (more than once is safe).
func reserveLaunch(m session.Meta, fresh bool) (release func(), ref *SpawnRefusal) {
	WithDeletionGate(func() {
		cwd := m.CWD()
		rel, ok := gitx.ReserveLaunch(m.Name, m.Dir, cwd)
		if !ok {
			ref = &SpawnRefusal{Status: http.StatusConflict, Code: gitx.ErrCodeDeleteInProgress(),
				Message: "the folder of this session is being deleted, so it was not started"}
			return
		}
		if fresh {
			if !session.DirExists(m.Dir) {
				rel()
				ref = &SpawnRefusal{Status: http.StatusBadRequest, Code: "bad_dir",
					Message: "dir does not exist: " + m.Dir}
				return
			}
		} else if cur, ok := session.ReadMeta(m.Name); !ok || (cur.Archived && !m.Archived) {
			// A delete that finished since the caller read the meta has trashed the session (no
			// meta) or shelved it, after moving its folder away.
			rel()
			ref = &SpawnRefusal{Status: http.StatusConflict, Code: gitx.ErrCodeDeleteInProgress(),
				Message: "the session was deleted or shelved while it was being started, so it was not started"}
			return
		}
		release = rel
	})
	return release, ref
}
