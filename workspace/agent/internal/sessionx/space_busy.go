package sessionx

import (
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// errCodeSpaceBusy answers a launch into an existing space where an AI session is running. The
// Console reads it to ask "start in a new space, or here anyway"; other clients see the message.
const errCodeSpaceBusy = "space_busy"

// spaceBusyError is the body of the space_busy refusal. Beyond the stable code it names the
// running session, because the only useful next step is to pick another place or to say "here
// anyway" knowing who else is there.
type spaceBusyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Session string `json:"session"`
	Title   string `json:"title,omitempty"`
	// Display is the session's name as shown now (session.Display): the caller's own list can
	// predate a rename, or a title that was cleared.
	Display string `json:"display"`
}

// spaceBusySession is the live AI session running in the space at dir, if any. A space has no
// branch and no worktree to split work into, so two agents in it edit the same files. The create
// asks it for a regular (not session-spawned) AI session that names an existing space by
// space.folder, whichever client sent it; a create that sends dir is not checked here.
//
// Shells do not count: a person's terminal in the folder is not a second writer racing the agent.
// Stopped and archived sessions do not count either: what is guarded is two processes at once.
// The space side is compared by workingCopyKey, the spawn guard's identity, and each session by
// sessionRootKey, the place its process really entered.
func spaceBusySession(dir string) (session.Meta, bool) {
	target := workingCopyKey(dir)
	for _, m := range session.ListMetas() {
		if m.Archived || m.Dir == "" {
			continue
		}
		switch NormalizeKind(m.Kind) {
		case session.KindShell, session.KindSSM:
			continue
		}
		if sessionRootKey(m) != target || !sessionAliveFn(m) {
			continue
		}
		return m, true
	}
	return session.Meta{}, false
}

// sessionRootKey is the working copy m's process runs in, by the rule Meta.CWD launches with. With
// a Subdir the path is Joined first, which cleans `link/..` lexically, so the cleaned Dir is the
// root (workingCopyKey). Without one the raw Dir is entered and the kernel takes `..` after
// following the link, so the raw spelling is resolved as it is. Meta.Dir keeps whatever spelling the
// create used, and only this decides which of the two places a `link/..` Dir names.
//
// Decided by the stored Subdir alone, never by whether that folder exists now: a running
// session's folder can be renamed under it, and its space must not change with that.
func sessionRootKey(m session.Meta) string {
	if m.Subdir == "" {
		if real, err := filepath.EvalSymlinks(m.Dir); err == nil {
			if abs, err := filepath.Abs(real); err == nil {
				return abs
			}
		}
	}
	return workingCopyKey(m.Dir)
}

// writeSpaceBusy answers 409 space_busy naming m.
func writeSpaceBusy(w http.ResponseWriter, folder string, m session.Meta) {
	who := m.Name
	if m.Title != "" {
		who = fmt.Sprintf("%s (%s)", m.Title, m.Name)
	}
	httpx.WriteJSON(w, http.StatusConflict, struct {
		Error spaceBusyError `json:"error"`
	}{spaceBusyError{
		Code: errCodeSpaceBusy,
		Message: fmt.Sprintf("session %s is already running in space %q; start in a new space, "+
			"or send space.allow_busy=true to start here anyway", who, folder),
		Session: m.Name,
		Title:   m.Title,
		Display: session.Display(m),
	}})
}
