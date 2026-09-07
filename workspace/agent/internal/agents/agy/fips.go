package agy

// The environment overlay that lets agy run on a host whose RDRAND the kernel has
// withdrawn. internal/hostcaps decides whether it is needed and holds the evidence; this is
// the seam every agy spawn site in the product goes through, so there is one thing to grep
// for and one thing to keep complete.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/hostcaps"

// MaskEnv is the overlay itself (nil on a normal host), for callers that inject the
// environment some other way than a cmd.Env slice: LaunchPlan.Env (tmux -e) and the
// version probe.
func MaskEnv() []string { return hostcaps.AgyRDRANDMask() }

// Env returns env plus the overlay, without touching env's backing array. Use it at every
// site that spawns agy — the login PTY, the /usage and /context scrapes, `agy models`, the
// assistant chat's `-p` runs. A site that misses it does not degrade on such a host, it
// SIGABRTs before printing anything.
func Env(env []string) []string {
	mask := MaskEnv()
	if len(mask) == 0 {
		return env
	}
	return append(append([]string(nil), env...), mask...)
}

// RDRANDMasked reports whether agy is running behind that mask here, for Status().
func RDRANDMasked() bool { return hostcaps.AgyRDRANDMasked() }
