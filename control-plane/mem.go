// mem.go — memory-size helpers for the per-workspace RAM cap (roadmap P3-4).
// Values are canonicalized to BYTES (int64) end to end; the runtime adapters
// format them per backend: docker takes a raw byte count for --memory, Fargate
// takes a task size (vCPU units + memory MiB) that must be a VALID combination.
package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Untyped so they flex to int64/uint64 at each use site (e.g. admin_stats' disk
// quota math). These are the package-wide 1024-based size constants.
const (
	kib = 1024
	mib = kib * 1024
	gib = mib * 1024
)

// parseMemBytes parses a human memory size into bytes. It accepts a bare integer
// (bytes, matching docker --memory) or a 1024-based suffix b/k/m/g/t (case- and
// spacing-insensitive, e.g. "512m", "8G", "2 GiB"). Returns ok=false on an empty or
// unparseable string so callers can fall back to a default rather than to 0.
func parseMemBytes(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, false
	}
	s = strings.TrimSuffix(s, "ib") // accept "gib"/"mib" as g/m
	mult := int64(1)
	switch s[len(s)-1] {
	case 'b':
		s = s[:len(s)-1]
	case 'k':
		mult, s = kib, s[:len(s)-1]
	case 'm':
		mult, s = mib, s[:len(s)-1]
	case 'g':
		mult, s = gib, s[:len(s)-1]
	case 't':
		mult, s = gib*1024, s[:len(s)-1]
	}
	s = strings.TrimSpace(s)
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return int64(n * float64(mult)), true
}

// mustMemBytes parses an env memory size, returning 0 (= "unset / no limit") for an
// empty or unparseable value — the safe default for optional operator ceilings.
func mustMemBytes(s string) int64 {
	if b, ok := parseMemBytes(s); ok {
		return b
	}
	return 0
}

// formatMemHuman renders bytes as the largest whole 1024-unit (e.g. "8g", "512m"),
// falling back to the byte count when it is not a clean multiple. Used for logs.
func formatMemHuman(b int64) string {
	switch {
	case b >= gib && b%gib == 0:
		return strconv.FormatInt(b/gib, 10) + "g"
	case b >= mib && b%mib == 0:
		return strconv.FormatInt(b/mib, 10) + "m"
	case b >= kib && b%kib == 0:
		return strconv.FormatInt(b/kib, 10) + "k"
	default:
		return strconv.FormatInt(b, 10)
	}
}

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

// fargateCPUUnits returns the valid Fargate CPU values, smallest first. Callers that
// present a choice (Console, MCP) use this so the offered sizes cannot be invalid.
func fargateCPUUnits() []int {
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
// returns ok=false — that is the ECS-managed EBS path (ADR 0044 決定 2), not a clamp,
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

// memClampNote is a short human explanation used in API/audit output when a requested
// value is clamped, so an admin sees why the effective cap differs from their input.
func memClampNote(requested, effective int64) string {
	if requested == effective {
		return ""
	}
	return fmt.Sprintf("clamped %s → %s", formatMemHuman(requested), formatMemHuman(effective))
}
