package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/resources"
)

// handleWorkspaceStats (GET /workspace/stats) returns memory / CPU / disk for *this
// Workspace itself* (docs/log/63 §63.9).
//
// The CP first tries to read the host-side cgroup directly (which only works on the docker
// and native profiles) and falls through to here when it cannot. On ECS (Fargate and
// `ecs-ec2`) this is always the route that answers.
//
// An axis that could not be read is omitted key and all. The CP forwards only the keys it
// got back, so "cannot measure" stays a "-" in the Console tile - it is never drawn as 0.
//
// The only caller is the CP (`/api/workspace/stats`, the stats stream on `/api/events`, and
// the admin member detail). It does not need adding to the control-plane proxy allowlist for
// direct Console calls: both CP entry points call this themselves.
func handleWorkspaceStats(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, resources.Read())
}
