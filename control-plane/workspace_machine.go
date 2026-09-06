// workspace_machine.go — "what is my workspace running on", for the member themselves.
//
// The three axes the Console already had (memory / CPU / disk) say how much is being USED.
// Nothing said what the machine IS, and on `ecs-ec2` — where a member gets an EC2 box to
// themselves whose type, architecture, cores and RAM follow from their stored size and
// class — that turned out to be the thing people needed and could not get: whether they
// are on arm64 (rtk is not installed there, JDKs are per-architecture), and how much memory
// a build may actually take before the kernel kills it.
//
// Two sources, deliberately kept apart:
//
//	measured  the workspace itself, from inside its own container (the Agent's
//	          /workspace/machine → workspace/agent/internal/resources). The truth about
//	          what is running RIGHT NOW, and unavailable while the workspace is stopped.
//	declared  this deployment's configuration: the rung the member's memory request and
//	          class resolve to (runtime.WorkspaceMachine). Always available, and it is what
//	          the NEXT start will use.
//
// They can legitimately disagree — a size or class change applies at the next start, so a
// running workspace keeps its old box — and that disagreement is exactly what someone
// asking "why is this still slow" needs to see. Merging them into one number would answer
// that question wrongly; the Console shows both when they differ.
package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// machineProfiler is the optional Runtime capability, probed the way BootPhase() is. Only
// the EC2 slot pool implements it; a runtime with no box to name declares nothing rather
// than describing itself as if it had one.
type machineProfiler interface {
	MachineProfile() runtime.WorkspaceMachine
}

// workspaceMachineWire is the response of GET /api/workspace/machine.
type workspaceMachineWire struct {
	// Runtime is the deployment's runtime id (ecs-ec2 / ecs / docker / native), which is
	// what decides which rows mean anything on screen.
	Runtime string `json:"runtime"`
	// Running: the measured half is only obtainable while the container is up, so the
	// Console needs to distinguish "stopped, this is what you will get" from "running but
	// unmeasurable".
	Running  bool                      `json:"running"`
	Measured *machineMeasured          `json:"measured,omitempty"`
	Declared *runtime.WorkspaceMachine `json:"declared,omitempty"`
}

// machine (GET /api/workspace/machine) — the caller's OWN workspace only; there is no
// membership to choose, since withResolved already resolved the one they own.
//
// Not gated beyond being signed in, for the same reason /api/admin/workspace-sizing is not:
// the instance type of your own workspace is readable from inside that workspace anyway
// (nproc, the cgroup, SMBIOS), so hiding it here would protect nothing while forcing the
// Console to guess.
func (a workspaceAPI) machine(w http.ResponseWriter, r *http.Request, res *resolved) {
	ctx := r.Context()
	out := workspaceMachineWire{
		Runtime: a.mgr.workspaceSizing().Runtime,
		Running: res.rt.State(ctx) == "running",
	}
	if mp, ok := res.rt.(machineProfiler); ok {
		d := mp.MachineProfile()
		out.Declared = &d
	}
	// Asking a stopped workspace would only burn the client's timeout; the declared half
	// still answers "what will I start on", which is the useful thing to know while it is
	// down.
	if out.Running {
		if m, err := a.mgr.agentMachine(ctx, res.rt); err == nil {
			out.Measured = m
		}
	}
	if out.Measured != nil && (out.Declared == nil || !out.Declared.Dedicated) {
		// Everything the container reads about the BOX (/proc/meminfo, SMBIOS) is the
		// host's, not this workspace's share of it. Where the box is shared — docker on a
		// fleet host — those are other members' numbers, and labelling them "your machine"
		// would be wrong rather than merely imprecise. The cgroup-derived fields (mem_max,
		// cpu_quota) and the architecture describe this container and are kept.
		out.Measured.MemTotal = nil
		out.Measured.InstanceType = ""
	}
	writeJSON(w, http.StatusOK, out)
}
