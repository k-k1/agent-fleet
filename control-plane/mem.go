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

// fargateTier is one valid Fargate CPU size and the memory range (MiB) it supports.
// The step within a tier is 1024 MiB up to 4 vCPU and coarser above, per the AWS
// Fargate task-size matrix; snapping up to a 1024 multiple stays valid in every tier.
type fargateTier struct {
	cpu           string
	minMiB, maxMiB int64
}

var fargateTiers = []fargateTier{
	{"256", 512, 2048},
	{"512", 1024, 4096},
	{"1024", 2048, 8192},
	{"2048", 4096, 16384},
	{"4096", 8192, 30720},
	{"8192", 16384, 61440},
	{"16384", 32768, 122880},
}

// fargateSize snaps a requested byte cap onto the smallest VALID Fargate (cpu,
// memoryMiB) pair whose memory is >= the request, keeping CPU as low as the memory
// allows (bumping it only when a smaller tier cannot hold the memory). baseCPU is the
// operator's configured floor (AF_ECS_TASK_CPU): the chosen CPU is never below it, so
// a memory bump never quietly shrinks CPU. Falls back to the largest tier if the
// request exceeds Fargate's ceiling. Returns strings as the ECS API expects them.
func fargateSize(memBytes int64, baseCPU string) (cpu, memoryMiB string) {
	reqMiB := (memBytes + mib - 1) / mib // ceil to MiB
	baseVal, _ := strconv.ParseInt(baseCPU, 10, 64)
	for _, t := range fargateTiers {
		cpuVal, _ := strconv.ParseInt(t.cpu, 10, 64)
		if cpuVal < baseVal {
			continue // never go below the operator's configured CPU
		}
		if reqMiB > t.maxMiB {
			continue // this tier can't hold the request; try a bigger one
		}
		m := reqMiB
		if m < t.minMiB {
			m = t.minMiB
		}
		if r := m % 1024; r != 0 { // snap up to a 1024 MiB multiple
			m += 1024 - r
		}
		if m > t.maxMiB {
			continue
		}
		return t.cpu, strconv.FormatInt(m, 10)
	}
	last := fargateTiers[len(fargateTiers)-1]
	return last.cpu, strconv.FormatInt(last.maxMiB, 10)
}

// memClampNote is a short human explanation used in API/audit output when a requested
// value is clamped, so an admin sees why the effective cap differs from their input.
func memClampNote(requested, effective int64) string {
	if requested == effective {
		return ""
	}
	return fmt.Sprintf("clamped %s → %s", formatMemHuman(requested), formatMemHuman(effective))
}
