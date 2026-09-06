package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/resources"
)

// handleWorkspaceMachine (GET /workspace/machine) reports WHAT THIS WORKSPACE IS RUNNING
// ON — architecture, vCPU, the memory limit, the box's RAM, and on EC2 the instance type.
//
// Separate from /workspace/stats even though both read the same cgroup: stats is polled
// every 4 seconds by the CP's event tick, and this answer only changes when the container
// is recreated. Merging them would put an unchanging payload on a stream and blur
// "how much am I using" into "what am I on".
//
// The measurement is only half the picture. What the box WILL be at the next start is the
// CP's to say (the slot ladder lives there), so the CP merges this with its own declared
// answer; a field this cannot measure is omitted rather than zeroed, which is what lets
// the CP tell the two apart.
//
// Called by the CP only (GET /api/workspace/machine), like /workspace/stats, so it needs
// no entry in the Console's REST proxy allowlist.
func handleWorkspaceMachine(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, resources.ReadMachine())
}
