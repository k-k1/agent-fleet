// workspace_sizing.go — what the three size axes (memory / CPU / disk) MEAN on the
// runtime this deployment actually runs (ADR 0045 決定 21, docs/64 §64.27).
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

import "net/http"

// Values for workspaceSizing.MemMeaning / DiskMeaning. Kept as short strings rather
// than booleans because there are three disk answers, not two, and a boolean pair
// would have to be re-read every time a fourth runtime appears.
const (
	memMeaningLimit = "limit" // a cap: docker --memory, Fargate task size
	memMeaningSlot  = "slot"  // a requirement: pick the smallest box that holds it

	diskMeaningWork  = "work"  // working disk, wiped when the workspace stops (Fargate)
	diskMeaningHome  = "home"  // the persistent home volume itself (ecs-ec2)
	diskMeaningQuota = "quota" // a reported number only; nothing enforces it (docker/native)
)

// workspaceSizing is the runtime's answer to "what do these three axes do here".
type workspaceSizing struct {
	Runtime string `json:"runtime"`
	// CPUEffective is false when the CPU axis is stored but never reaches the
	// backend. A field that does nothing is worse than a missing field, so the
	// Console hides it rather than showing it greyed out.
	CPUEffective bool   `json:"cpu_effective"`
	MemMeaning   string `json:"mem_meaning"`
	DiskMeaning  string `json:"disk_meaning"`
	// DiskDefaultGB is what 0 on the disk axis actually gives (0 = "the backend's own
	// default", which is all docker/native can say).
	DiskDefaultGB int `json:"disk_default_gb"`
	// DiskCreateOnly: the value is read when the volume is created and never again.
	// ecs-ec2 has no ModifyVolume call, and EBS cannot shrink in any case.
	DiskCreateOnly bool `json:"disk_create_only"`
	// Slots is the ladder a memory request lands on, ascending. Only ecs-ec2 has one;
	// everywhere else it is absent and the Console shows no box.
	Slots []workspaceSlot `json:"slots,omitempty"`
}

// workspaceSlot is one rung of the ladder. VCPU is 0 when the operator did not
// declare it (AF_ECS_EC2_SLOT_TYPES accepts type:memMiB and type:memMiB:vcpu) — the
// Console then simply does not print a vCPU count rather than guessing one.
type workspaceSlot struct {
	InstanceType string `json:"instance_type"`
	MemMiB       int64  `json:"mem_mib"`
	VCPU         int    `json:"vcpu,omitempty"`
}

// sizingProfiler is the optional RuntimeFactory capability. Same shape as
// WorkspaceImage(): adapters that have nothing special to say don't implement it.
type sizingProfiler interface {
	SizingProfile() workspaceSizing
}

// workspaceSizing reports the deployment's profile, falling back to the historical
// docker/Fargate description for an adapter that does not declare one.
func (m *manager) workspaceSizing() workspaceSizing {
	if f, ok := m.rtFactory.(sizingProfiler); ok {
		return f.SizingProfile()
	}
	return workspaceSizing{
		Runtime: "local", CPUEffective: true,
		MemMeaning: memMeaningLimit, DiskMeaning: diskMeaningQuota,
	}
}

// SizingProfile — docker: --memory and --cpus are real caps; the disk number is the
// display-only quota it has always been (nothing enforces it on a shared host).
func (f *dockerFactory) SizingProfile() workspaceSizing {
	return workspaceSizing{
		Runtime: "local", CPUEffective: true,
		MemMeaning: memMeaningLimit, DiskMeaning: diskMeaningQuota,
	}
}

// SizingProfile — native: containerless, so nothing is enforced at all (there is no
// cgroup to put a limit in). Reported honestly rather than pretending docker.
func (f *nativeFactory) SizingProfile() workspaceSizing {
	return workspaceSizing{
		Runtime: "native", CPUEffective: false,
		MemMeaning: memMeaningLimit, DiskMeaning: diskMeaningQuota,
	}
}

// SizingProfile — Fargate: all three axes reach the task definition, and the disk is
// the ephemeral working disk (free up to 20 GiB, wiped on stop).
func (f *ecsFactory) SizingProfile() workspaceSizing {
	def := f.cfg.diskGiB
	if def == 0 {
		def = 20 // Fargate's free default when the field is left out
	}
	return workspaceSizing{
		Runtime: "ecs", CPUEffective: true,
		MemMeaning: memMeaningLimit, DiskMeaning: diskMeaningWork,
		DiskDefaultGB: def,
	}
}

// SizingProfile — the EC2 slot pool. Memory picks a box, CPU is not used, and the
// disk number is the persistent home's EBS size, honoured only at creation.
func (f *ecsEC2Factory) SizingProfile() workspaceSizing {
	slots := make([]workspaceSlot, 0, len(f.pool.slotSizes))
	for _, s := range f.pool.slotSizes {
		slots = append(slots, workspaceSlot{InstanceType: s.instanceType, MemMiB: s.memMiB, VCPU: s.vcpu})
	}
	return workspaceSizing{
		Runtime: "ecs-ec2", CPUEffective: false,
		MemMeaning: memMeaningSlot, DiskMeaning: diskMeaningHome,
		DiskDefaultGB: int(f.pool.homeGiB), DiskCreateOnly: true,
		Slots: slots,
	}
}

// workspaceSizingHandler (GET /api/admin/workspace-sizing) — read-only description of
// this deployment's size axes. Any signed-in identity may read it: the member detail
// that consumes it is tenant_admin-only, but the instance type a person's own
// workspace runs on is visible from inside that workspace anyway (nproc, cgroup), so
// gating it would protect nothing while forcing the Console to guess.
func (a adminAPI) workspaceSizingProfile(w http.ResponseWriter, _ *http.Request, _ Identity) {
	writeJSON(w, http.StatusOK, a.mgr.workspaceSizing())
}
