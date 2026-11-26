package sessionx

import (
	"net/http"
	"path/filepath"

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
// published (more than once is safe; never inside the deletion gate).
func reserveLaunch(m session.Meta, src *session.Meta) (release func(), ref *SpawnRefusal) {
	cwd := ""
	if m.Subdir != "" {
		cwd = filepath.Join(m.Dir, filepath.FromSlash(m.Subdir))
	}
	WithDeletionGate(func() {
		rel, ok := gitx.ReserveLaunch(m.Name, m.Dir, cwd)
		if !ok {
			ref = &SpawnRefusal{Status: http.StatusConflict, Code: gitx.ErrCodeDeleteInProgress(),
				Message: "the folder of this session is being deleted, so it was not started"}
			return
		}
		switch {
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
	return func() { WithDeletionGate(held) }, nil
}

// settledSince reports whether the session snap was read as has been settled by a delete since:
// its meta is gone (a shell / ssm session is trashed with its folder), or it is shelved now and was
// not then, or it is shelved with its folder gone (what a delete leaves, read by a caller that
// only read the meta after the delete was over).
func settledSince(snap session.Meta) bool {
	cur, ok := session.ReadMeta(snap.Name)
	if !ok {
		return true
	}
	return cur.Archived && (!snap.Archived || !session.DirExists(cur.Dir))
}
