// profiles.go — the two optional capabilities every adapter answers for itself:
// what the three size axes MEAN here (workspace_sizing.go 由来) and whether there is
// an AWS bill to show at all (cost_profile.go 由来).
//
// Both used to live next to the CP's handlers, with methods hung on the adapter types
// from the outside. That stopped being possible when the adapters moved into this
// package — Go will not let another package define methods on these types — so the
// declarations came with them. The CP keeps the halves that are genuinely its own (the
// two optional interfaces, the manager fallbacks, the HTTP handlers) and aliases the
// types declared here.
package runtime

// Values for WorkspaceSizing.MemMeaning / DiskMeaning. Kept as short strings rather
// than booleans because there are three disk answers, not two, and a boolean pair
// would have to be re-read every time a fourth runtime appears.
const (
	MemMeaningLimit = "limit" // a cap: docker --memory, Fargate task size
	MemMeaningSlot  = "slot"  // a requirement: pick the smallest box that holds it

	DiskMeaningWork  = "work"  // working disk, wiped when the workspace stops (Fargate)
	DiskMeaningHome  = "home"  // the persistent home volume itself (ecs-ec2)
	DiskMeaningQuota = "quota" // a reported number only; nothing enforces it (docker/native)
)

// WorkspaceSizing is the runtime's answer to "what do these three axes do here".
type WorkspaceSizing struct {
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
	//
	// ⚠️ With more than one class (below) this is the DEFAULT class's ladder. It stays
	// because a Console built before classes existed reads it, and because the common
	// deployment has exactly one class — SlotClasses then holds the same rungs once.
	Slots []WorkspaceSlot `json:"slots,omitempty"`
	// SlotClasses are the machine classes this deployment offers (docs/log/70 §70.4).
	// Absent when the deployment declared a single unnamed ladder, which is what every
	// existing AF_ECS_EC2_SLOT_TYPES value parses to — so a one-class deployment shows
	// no picker rather than a picker with one entry.
	SlotClasses []WorkspaceSlotClass `json:"slot_classes,omitempty"`
	// DefaultSlotClass is the id a member with no per-user and no per-tenant value
	// lands on.
	DefaultSlotClass string `json:"default_slot_class,omitempty"`
}

// WorkspaceSlotClass is one declared machine class: a display name, a CPU
// architecture and its own ladder.
//
// Label is the operator's words, not a generated one. The Console shows THIS, and
// `m7g.xlarge` only as the "you land on" detail — a tenant admin is choosing
// "省コスト（Arm）", not an EC2 instance family (docs/log/70 §70.10).
type WorkspaceSlotClass struct {
	ID    string          `json:"id"`
	Label string          `json:"label"`
	Arch  string          `json:"arch"` // x86_64 | arm64
	Slots []WorkspaceSlot `json:"slots"`
}

// WorkspaceSlot is one rung of the ladder. VCPU is 0 when the operator did not
// declare it (AF_ECS_EC2_SLOT_TYPES accepts type:memMiB and type:memMiB:vcpu) — the
// Console then simply does not print a vCPU count rather than guessing one.
type WorkspaceSlot struct {
	InstanceType string `json:"instance_type"`
	MemMiB       int64  `json:"mem_mib"`
	VCPU         int    `json:"vcpu,omitempty"`
	// UsableMemMiB is what the WORKSPACE gets, as opposed to what the box has: the
	// rung less the reserve held back for the box's own daemons (ADR 0045 決定 28).
	//
	// It exists because the two stopped being the same number. While the container was
	// uncapped, "8 GiB" described both the machine and the workspace and one figure was
	// honest; now the workspace's limit is lower, and printing only the box would tell
	// a member they have memory the cgroup will not give them. Omitted (0) when the
	// deployment runs uncapped, which is the one case where the box IS the answer.
	UsableMemMiB int64 `json:"usable_mem_mib,omitempty"`
}

// SizingProfile — docker: --memory and --cpus are real caps; the disk number is the
// display-only quota it has always been (nothing enforces it on a shared host).
func (f *dockerFactory) SizingProfile() WorkspaceSizing {
	return WorkspaceSizing{
		Runtime: "local", CPUEffective: true,
		MemMeaning: MemMeaningLimit, DiskMeaning: DiskMeaningQuota,
	}
}

// SizingProfile — native: containerless, so nothing is enforced at all (there is no
// cgroup to put a limit in). Reported honestly rather than pretending docker.
func (f *nativeFactory) SizingProfile() WorkspaceSizing {
	return WorkspaceSizing{
		Runtime: "native", CPUEffective: false,
		MemMeaning: MemMeaningLimit, DiskMeaning: DiskMeaningQuota,
	}
}

// SizingProfile — Fargate: all three axes reach the task definition, and the disk is
// the ephemeral working disk (free up to 20 GiB, wiped on stop).
func (f *ecsFactory) SizingProfile() WorkspaceSizing {
	def := f.cfg.diskGiB
	if def == 0 {
		def = 20 // Fargate's free default when the field is left out
	}
	return WorkspaceSizing{
		Runtime: "ecs", CPUEffective: true,
		MemMeaning: MemMeaningLimit, DiskMeaning: DiskMeaningWork,
		DiskDefaultGB: def,
	}
}

// SizingProfile — the EC2 slot pool. Memory picks a box, CPU is not used, and the
// disk number is the persistent home's EBS size, honoured only at creation.
func (f *ecsEC2Factory) SizingProfile() WorkspaceSizing {
	rungs := func(slots []ec2Slot) []WorkspaceSlot {
		out := make([]WorkspaceSlot, 0, len(slots))
		for _, s := range slots {
			out = append(out, WorkspaceSlot{
				InstanceType: s.instanceType, MemMiB: s.memMiB, VCPU: s.vcpu,
				UsableMemMiB: f.pool.workspaceMemCapMiB(s.memMiB),
			})
		}
		return out
	}
	p := WorkspaceSizing{
		Runtime: "ecs-ec2", CPUEffective: false,
		MemMeaning: MemMeaningSlot, DiskMeaning: DiskMeaningHome,
		DiskDefaultGB: int(f.pool.homeGiB), DiskCreateOnly: true,
		DefaultSlotClass: f.pool.defaultClass,
		Slots:            rungs(f.pool.classFor(f.pool.defaultClass).slots),
	}
	// A single unnamed ladder is reported the way it always was — no class list, so
	// the Console shows the memory chips and no picker. Offering a picker with one
	// entry would be a new question with only one possible answer (docs/log/70 §70.10).
	if len(f.pool.classes) > 1 {
		for _, c := range f.pool.classes {
			p.SlotClasses = append(p.SlotClasses, WorkspaceSlotClass{
				ID: c.id, Label: c.label, Arch: c.arch, Slots: rungs(c.slots),
			})
		}
	}
	return p
}

// --- cost -----------------------------------------------------------------------

// CostProfile is the runtime's answer to "is there money to show here, and what does it
// cover". Everything in it is a claim the Console is allowed to print.
type CostProfile struct {
	Runtime string `json:"runtime"`
	// Available is the whole gate: false = no cost surface anywhere in the UI.
	Available bool `json:"available"`
	// Attributable lists what actually carries `af-membership`, so the Console can name
	// what a member's number covers instead of implying it covers everything. Measured
	// on the reference deployment: what CAN be attributed is about a fifth of the bill
	// (docs/log/67 §67.3), and the rest is shared — never divided (ADR 0048 決定 4).
	Attributable []string `json:"attributable,omitempty"`
	// Shared lists the big cost centres that belong to nobody. Shown only to a
	// super_admin, but declared here so the member-facing hint can say what is EXCLUDED
	// without the Console hard-coding a list of AWS service names.
	Shared []string `json:"shared,omitempty"`
	// Verified is false where the tagging exists in code but has never been observed on
	// a real deployment (Fargate). A number nobody has ever seen arrive should not be
	// presented with the same confidence as one that has.
	Verified bool `json:"verified"`
}

// cost centre labels. Kept as stable identifiers rather than prose so the Console can
// translate them; the Console owns the wording in both languages.
const (
	costCentreSlotHours   = "slot_hours"   // EC2 instance-hours while a home is attached
	costCentreHomeVolume  = "home_volume"  // the member's persistent EBS home
	costCentreSnapshots   = "snapshots"    // hibernation + backup snapshots of that home
	costCentreTaskCompute = "task_compute" // Fargate task vCPU/GB-hours
	costCentreScratch     = "scratch"      // ECS-managed EBS working disk

	costCentreNAT      = "nat"       // the single biggest shared line, and untaggable
	costCentreDNS      = "dns"       // Route53 hosted zone + queries
	costCentreLB       = "lb"        // ALB
	costCentreDB       = "db"        // RDS
	costCentreEFS      = "efs"       // billed per filesystem, so it cannot be split
	costCentreIdlePool = "idle_pool" // warm slots nobody is holding
	costCentreCP       = "cp"        // the control plane's own task
	costCentreTax      = "tax"
)

// CostProfile — docker: the operator's own hardware. There is no invoice to read.
func (f *dockerFactory) CostProfile() CostProfile { return CostProfile{Runtime: "local"} }

// CostProfile — native: same, containerless.
func (f *nativeFactory) CostProfile() CostProfile { return CostProfile{Runtime: "native"} }

// CostProfile — Fargate. The task is the billed unit and it now inherits af-membership
// from its service, but ⚠️ this has never run against real Fargate: the deployment this
// was developed on is ecs-ec2 (ADR 0048 決定 9). Reported as unverified rather than
// quietly claimed.
func (f *ecsFactory) CostProfile() CostProfile {
	return CostProfile{
		Runtime: "ecs", Available: true, Verified: false,
		Attributable: []string{costCentreTaskCompute, costCentreScratch},
		Shared: []string{costCentreNAT, costCentreDNS, costCentreLB, costCentreDB,
			costCentreEFS, costCentreCP, costCentreTax},
	}
}

// CostProfile — the EC2 slot pool, the one that has been measured end to end. A slot is
// used by exactly one person while their home is attached (ADR 0045 決定 8), which is
// what makes instance-hours attributable at all; an unclaimed warm slot is shared, and
// showing that is the point rather than a caveat (it is the price of the pool size).
func (f *ecsEC2Factory) CostProfile() CostProfile {
	return CostProfile{
		Runtime: "ecs-ec2", Available: true, Verified: true,
		Attributable: []string{costCentreSlotHours, costCentreHomeVolume, costCentreSnapshots},
		Shared: []string{costCentreNAT, costCentreDNS, costCentreLB, costCentreDB,
			costCentreEFS, costCentreIdlePool, costCentreCP, costCentreTax},
	}
}
