package fleetgraph

import (
	"net/http"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// HandleFleetGraph serves GET /api/fleet-graph?since=<ms>&until=<ms> (ADR 0096 decision 7).
// Registered directly in workspace/agent/routes.go and proxied through the control
// plane's explicit allowlist (control-plane/routes.go) — the CP does not pass requests
// through by default.
func HandleFleetGraph(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	until, _ := strconv.ParseInt(r.URL.Query().Get("until"), 10, 64)
	page, err := BuildPage(since, until)
	if err != nil {
		// Every other Agent handler answers an error as {code,message} (httpx.WriteErr) —
		// the Console's api() helper is built around that shape, so a plain http.Error
		// body here would surface as a reason-less failure.
		httpx.WriteErr(w, http.StatusInternalServerError, "fleet_graph_unavailable", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
