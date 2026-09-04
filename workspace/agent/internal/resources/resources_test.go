package resources

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCgroup writes a minimal cgroup v2 scope into a temp directory and points
// AF_CGROUP_DIR at it.
func fakeCgroup(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AF_CGROUP_DIR", dir)
	return dir
}

func TestReadsMemoryFromItsOwnCgroup(t *testing.T) {
	fakeCgroup(t, map[string]string{
		"memory.current": "1073741824\n",
		"memory.max":     "4294967296\n",
	})
	s := Read()
	if s.MemUsed == nil || *s.MemUsed != 1073741824 {
		t.Errorf("mem_used = %v, want 1073741824", s.MemUsed)
	}
	if s.MemMax == nil || *s.MemMax != 4294967296 {
		t.Errorf("mem_max = %v, want 4294967296", s.MemMax)
	}
}

// An unlimited cgroup writes "max" into memory.max. Carrying that as a number would make
// the screen draw a figure like 16 EiB as the limit, so the key is dropped entirely.
func TestUnlimitedMemoryMaxIsOmittedRatherThanHuge(t *testing.T) {
	fakeCgroup(t, map[string]string{
		"memory.current": "1000\n",
		"memory.max":     "max\n",
	})
	if s := Read(); s.MemMax != nil {
		t.Errorf("mem_max = %v, want nil for an unlimited cgroup", *s.MemMax)
	}
}

// An axis that cannot be read is absent, not 0. If "0%" and "not measurable" share a value,
// the Console draws the unmeasurable as 0 (and the tile can no longer show "–").
func TestUnreadableAxesAreOmittedNotZero(t *testing.T) {
	fakeCgroup(t, nil) // empty directory = none of the files exist
	s := Read()
	if s.MemUsed != nil || s.MemMax != nil || s.CPUPct != nil || s.OOMKillTotal != nil {
		t.Fatalf("want every cgroup axis omitted, got %+v", s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mem_used", "mem_max", "cpu_pct", "oom_kill_total"} {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if _, present := m[key]; present {
			t.Errorf("%s present in %s, want the key omitted", key, b)
		}
	}
}

func TestOOMKillCountReadsTheCumulativeCounter(t *testing.T) {
	fakeCgroup(t, map[string]string{
		"memory.events": "low 0\nhigh 4\nmax 2\noom 1\noom_kill 3\n",
	})
	v, ok := OOMKillCount()
	if !ok || v != 3 {
		t.Errorf("OOMKillCount = %d,%v want 3,true", v, ok)
	}
}

// CPU is a delta of a cumulative counter. The first sample has nothing to diff against, so
// it is always "not measurable".
func TestCPUNeedsTwoSamples(t *testing.T) {
	dir := fakeCgroup(t, map[string]string{"cpu.stat": "usage_usec 1000000\n"})
	m := &cpuMeter{}
	base := time.Unix(1700000000, 0)
	if _, ok := m.pct(base); ok {
		t.Fatal("first sample reported a percentage; it has nothing to diff against")
	}
	// 1 second of CPU over 2 seconds of wall clock = 50%.
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 2000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pct, ok := m.pct(base.Add(2 * time.Second))
	if !ok {
		t.Fatal("second sample still reported no percentage")
	}
	if pct < 49.9 || pct > 50.1 {
		t.Errorf("cpu_pct = %v, want ~50", pct)
	}
}

// A revisit sooner than minInterval returns the previous answer unchanged. stats is hit by
// two streams (the SSE tick and the admin screen's polling), so taking a fresh delta on every
// call has them trampling each other's previous value and the numbers jump.
func TestCPUShortRevisitReusesTheLastAnswer(t *testing.T) {
	dir := fakeCgroup(t, map[string]string{"cpu.stat": "usage_usec 0\n"})
	m := &cpuMeter{}
	base := time.Unix(1700000000, 0)
	m.pct(base)
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 1000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, ok := m.pct(base.Add(2 * time.Second))
	if !ok {
		t.Fatal("want a percentage from the second sample")
	}
	// Even when the counter moved, only 100ms elapsed, so the same answer comes back.
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 9000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, ok := m.pct(base.Add(2*time.Second + 100*time.Millisecond))
	if !ok || again != first {
		t.Errorf("short revisit = %v,%v want %v,true (the cached answer)", again, ok, first)
	}
}

// A counter that went backwards (container recreate = the cgroup was rebuilt) reports as not
// measurable. A delta against the old previous value means nothing.
func TestCPUCounterResetReportsUnmeasurable(t *testing.T) {
	dir := fakeCgroup(t, map[string]string{"cpu.stat": "usage_usec 5000000\n"})
	m := &cpuMeter{}
	base := time.Unix(1700000000, 0)
	m.pct(base)
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pct, ok := m.pct(base.Add(2 * time.Second)); ok {
		t.Errorf("counter reset reported %v%%, want unmeasurable", pct)
	}
}

// Disk is a statfs of the filesystem home sits on, not a du tree walk. Even in a test
// environment home is always on some filesystem, so check that values come back and
// used <= total.
func TestHomeUsageReportsUsedWithinTotal(t *testing.T) {
	used, total, ok := homeUsage()
	if !ok {
		t.Skip("statfs unavailable for home in this environment")
	}
	if total == 0 || used > total {
		t.Errorf("used=%d total=%d — want 0 < used <= total", used, total)
	}
}

// --- cgroup v1 fallback ---
//
// The hosts the fleet provisions itself are v2 (the EC2 slot is pinned to AL2023), but
// Fargate's platform version is not specified, so we cannot choose what runs underneath.
// These fixtures pin down that hitting v1 does not silently fall back to "memory and CPU
// only –". Note that this can only confirm the file names and the units; no real v1 host was
// measured.

// fakeV1Cgroup builds v1's per-subsystem layout (memory/ and cpuacct/).
func fakeV1Cgroup(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AF_CGROUP_DIR", dir)
	return dir
}

func TestFallsBackToCgroupV1(t *testing.T) {
	fakeV1Cgroup(t, map[string]string{
		"memory/memory.usage_in_bytes": "1073741824\n",
		"memory/memory.limit_in_bytes": "4294967296\n",
		"memory/memory.oom_control":    "oom_kill_disable 0\nunder_oom 0\noom_kill 2\n",
	})
	s := Read()
	if s.MemUsed == nil || *s.MemUsed != 1073741824 {
		t.Errorf("mem_used = %v, want 1073741824", s.MemUsed)
	}
	if s.MemMax == nil || *s.MemMax != 4294967296 {
		t.Errorf("mem_max = %v, want 4294967296", s.MemMax)
	}
	if s.OOMKillTotal == nil || *s.OOMKillTotal != 2 {
		t.Errorf("oom_kill_total = %v, want 2", s.OOMKillTotal)
	}
}

// v1 expresses "no limit" as a huge number rather than "max". It parses as a number, so
// without a threshold to reject it we would draw 0% usage over an "8 EiB limit".
func TestV1UnlimitedSentinelIsNotALimit(t *testing.T) {
	fakeV1Cgroup(t, map[string]string{
		"memory/memory.usage_in_bytes": "1000\n",
		"memory/memory.limit_in_bytes": "9223372036854771712\n",
	})
	if s := Read(); s.MemMax != nil {
		t.Errorf("mem_max = %v, want nil for v1's unlimited sentinel", *s.MemMax)
	}
}

// v1's cpuacct.usage is in NANOSECONDS, v2's usage_usec in MICROSECONDS. Forgetting to
// normalise makes the percentage 1000x too high and an idle Workspace look like tens of
// thousands of percent.
func TestV1CPUIsNanosecondsNotMicroseconds(t *testing.T) {
	dir := fakeV1Cgroup(t, map[string]string{"cpuacct/cpuacct.usage": "1000000000\n"}) // 1 second
	m := &cpuMeter{}
	base := time.Unix(1700000000, 0)
	if _, ok := m.pct(base); ok {
		t.Fatal("first sample reported a percentage")
	}
	// +1 second of CPU over 2 seconds of wall clock = 50%. Misreading ns as µs makes this
	// 50000%.
	if err := os.WriteFile(filepath.Join(dir, "cpuacct/cpuacct.usage"), []byte("2000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pct, ok := m.pct(base.Add(2 * time.Second))
	if !ok {
		t.Fatal("second sample reported no percentage")
	}
	if pct < 49.9 || pct > 50.1 {
		t.Errorf("cpu_pct = %v, want ~50 (~50000 when the unit conversion is missing)", pct)
	}
}

// On a host where v2 is readable, v1 is never consulted (a hybrid layout with both present
// must not be read twice, and the v2 value wins).
func TestV2WinsWhenBothLayoutsExist(t *testing.T) {
	fakeV1Cgroup(t, map[string]string{
		"memory.current":               "111\n",
		"memory/memory.usage_in_bytes": "999\n",
	})
	s := Read()
	if s.MemUsed == nil || *s.MemUsed != 111 {
		t.Errorf("mem_used = %v, want 111 (the v2 value)", s.MemUsed)
	}
}
