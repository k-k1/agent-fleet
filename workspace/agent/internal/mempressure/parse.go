// Package mempressure is phase 0 of ADR 0120: the Agent samples its own container cgroup's
// memory pressure and records it, and acts on none of it. Nothing here warns, refuses or stops a
// session.
//
// A signal that cannot be read is reported as unavailable, never as zero: "0% stalled" and "this
// cannot be measured here" are different statements, and a threshold fixed later from a series
// that mixes them would be fixed against fiction. Every figure is therefore a pointer, nil for
// "unknown".
package mempressure

import (
	"errors"
	"strconv"
	"strings"
)

// PSIWindow is one line (some or full) of memory.pressure. Percent of wall time stalled.
type PSIWindow struct {
	Avg10, Avg60 float64
	TotalUsec    uint64
}

// PSI is a parsed memory.pressure.
type PSI struct{ Some, Full PSIWindow }

// ParsePSI parses the cgroup v2 memory.pressure format:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=75
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=75
//
// Both lines and the avg10 / avg60 / total of each are required. A file that lacks any of them
// (an older kernel, a truncated read) is an error rather than a PSI with zeros in it.
func ParsePSI(s string) (PSI, error) {
	var p PSI
	var haveSome, haveFull bool
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		var w PSIWindow
		var got [3]bool
		for _, kv := range f[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "avg10":
				x, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return PSI{}, errors.New("memory.pressure: bad avg10")
				}
				w.Avg10, got[0] = x, true
			case "avg60":
				x, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return PSI{}, errors.New("memory.pressure: bad avg60")
				}
				w.Avg60, got[1] = x, true
			case "total":
				x, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return PSI{}, errors.New("memory.pressure: bad total")
				}
				w.TotalUsec, got[2] = x, true
			}
		}
		switch f[0] {
		case "some", "full":
			if !got[0] || !got[1] || !got[2] {
				return PSI{}, errors.New("memory.pressure: " + f[0] + " line incomplete")
			}
			if f[0] == "some" {
				p.Some, haveSome = w, true
			} else {
				p.Full, haveFull = w, true
			}
		}
	}
	if !haveSome || !haveFull {
		return PSI{}, errors.New("memory.pressure: missing some/full line")
	}
	return p, nil
}

// ParseKV parses a flat "key value" file (memory.stat, memory.events). Lines that are not a
// key and one unsigned number are skipped.
func ParseKV(s string) map[string]uint64 {
	m := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
			m[k] = n
		}
	}
	return m
}

// ParseRollup pulls Pss_Anon and Pss_Shmem (bytes) out of /proc/<pid>/smaps_rollup. Both must be
// present: Pss_Anon only exists on newer kernels, and a missing line means "not measurable", not
// zero.
func ParseRollup(s string) (anon, shmem uint64, err error) {
	var haveAnon, haveShmem bool
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || (k != "Pss_Anon" && k != "Pss_Shmem") {
			continue
		}
		f := strings.Fields(v)
		if len(f) != 2 || f[1] != "kB" {
			return 0, 0, errors.New("smaps_rollup: unexpected " + k + " line")
		}
		n, perr := strconv.ParseUint(f[0], 10, 64)
		if perr != nil {
			return 0, 0, errors.New("smaps_rollup: bad " + k)
		}
		if k == "Pss_Anon" {
			anon, haveAnon = n<<10, true
		} else {
			shmem, haveShmem = n<<10, true
		}
	}
	if !haveAnon || !haveShmem {
		return 0, 0, errors.New("smaps_rollup: no Pss_Anon / Pss_Shmem (kernel too old)")
	}
	return anon, shmem, nil
}
