// Package resources reads THIS workspace's own resource measurements (memory, CPU,
// disk) from inside the container.
//
// Why on the Agent side. The CP's `containerStats` (control-plane/metrics.go) looks the
// container id up with `docker inspect` and reads the host's
// `/sys/fs/cgroup/system.slice/docker-<id>.scope` — a reading that only holds where CP
// and the workspace sit on the same host. On ECS (Fargate and `ecs-ec2` alike) the CP
// task has neither the docker binary nor the target cgroup, so all three of memory /
// CPU / disk came back empty and the member-detail tiles stayed "–" (docs/log/63 §63.9).
//
// From inside the container it is the other way round: the cgroup namespace remaps
// `/sys/fs/cgroup` onto ourselves, so whatever the runtime is, reading the same two
// files is enough. There is precedent: status.OOMKillCount (moved into this package)
// already counted our own oom_kill this way.
//
// An axis that cannot be read is OMITTED, not returned as zero. Representing "0%" and
// "not measurable" with the same value makes the screen draw the unmeasurable as 0.
package resources

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// cgroupDir is the container's own cgroup v2 root. Overridable for tests.
func cgroupDir() string {
	if v := os.Getenv("AF_CGROUP_DIR"); v != "" {
		return v
	}
	return "/sys/fs/cgroup"
}

// Stats is one observation. An axis that could not be read is nil and disappears from
// the JSON too. The field names are deliberately the SAME KEYS the CP's docker path
// (control-plane/metrics.go) emits: CP puts the values from either path into the same
// map, and the Console does not distinguish where they came from.
type Stats struct {
	MemUsed *uint64 `json:"mem_used,omitempty"`
	MemMax  *uint64 `json:"mem_max,omitempty"`
	// CPUPct is "one core = 100%" (the docker stats convention). Two samples are
	// needed, so the first call after process start is always nil.
	CPUPct *float64 `json:"cpu_pct,omitempty"`
	// OOMKillTotal is cumulative. Deciding "did it grow recently" is the CP's job
	// (oomTracker).
	OOMKillTotal *uint64 `json:"oom_kill_total,omitempty"`
	// DiskUsed / DiskTotal are a statfs of the filesystem home sits on. Not a tree
	// walk like du, so it is cheap enough to call every time.
	DiskUsed  *uint64 `json:"disk_used,omitempty"`
	DiskTotal *uint64 `json:"disk_total,omitempty"`
}

// Read returns the current observation. Every axis may fail independently — environments
// where only one side is readable do exist (a cgroup v1 host, home unreadable for
// permission reasons).
func Read() Stats {
	var s Stats
	if v, ok := memUsed(); ok {
		s.MemUsed = &v
	}
	if v, ok := memMax(); ok {
		s.MemMax = &v
	}
	if v, ok := cpu.pct(time.Now()); ok {
		s.CPUPct = &v
	}
	if v, ok := OOMKillCount(); ok {
		s.OOMKillTotal = &v
	}
	if used, total, ok := homeUsage(); ok {
		s.DiskUsed, s.DiskTotal = &used, &total
	}
	return s
}

// --- cgroup v2 and v1, both supported ---
//
// The hosts the fleet provisions itself are cgroup v2 (the EC2 slot AMI is pinned to the
// `amazon-linux-2023` ECS-optimized one — deploy/aws/ecs/cfn/40-ec2-pool.yaml). But
// Fargate's platform version is not pinned (CP passes no `PlatformVersion` and follows
// LATEST), so we cannot control whether the underlying host is v1. An implementation that
// only reads the v2 file names would then show "memory and CPU silently –" — exactly the
// appearance this code exists to fix.
//
// The names and the units both differ, so each axis is read v2 first, then v1.
//
//	         v2                            v1
//	memory   memory.current                memory/memory.usage_in_bytes
//	limit    memory.max ("max"=unlimited)  memory/memory.limit_in_bytes (huge=unlimited)
//	CPU      cpu.stat usage_usec (µs)      cpuacct/cpuacct.usage (ns)
//	OOM      memory.events oom_kill        memory/memory.oom_control oom_kill
//
// Only the v2 side is confirmed on real hardware (it matches this container's actual
// cgroup and `df`). The v1 side is verified against fixtures only — no v1 host was
// provisioned and measured.

// v1Unlimited: cgroup v1 expresses "no limit" as a huge number (typically
// 9223372036854771712 = PAGE_COUNTER_MAX). Unlike v2's "max" it parses as a number, so
// without a threshold to reject it we would draw 0% usage over an "8 EiB limit".
const v1Unlimited = uint64(1) << 62

func memUsed() (uint64, bool) {
	if v, ok := readCgroupUint("memory.current"); ok {
		return v, true
	}
	return readCgroupUint("memory/memory.usage_in_bytes")
}

func memMax() (uint64, bool) {
	if v, ok := readCgroupUint("memory.max"); ok {
		return v, true
	}
	v, ok := readCgroupUint("memory/memory.limit_in_bytes")
	if !ok || v >= v1Unlimited {
		return 0, false
	}
	return v, true
}

// readCgroupUint reads a cgroup single-value file. "max" (unlimited) is !ok, so that the
// absence of a limit is never drawn on screen as a huge number.
func readCgroupUint(name string) (uint64, bool) {
	b, err := os.ReadFile(cgroupDir() + "/" + name)
	if err != nil {
		return 0, false
	}
	t := strings.TrimSpace(string(b))
	if t == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(t, 10, 64)
	return v, err == nil
}

// readCgroupKV pulls one line out of a flat "key value" file (memory.events and friends).
func readCgroupKV(name, key string) (uint64, bool) {
	b, err := os.ReadFile(cgroupDir() + "/" + name)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == key {
			v, err := strconv.ParseUint(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// OOMKillCount reads the cumulative oom_kill counter from the container's own
// cgroup v2 memory.events. From inside the container /sys/fs/cgroup is
// cgroup-namespaced to this container, so this is our own count. Reports !ok when
// unreadable (a non cgroup-v2 host, a different layout, etc.) so callers degrade
// instead of guessing OOM. It lives here so that every cgroup read sits in one place;
// status.OOMKillCount is kept for compatibility and only delegates to this.
//
// v1's `memory.oom_control` has three lines, "oom_kill_disable 0 / under_oom 0 /
// oom_kill N", and the third one only exists on fairly recent kernels. Without it, !ok
// means "cannot tell whether this was an OOM" and the callers (record_exit / supervisor)
// fall back to crashed as the cause of death.
func OOMKillCount() (uint64, bool) {
	if v, ok := readCgroupKV("memory.events", "oom_kill"); ok {
		return v, true
	}
	return readCgroupKV("memory/memory.oom_control", "oom_kill")
}

// --- CPU ---

// cpuMeter derives a usage percentage from cpu.stat's cumulative usage_usec. Being a
// cumulative counter, the only way is to take a delta, which requires SOMEONE to remember
// the previous value.
//
// Keeping exactly one of those here, rather than letting callers remember, is the point.
// stats is hit both by the CP's SSE tick (4s) and by the admin screen's polling (4s), so
// taking the delta per call has the two streams trampling each other's previous value:
// both end up with a very short window and the numbers jump. One shared meter instead,
// and a revisit sooner than minInterval gets the previous answer back unchanged.
type cpuMeter struct {
	mu   sync.Mutex
	prev uint64
	at   time.Time
	last float64
	have bool // last is valid (= at least two samples were taken)
}

// minInterval is the shortest gap at which a new delta is taken. Against usage_usec's
// resolution, too short a window lets quantisation error dominate and an idle workspace
// can look like tens of percent.
const minInterval = time.Second

var cpu = &cpuMeter{}

func (m *cpuMeter) pct(now time.Time) (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.at.IsZero() && now.Sub(m.at) < minInterval {
		return m.last, m.have
	}
	usage, ok := readUsageUsec()
	if !ok {
		return 0, false
	}
	prev, prevAt := m.prev, m.at
	m.prev, m.at = usage, now
	wall := now.Sub(prevAt).Microseconds()
	// A first call, a clock that went backwards and a counter reset (container
	// recreate) are all unmeasurable. Clear have rather than keep returning the last
	// value: better to say "not measurable" than to present a stale number as current.
	if prevAt.IsZero() || wall <= 0 || usage < prev {
		m.last, m.have = 0, false
		return 0, false
	}
	m.last, m.have = float64(usage-prev)/float64(wall)*100, true
	return m.last, true
}

// readUsageUsec returns cumulative CPU time in MICROSECONDS. v1's cpuacct.usage is in
// NANOSECONDS, so without normalising the unit here the percentage comes out 1000x too
// high (an idle workspace looking like tens of thousands of percent). Callers need not
// know about the unit at all.
func readUsageUsec() (uint64, bool) {
	if v, ok := readCgroupKV("cpu.stat", "usage_usec"); ok {
		return v, true
	}
	if ns, ok := readCgroupUint("cpuacct/cpuacct.usage"); ok {
		return ns / 1000, true
	}
	return 0, false
}

// --- Disk ---

// homeUsage returns used / total of the filesystem home sits on, via statfs.
//
// Deliberately NOT `du`. The CP's docker path walks the home tree with
// `du -sb <dataDir>/home` because that was the only way to size "one directory on the
// host", and it costs more the bigger the tree gets (hence the 60s cache on the CP side).
// Inside the container home is our own volume, so a single statfs gives both usage and
// capacity. On `ecs-ec2` home is the persistent EBS volume, which makes these two exactly
// the numbers we want to know (docs/log/64).
func homeUsage() (used, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(paths.HomeDir(), &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	if bs == 0 || st.Blocks == 0 {
		return 0, 0, false
	}
	total = st.Blocks * bs
	// Usage is Blocks-Bfree (real usage, including the root reservation). Bavail
	// would be the free space an ordinary user sees, and then used+free no longer
	// adds up to total.
	used = (st.Blocks - st.Bfree) * bs
	return used, total, true
}
