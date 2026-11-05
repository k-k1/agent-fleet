package cloudruns

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// recentLimit is how many rows Settings' "Recent runs" shows.
const recentLimit = 100

// RecentWire is the body of GET /aws-runs.
type RecentWire struct {
	Runs []Row `json:"runs"`
}

// HandleRecent is GET /aws-runs: the newest runs of every session and of the workspace's
// spool, so a run whose session name was cleared or changed is still listed somewhere.
// ?provider=aws|gcp keeps one cloud's rows, before the limit: filtered after it, a hundred runs
// of one cloud would leave the other's tab empty.
func HandleRecent(w http.ResponseWriter, r *http.Request) {
	runs := RecentOf(r.URL.Query().Get("provider"), recentLimit)
	if runs == nil {
		runs = []Row{}
	}
	httpx.WriteJSON(w, http.StatusOK, RecentWire{Runs: runs})
}
