// workspace_sizing.go — what the three size axes (memory / CPU / disk) MEAN on the
// runtime this deployment actually runs (ADR 0045 決定 21, docs/log/64 §64.27).
//
// The STORED shape does not change: three independent numbers, runtime-neutral
// (ADR 0044 決定 1). What changes is that the runtime now SAYS what a stored value
// becomes, so the Console can stop describing every deployment as if it were Fargate.
//
// It was describing Fargate everywhere, and on `ecs-ec2` two of the three axes were
// simply wrong: the CPU field is not read at all (fargateSize lives on the Fargate
// path; the EC2 task definition carries no cpu), and the disk field sizes the
// PERSISTENT home volume while the hint under it said "the working disk is wiped when
// the workspace stops". The memory field is subtler — the value is real, but it picks
// a slot rather than capping anything, because an EC2 slot is used by one person and
// the task reserves nothing (ADR 0045 決定 8).
package main

import (
	"net/http"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ⚠️ サイジングの語彙（runtime.MemMeaning* / DiskMeaning*）とその 3 型は adapters 側が
// 宣言する。この file はかつて 4 つの factory 型に SizingProfile() メソッドを生やしており、
// Go はそれを宣言元パッケージでしか許さない（internal/runtime/profiles.go）。
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
