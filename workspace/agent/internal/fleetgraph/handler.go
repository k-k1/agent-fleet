package fleetgraph

import (
	"encoding/json"
	"net/http"
	"strconv"
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
		http.Error(w, "fleet graph unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}
