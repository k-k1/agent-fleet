package agents

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// BeginLoginChange is called by a login, key or disconnect handler before it lets a CLI replace
// or remove its login: it records the change durably (credbind.Begin), so a login whose new
// generation then fails to persist is never read back, even after a restart, as the one it
// replaced (ADR 0138 D5b). When nothing can be recorded it answers 500 and returns false,
// and the handler must not make the change.
func BeginLoginChange(w http.ResponseWriter, logins ...string) bool {
	if err := credbind.Begin(logins...); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "login_change_unrecorded",
			"the Agent could not record this login change in its config directory ("+err.Error()+"), so it did not make it")
		return false
	}
	return true
}
