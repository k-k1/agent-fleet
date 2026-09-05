// workspace_sizing.go — what the three size axes (memory / CPU / disk) MEAN on the
// runtime this deployment actually runs (ADR 0045 decision 21, docs/log/64 §64.27).
//
// The STORED shape does not change: three independent numbers, runtime-neutral
// (ADR 0044 decision 1). What changes is that the runtime now SAYS what a stored value
// becomes, so the Console can stop describing every deployment as if it were Fargate.
//
// It was describing Fargate everywhere, and on `ecs-ec2` two of the three axes were
// simply wrong: the CPU field is not read at all (fargateSize lives on the Fargate
// path; the EC2 task definition carries no cpu), and the disk field sizes the
// PERSISTENT home volume while the hint under it said "the working disk is wiped when
// the workspace stops". The memory field is subtler — the value is real, but it picks
// a slot rather than capping anything, because an EC2 slot is used by one person and
// the task reserves nothing (ADR 0045 decision 8).
package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The sizing vocabulary (runtime.MemMeaning* / DiskMeaning*) and its three types are
// declared alongside the adapters in internal/runtime/profiles.go: the SizingProfile()
// methods hang off the factory types, and Go only allows that in their own package.
//
// sizingProfiler is the optional RuntimeFactory capability. Same shape as
// WorkspaceImage(): adapters that have nothing special to say don't implement it.
type sizingProfiler interface {
	SizingProfile() runtime.WorkspaceSizing
}

// workspaceSizing reports the deployment's profile, falling back to the historical
// docker/Fargate description for an adapter that does not declare one.
func (m *manager) workspaceSizing() runtime.WorkspaceSizing {
	if f, ok := m.rtFactory.(sizingProfiler); ok {
		return f.SizingProfile()
	}
	return runtime.WorkspaceSizing{
		Runtime: "local", CPUEffective: true,
		MemMeaning: runtime.MemMeaningLimit, DiskMeaning: runtime.DiskMeaningQuota,
	}
}

// workspaceSizingHandler (GET /api/admin/workspace-sizing) — read-only description of
// this deployment's size axes. Any signed-in identity may read it: the member detail
// that consumes it is tenant_admin-only, but the instance type a person's own
// workspace runs on is visible from inside that workspace anyway (nproc, cgroup), so
// gating it would protect nothing while forcing the Console to guess.
func (a adminAPI) workspaceSizingProfile(w http.ResponseWriter, _ *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.mgr.workspaceSizing())
}
