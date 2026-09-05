// Package hostcaps detects host CPU / runtime-environment capabilities. It gathers in one
// place the checks that hide agent kinds the host cannot run from the Console's selector
// (the capability guard, docs/log/32 Track B).
//
// The only kind covered so far is agy (Antigravity CLI, kind="agy"): agy is a Go
// BoringCrypto (FIPS) build whose FIPS random module requires the RDRAND instruction on
// x86. On a host that does not expose RDRAND (masked by the kernel or disabled in the
// BIOS; a real example is the AMD Ryzen Embedded R2514) the CRNGT self-test SIGABRTs right
// after launch and there is no way around it from user space (docs/decisions/0008 PoC).
// Rather than launching it only to have it die, detect it here beforehand and stop
// exposing the kind at all.
package hostcaps

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

const cpuinfoPath = "/proc/cpuinfo"

// RDRAND reports whether the host CPU exposes the RDRAND instruction
// (the "rdrand" flag in /proc/cpuinfo). Result is cached for the process
// lifetime — CPU flags cannot change under a running container.
// Non-x86 hosts (no "flags" lines) report false; callers that only need
// RDRAND as an x86 FIPS requirement must gate on GOARCH themselves (see
// AgyStatus).
var RDRAND = sync.OnceValue(func() bool {
	b, err := os.ReadFile(cpuinfoPath)
	if err != nil {
		// Better to expose the kind and let real behaviour decide than to hide it on a
		// false negative where cpuinfo is unreadable — which happens only in non-Linux
		// test environments, never in the fleet.
		return true
	}
	return rdrandInCPUInfo(string(b))
})

// rdrandInCPUInfo reports whether the flags line of /proc/cpuinfo text carries the rdrand
// flag as a whole word. Never a substring match: the flag vocabulary is an exact
// space-separated set.
func rdrandInCPUInfo(cpuinfo string) bool {
	for line := range strings.Lines(cpuinfo) {
		key, vals, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "flags" {
			continue
		}
		for _, f := range strings.Fields(vals) {
			if f == "rdrand" {
				return true
			}
		}
	}
	return false
}

// AgyStatus reports whether the agy kind is runnable on this host, with a
// machine-readable reason when it is not:
//
//	supported=false reason="not_installed" — agy binary absent (image without the
//	                                         bake; PATH prefers ~/.local/bin too)
//	supported=false reason="no_rdrand"     — x86 host without RDRAND (agy would
//	                                         SIGABRT at launch)
//	supported=true  reason=""
//
// agy.Status() (the "agy" field of GET /connections) carries this through as supported /
// reason, and the Console leaves a supported=false kind out of the selector. Session
// creation refuses on the same check (docs/log/32).
func AgyStatus() (supported bool, reason string) {
	if _, err := exec.LookPath("agy"); err != nil {
		return false, "not_installed"
	}
	// The RDRAND requirement is specific to x86's FIPS random module (0008); it is not
	// imposed on arm64 and the like.
	//
	// Measured (docs/log/70 §70.13): `agy --version` and `agy --help` exit 0 in a Debian 12
	// container on three Graviton generations (m8g=Graviton4 / m7g=Graviton3 /
	// m6g=Neoverse-N1). m6g is the decisive one — it works even though /proc/cpuinfo has no
	// `rng` (ARMv8.5-RNG, i.e. RNDR, the counterpart of x86's rdrand), so arm64's
	// BoringCrypto FIPS randomness comes from the kernel's getrandom(2) rather than an
	// instruction. This branch is a measurement, not an assumption.
	if runtime.GOARCH == "amd64" && !RDRAND() {
		return false, "no_rdrand"
	}
	return true, ""
}
