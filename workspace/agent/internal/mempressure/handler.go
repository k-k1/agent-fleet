package mempressure

import (
	"net/http"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// HandleWindows (GET /workspace/usage-windows?since=<unix>&limit=<n>) serves the one-minute
// windows the sampler wrote after a cursor (ADR 0139 decision 3).
//
// It sits beside /workspace/stats and not in it because the Stats figures cannot be turned
// into minutes: CPU there is a delta since anyone's last call, PSI a point reading. These
// windows are fixed, tagged with the launch id and kept on home, so a caller that missed some
// (a stop between two polls, an Agent that was busy) reads them later from the same cursor.
//
// An Agent without the sampler (AF_MEM_PRESSURE=off) answers an empty page, not an error: no
// windows were measured, which the caller counts as unmeasured minutes.
//
// Called by the CP only, like /workspace/stats, so it needs no entry in the Console's REST
// proxy allowlist.
func HandleWindows(w http.ResponseWriter, r *http.Request) {
	var since int64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "since must be a unix second")
			return
		}
		since = n
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httpx.WriteErr(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
			return
		}
		limit = n
	}
	page, err := WindowsSince(since, limit)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "series_unreadable", "the usage series could not be read")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
