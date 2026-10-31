package mempressure

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// RequestedLimitEnv carries the memory limit, in bytes, the runtime asked for when it started
// this container. The Control Plane's adapters set it only where they set a limit (ADR 0120
// decision 1); the Agent reads it once at start. Unset, empty or unparsable means "not told".
const RequestedLimitEnv = "AF_WS_MEMORY_LIMIT_BYTES"

// Agreement is whether the resolved cgroup's memory.max matches the limit the runtime asked for.
type Agreement string

const (
	// AgreeUnknown covers every case where one side is missing: an older Control Plane, a
	// runtime that sets no limit, an unreadable memory.max. It is not a mismatch.
	AgreeUnknown  Agreement = "unknown"
	AgreeMatch    Agreement = "agree"
	AgreeMismatch Agreement = "disagree"
)

// limitSlack is how far memory.max may sit from the requested byte count and still agree. The
// kernel rounds a written limit down to a page, and the page is 4 KiB on x86_64 but up to 64 KiB
// on arm64, so the slack is the largest page.
const limitSlack = 64 << 10

// CompareMemoryMax compares the requested limit (RequestedLimitEnv's value) with the content of
// the cgroup's memory.max file. memMax is "" when the file could not be read. The detail is a
// log line's worth of explanation. A requested limit against "max" (unlimited) disagrees: the
// limit was asked for and is not in force on this cgroup.
func CompareMemoryMax(requested, memMax string) (Agreement, string) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return AgreeUnknown, "the runtime did not say which memory limit it asked for"
	}
	want, err := strconv.ParseInt(requested, 10, 64)
	if err != nil || want <= 0 {
		return AgreeUnknown, fmt.Sprintf("requested memory limit %q is not a positive byte count", requested)
	}
	memMax = strings.TrimSpace(memMax)
	if memMax == "" {
		return AgreeUnknown, "memory.max could not be read"
	}
	if memMax == "max" {
		return AgreeMismatch, fmt.Sprintf("memory.max is unlimited but the runtime asked for %d bytes", want)
	}
	got, err := strconv.ParseInt(memMax, 10, 64)
	if err != nil || got < 0 {
		return AgreeUnknown, fmt.Sprintf("memory.max %q is not a byte count", memMax)
	}
	d := got - want
	if d < 0 {
		d = -d
	}
	if d <= limitSlack {
		return AgreeMatch, fmt.Sprintf("memory.max %d matches the requested %d bytes", got, want)
	}
	return AgreeMismatch, fmt.Sprintf("memory.max is %d bytes but the runtime asked for %d", got, want)
}

// CheckMemoryMax is the wiring: it reads dir/memory.max and compares it with the requested limit,
// but only for a cgroup the boundary check accepted. A directory that is not verified as this
// container's own says nothing about this container's limit, so it is unknown, never agree or
// disagree.
func CheckMemoryMax(b Boundary, dir, requested string) (Agreement, string) {
	if !b.OK {
		return AgreeUnknown, "cgroup not verified as this container's own"
	}
	raw, _ := os.ReadFile(dir + "/memory.max")
	return CompareMemoryMax(requested, string(raw))
}
