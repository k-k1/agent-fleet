// fargate.go — the valid Fargate task sizes.
//
// This sits with its one reader, registerTaskDef on the Fargate adapter. The CP keeps
// the rest of mem.go (parseMemBytes and the 1024-based units) and aliases
// fargateCPUUnits for the callers that offer a size choice.
package runtime

import "strconv"

// fargateTier is one valid Fargate CPU size and the memory values it accepts.
// Within a tier the valid memory values are minMiB, minMiB+stepMiB, … up to maxMiB.
// The step is NOT uniform across tiers — 8 vCPU takes 4096 MiB steps and 16 vCPU
// takes 8192 — and the 0.25 vCPU tier is not an arithmetic sequence at all, so it
// carries its three values explicitly.
//
// The whole table was measured against the real ECS API rather than copied from
// documentation (docs/log/63 §63.2: RegisterTaskDefinition rejects an invalid pair with
// "No Fargate configuration exists for given values", and registering is free). The
// pre-measurement table assumed a uniform 1024 step, which produced INVALID sizes in
// the two top tiers — e.g. a 34 GiB cap became 8192/34816 and failed the whole Start.
type fargateTier struct {
	cpu                     string
	minMiB, maxMiB, stepMiB int64
	explicit                []int64 // when set, the only valid values (0.25 vCPU)
}

var fargateTiers = []fargateTier{
	{cpu: "256", minMiB: 512, maxMiB: 2048, explicit: []int64{512, 1024, 2048}},
	{cpu: "512", minMiB: 1024, maxMiB: 4096, stepMiB: 1024},
	{cpu: "1024", minMiB: 2048, maxMiB: 8192, stepMiB: 1024},
	{cpu: "2048", minMiB: 4096, maxMiB: 16384, stepMiB: 1024},
	{cpu: "4096", minMiB: 8192, maxMiB: 30720, stepMiB: 1024},
	{cpu: "8192", minMiB: 16384, maxMiB: 61440, stepMiB: 4096},
	{cpu: "16384", minMiB: 32768, maxMiB: 122880, stepMiB: 8192},
}

// snap returns the smallest memory value this tier accepts that is >= reqMiB, or
// ok=false when the tier cannot hold the request. A request below the tier's minimum
// is raised to it (Fargate has no "4 vCPU with 2 GiB").
func (t fargateTier) snap(reqMiB int64) (int64, bool) {
	if len(t.explicit) > 0 {
		for _, v := range t.explicit {
			if v >= reqMiB {
				return v, true
			}
		}
		return 0, false
	}
	if reqMiB > t.maxMiB {
		return 0, false
	}
	m := reqMiB
	if m < t.minMiB {
		m = t.minMiB
	}
	if r := (m - t.minMiB) % t.stepMiB; r != 0 {
		m += t.stepMiB - r
	}
	if m > t.maxMiB {
		return 0, false
	}
	return m, true
}

// FargateCPUUnits returns the valid Fargate CPU values, smallest first. Callers that
// present a choice (Console, MCP) use this so the offered sizes cannot be invalid.
func FargateCPUUnits() []int {
	out := make([]int, 0, len(fargateTiers))
	for _, t := range fargateTiers {
		n, _ := strconv.Atoi(t.cpu)
		out = append(out, n)
	}
	return out
}

// fargateSize snaps a requested (memory, CPU) onto the smallest VALID Fargate task
// size that holds both. cpuUnits is the per-workspace CPU request (0 = derive from
// memory, the pre-P1 behaviour); baseCPU is the operator's configured floor
// (AF_ECS_TASK_CPU). The chosen tier is never below either floor, so neither a memory
// bump nor an unset CPU quietly shrinks CPU — and asking for more CPU can RAISE the
// memory, because Fargate's bigger tiers have a memory minimum. Falls back to the
// largest tier if the request exceeds Fargate's ceiling. Returns strings as the ECS
// API expects them.
func fargateSize(memBytes int64, cpuUnits int, baseCPU string) (cpu, memoryMiB string) {
	reqMiB := (memBytes + mib - 1) / mib // ceil to MiB
	floor, _ := strconv.ParseInt(baseCPU, 10, 64)
	if int64(cpuUnits) > floor {
		floor = int64(cpuUnits)
	}
	for _, t := range fargateTiers {
		cpuVal, _ := strconv.ParseInt(t.cpu, 10, 64)
		if cpuVal < floor {
			continue // below the operator floor or the requested CPU
		}
		if m, ok := t.snap(reqMiB); ok {
			return t.cpu, strconv.FormatInt(m, 10)
		}
	}
	last := fargateTiers[len(fargateTiers)-1]
	return last.cpu, strconv.FormatInt(last.maxMiB, 10)
}

// Fargate's task ephemeral storage bounds, measured the same way as the size matrix
// (docs/log/63 §63.2): below 21 the API answers "EphemeralStorage size should be at least
// 21", above 200 "... at most 200". 20 GiB is what a task gets when the field is
// absent, and it is the only free amount — so "unset" must stay absent rather than
// being written as 20.
const (
	fargateDiskMinGiB = 21
	fargateDiskMaxGiB = 200
)

// fargateDiskGiB maps a requested per-workspace disk size onto what the ECS task
// definition should carry: an ephemeral-storage size, or 0 meaning "leave the field
// out" (deployment default 20 GiB, free). A request above the ephemeral ceiling
// returns ok=false — that is the ECS-managed EBS path (ADR 0044 decision 2), not a clamp,
// because silently cutting 500 GiB down to 200 would be a surprise.
func fargateDiskGiB(reqGiB int) (sizeGiB int32, needsEBS bool) {
	switch {
	case reqGiB <= fargateDiskMinGiB-1:
		return 0, false // unset, or no bigger than the free default → leave it absent
	case reqGiB > fargateDiskMaxGiB:
		return 0, true // too big for ephemeral → managed EBS
	default:
		return int32(reqGiB), false // 21–200
	}
}
