// Package hostcaps detects host CPU / runtime-environment capabilities. It gathers in one
// place the checks that hide agent kinds the host cannot run from the Console's selector
// (the capability guard, docs/log/32 Track B), and the environment a child process needs to
// run there after all.
//
// The only kind covered so far is agy (Antigravity CLI, kind="agy"), a Go BoringCrypto
// (FIPS) build that SIGABRTs at launch with "CRNGT failed" on the development host of
// docs/decisions/0008 (AMD Ryzen Embedded R2514). The cause is a disagreement about one CPU
// feature rather than a missing one — all three steps measured on that host, 2026-09-07:
//
//   - CPUID.1:ECX bit 30 says RDRAND is present, so a library doing its own detection (as
//     BoringCrypto does) reaches for the instruction.
//   - The instruction is stuck: it returns 0xffffffffffffffff every time with the carry
//     flag set, i.e. it reports success while handing back a constant. That is the AMD
//     RDRAND errata, and it is exactly why the kernel clears the flag from /proc/cpuinfo
//     while leaving CPUID alone.
//   - CRNGT is the continuous RNG self-test, which rejects a block equal to the previous
//     one. Fed a constant it fires on the first call and the module aborts. The self-test
//     is doing its job; the entropy source is the broken part.
//
// So this host has no usable hardware RNG, and the answer is to tell OpenSSL's detection
// what the kernel already tells every other consumer: clear the bit (OPENSSL_ia32cap).
// Randomness then comes from the kernel CSPRNG, and agy runs — measured here through
// `agy --version`, a full OAuth login and `agy models`. AgyRDRANDMask is the single place
// that mask is built; the gate below is what decides whether it is needed at all.
package hostcaps

import (
	"context"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const cpuinfoPath = "/proc/cpuinfo"

// rdrandMask clears CPUID.1:ECX bit 30 (RDRAND) from OpenSSL's own feature detection.
// OPENSSL_ia32cap's "~" form is a mask of bits to drop, and 0x4000000000000000 is bit 30 of
// the second word (ECX). Nothing else about the process changes.
const rdrandMask = "OPENSSL_ia32cap=~0x4000000000000000"

// RDRAND reports whether the host CPU exposes the RDRAND instruction
// (the "rdrand" flag in /proc/cpuinfo). Result is cached for the process
// lifetime — CPU flags cannot change under a running container.
// Non-x86 hosts (no "flags" lines) report false; callers that only need
// RDRAND as an x86 FIPS requirement must gate on GOARCH themselves (see
// AgyStatus).
//
// Reading the kernel's view rather than CPUID is deliberate: CPUID still advertises the
// instruction on the host this exists for. What matters is whether the instruction can be
// TRUSTED, and the kernel dropping the flag is the signal for that.
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

// AgyRDRANDMask returns the environment overlay every agy child process needs on this host:
// nil where the CPU's RDRAND can be trusted (and on architectures that never wanted it), one
// OPENSSL_ia32cap assignment where it cannot. Appending nil is a no-op, so a spawn site adds
// it unconditionally — and it has to be EVERY spawn site, because a missed one does not
// degrade, it SIGABRTs.
//
// The overlay is self-limiting by construction: a host whose /proc/cpuinfo advertises rdrand
// never gets it, so no deployment that runs agy today changes behaviour. A deployment for
// which the FIPS module's own entropy path is a hard requirement sets AF_AGY_RDRAND_MASK=0,
// and agy goes back to being hidden here rather than run with the mask (docs/decisions/0008).
func AgyRDRANDMask() []string {
	if !agyMaskNeeded() {
		return nil
	}
	return []string{rdrandMask}
}

// AgyRDRANDMasked reports whether agy actually runs behind the mask on this host — for the
// Console's agy card, which says so rather than leaving the substitution silent.
func AgyRDRANDMasked() bool { return agyMaskNeeded() && agyStartsMasked() }

// agyMaskNeeded is the host gate: an x86 host whose kernel has withdrawn RDRAND, unless the
// operator refused the mask.
func agyMaskNeeded() bool {
	return runtime.GOARCH == "amd64" && !RDRAND() && os.Getenv("AF_AGY_RDRAND_MASK") != "0"
}

// agyStartsMasked answers "does agy start here with the mask applied" by starting it. On a
// host with no usable RDRAND the mask is the whole reason the kind is runnable, so that
// claim is measured rather than assumed: should a future agy build reach for the instruction
// by another route, the kind goes back to being hidden instead of dying at launch in front
// of the user. One `agy --version` costs ~0.2s and the answer is cached for the process
// lifetime (CPU flags and the mask decision cannot change under a running container).
//
// Only ever reached with the binary present (AgyStatus checks PATH first), so a false here
// means the mask failed, never that there was nothing to run.
var agyStartsMasked = sync.OnceValue(func() bool {
	if !agyMaskNeeded() {
		// Reached only when the operator set AF_AGY_RDRAND_MASK=0 on a host that needs it:
		// say so, or the kind silently disappears with no way to tell why.
		log.Printf("agy: this host has no usable RDRAND and AF_AGY_RDRAND_MASK=0 refuses the workaround — the kind stays hidden")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "agy", "--version")
	cmd.Env = append(os.Environ(), rdrandMask)
	if err := cmd.Run(); err != nil {
		log.Printf("agy: this host has no usable RDRAND and %s did not make it start (%v) — the kind stays hidden", rdrandMask, err)
		return false
	}
	log.Printf("agy: this host's RDRAND is withdrawn by the kernel; every agy process runs with %s, so its randomness comes from the kernel CSPRNG rather than the CPU (docs/decisions/0008)", rdrandMask)
	return true
})

// AgyStatus reports whether the agy kind is runnable on this host, with a
// machine-readable reason when it is not:
//
//	supported=false reason="not_installed" — agy binary absent (image without the
//	                                         bake; PATH prefers ~/.local/bin too)
//	supported=false reason="no_rdrand"     — x86 host with no usable RDRAND that the
//	                                         mask could not rescue either (agy would
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
	if runtime.GOARCH == "amd64" && !RDRAND() && !agyStartsMasked() {
		return false, "no_rdrand"
	}
	return true, ""
}
