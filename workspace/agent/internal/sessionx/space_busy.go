package sessionx

import (
	"fmt"
	"net/http"

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
}

// spaceBusySession is the live AI session running in the space at dir, if any. A space has no
// branch and no worktree to split work into, so two agents in it edit the same files; nothing
// else keeps a schedule, an MCP client or a stale Console list from starting the second one.
//
// Shells do not count: a person's terminal in the folder is not a second writer racing the agent.
// Stopped and archived sessions do not count either: what is guarded is two processes at once.
// The comparison is workingCopyKey, the spawn guard's identity, so a symlinked spelling of the
// same folder still matches.
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
		if workingCopyKey(m.Dir) != target || !sessionAliveFn(m) {
			continue
		}
		return m, true
	}
	return session.Meta{}, false
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
	}})
}
