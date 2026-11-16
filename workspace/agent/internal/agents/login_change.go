package agents

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// BeginLoginChange is called by a login, key or disconnect handler before it lets a CLI replace
// or remove its login: it records the change durably (credbind.Begin), so a login whose new
// generation then fails to persist is never read back, even after a restart, as the one it
// replaced (ADR 0138 D5b). When nothing can be recorded it answers 500 and returns false, and the
// handler must not make the change.
//
// The handler owns the returned operation until it ends: a synchronous handler defers its
// Finish, a flow hands it to Flow.LoginOp, whose Close finishes it on every path (completion,
// cancellation, the store's reap).
func BeginLoginChange(w http.ResponseWriter, logins ...string) (*credbind.Op, bool) {
	op, err := credbind.Begin(logins...)
	if err != nil {
		WriteLoginChangeUnrecorded(w, err)
		return nil, false
	}
	return op, true
}

// WriteLoginChangeUnrecorded is BeginLoginChange's answer when credbind.Begin failed, for a
// handler that began the change itself.
func WriteLoginChangeUnrecorded(w http.ResponseWriter, err error) {
	httpx.WriteErr(w, http.StatusInternalServerError, "login_change_unrecorded",
		"the Agent could not record this login change in its config directory ("+err.Error()+"), so it did not make it")
}
