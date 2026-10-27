package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mempressure"
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
// boot_install_failed rides here so the admin member list and detail can flag a start whose
// CLI install failed without a second per-member call: reading the status file is one stat and
// one read of at most 8 KiB, and nothing is read from home.
//
// The only caller is the CP (`/api/workspace/stats`, the stats stream on `/api/events`, and
// the admin member detail). It does not need adding to the control-plane proxy allowlist for
// direct Console calls: both CP entry points call this themselves.
//
// mem_pressure (ADR 0120 phase 0) is the latest memory-pressure sample. It is absent before the
// first sample, and present with available=false when pressure cannot be measured here.
func handleWorkspaceStats(w http.ResponseWriter, r *http.Request) {
	s := resources.Read()
	s.BootInstallFailed = bootInstallFailed()
	httpx.WriteJSON(w, http.StatusOK, workspaceStatsBody{Stats: s, MemPressure: mempressure.Current()})
}

// workspaceStatsBody is the stats wire: resources.Stats plus the axis that comes from the
// sampler rather than from a one-shot read.
type workspaceStatsBody struct {
	resources.Stats
	MemPressure *mempressure.Wire `json:"mem_pressure,omitempty"`
}
