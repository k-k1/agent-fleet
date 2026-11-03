package sessionx

import (
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// HandleSessionBackgroundStop is POST /sessions/{name}/background-stop {pid, start}: the
// mirror's Stop on one background shell (#3279). Esc only interrupts a turn; a
// run_in_background shell outlives it, and one that waits forever keeps the session
// background-busy (and, through the reaper, the workspace awake) until something kills it.
//
// No kind check here: claude.StopBackgroundShell only accepts a shell whose parent is a claude
// process under the session's tmux pane, re-read from a fresh process table. Any other kind,
// and a Managed session (no pane), answers "gone", and the body can name nothing outside this
// session's background shells.
func HandleSessionBackgroundStop(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	var req struct {
		PID   int    `json:"pid"`
		Start uint64 `json:"start"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.PID <= 1 {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_pid", "pid must name a background shell")
		return
	}
	if _, ok := session.ReadMeta(name); !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	if err := claude.StopBackgroundShell(name, req.PID, req.Start); err != nil {
		if errors.Is(err, claude.ErrBGShellGone) {
			httpx.WriteErr(w, http.StatusConflict, "gone", "that background shell is no longer running in this session")
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, "stop_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, bgStopResp{Stopped: req.PID})
}

// bgStopResp is the answer to a background-stop: the pid that was ended.
type bgStopResp struct {
	Stopped int `json:"stopped"`
}
