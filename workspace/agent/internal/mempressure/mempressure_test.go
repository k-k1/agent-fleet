package mempressure

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

func init() { log.SetOutput(io.Discard) }

const psiOK = "some avg10=1.50 avg60=0.75 avg300=0.10 total=1234\nfull avg10=0.25 avg60=0.05 avg300=0.00 total=99\n"

func TestParsePSI(t *testing.T) {
	p, err := ParsePSI(psiOK)
	if err != nil {
		t.Fatal(err)
	}
	if p.Some.Avg10 != 1.5 || p.Some.Avg60 != 0.75 || p.Some.TotalUsec != 1234 || p.Full.Avg10 != 0.25 || p.Full.TotalUsec != 99 {
		t.Fatalf("parsed %+v", p)
	}
	for name, in := range map[string]string{
		"empty":      "",
		"no full":    "some avg10=1.0 avg60=0.0 avg300=0.0 total=1\n",
		"no avg60":   "some avg10=1.0 total=1\nfull avg10=0.0 avg60=0.0 total=1\n",
		"bad number": "some avg10=x avg60=0.0 total=1\nfull avg10=0.0 avg60=0.0 total=1\n",
		"no total":   "some avg10=1.0 avg60=0.0\nfull avg10=0.0 avg60=0.0 total=1\n",
		"permission": "Permission denied",
	} {
		if _, err := ParsePSI(in); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestParseRollup(t *testing.T) {
	a, s, err := ParseRollup("Rss: 10 kB\nPss_Anon:   300 kB\nPss_Shmem:   7 kB\n")
	if err != nil || a != 300<<10 || s != 7<<10 {
		t.Fatalf("got %d %d %v", a, s, err)
	}
	// An old kernel has no Pss_Anon: that is "not measurable", not zero.
	if _, _, err := ParseRollup("Rss: 10 kB\nPss: 5 kB\n"); err == nil {
		t.Fatal("missing Pss_Anon parsed as zero")
	}
}

const (
	nsRootCg   = "0::/\n"
	nsRootMnt  = "532 531 0:30 / /sys/fs/cgroup ro,nosuid - cgroup2 cgroup rw\n"
	hostCg     = "0::/system.slice/docker-abc.scope\n"
	boundCg    = "0::/system.slice/docker-abc.scope\n"
	boundMnt   = "532 531 0:30 /system.slice/docker-abc.scope /sys/fs/cgroup ro - cgroup2 cgroup rw\n"
	v1Cg       = "12:memory:/docker/abc\n1:name=systemd:/docker/abc\n"
	v1Mnt      = "30 29 0:25 / /sys/fs/cgroup/memory ro - cgroup cgroup rw,memory\n"
	escapedMnt = "532 531 0:30 / /sys/fs/cg\\040root ro - cgroup2 cgroup rw\n"
)

// A host's root cgroup reads exactly like a private namespace root ("0::/" on a root mount);
// the only difference is that the kernel creates no memory.max there.
func TestCheckBoundaryHostRoot(t *testing.T) {
	if b := CheckBoundary("/sys/fs/cgroup", nsRootCg, nsRootMnt, false); b.OK || !strings.Contains(b.Reason, "no memory.max") {
		t.Fatalf("host root accepted: %+v", b)
	}
	if b := CheckBoundary("/sys/fs/cgroup", nsRootCg, nsRootMnt, true); !b.OK {
		t.Fatalf("private namespace root rejected: %+v", b)
	}
}

func TestCheckBoundary(t *testing.T) {
	cases := []struct {
		name, dir, cg, mnt string
		ok                 bool
		why                string
	}{
		{"private cgroup namespace", "/sys/fs/cgroup", nsRootCg, nsRootMnt, true, ""},
		{"host namespace reads the host root", "/sys/fs/cgroup", hostCg, nsRootMnt, false, "not this process's cgroup"},
		{"bind mount of own cgroup", "/sys/fs/cgroup", boundCg, boundMnt, true, ""},
		{"bind mount of someone else's", "/sys/fs/cgroup", "0::/system.slice/docker-zzz.scope\n", boundMnt, false, "not this process's cgroup"},
		{"child of the namespace root", "/sys/fs/cgroup", "0::/init\n", nsRootMnt, false, "not this process's cgroup"},
		{"dir is the own sub cgroup", "/sys/fs/cgroup/init", "0::/init\n", nsRootMnt, true, ""},
		{"cgroup v1", "/sys/fs/cgroup", v1Cg, v1Mnt, false, "no cgroup v2"},
		{"no cgroup2 mount", "/sys/fs/cgroup", nsRootCg, v1Mnt, false, "not under a cgroup2 mount"},
		{"dir elsewhere", "/tmp/x", nsRootCg, nsRootMnt, false, "not under a cgroup2 mount"},
		{"escaped mount point", "/sys/fs/cg root", nsRootCg, escapedMnt, true, ""},
		{"prefix is not a parent", "/sys/fs/cgroupfoo", nsRootCg, nsRootMnt, false, "not under a cgroup2 mount"},
		{"nothing readable", "/sys/fs/cgroup", "", "", false, "no cgroup v2"},
	}
	for _, c := range cases {
		b := CheckBoundary(c.dir, c.cg, c.mnt, true)
		if b.OK != c.ok || (!c.ok && !strings.Contains(b.Reason, c.why)) {
			t.Errorf("%s: got %+v, want ok=%v why~%q", c.name, b, c.ok, c.why)
		}
	}
}

// fixture writes a cgroup directory and returns it.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fullFixture() map[string]string {
	return map[string]string{
		"memory.pressure": psiOK,
		"memory.current":  "1000000\n",
		"memory.max":      "4000000\n",
		"memory.stat":     "anon 600000\nfile 300000\nshmem 5\ninactive_file 200000\nworkingset_refault_file 100\n",
		"memory.events":   "low 0\nhigh 2\nmax 3\noom 4\noom_kill 5\n",
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func testSampler(dir string, c *clock, series *Series) *Sampler {
	return New(dir, Boundary{OK: true}, Sources{
		ReadFile: os.ReadFile,
		Now:      c.now,
		Probe:    func() (time.Duration, error) { return 3 * time.Millisecond, nil },
	}, series)
}

func TestSamplerReadsEverySignal(t *testing.T) {
	dir := fixture(t, fullFixture())
	c := &clock{time.Unix(1000, 0)}
	s := testSampler(dir, c, nil)
	s.Tick()
	w := s.Wire()
	if w == nil || !w.Available {
		t.Fatalf("wire = %+v", w)
	}
	if *w.SomeAvg10 != 1.5 || *w.FullAvg60 != 0.05 || *w.WorkingSet != 800000 || *w.AgentLatencyMs != 3 || w.AgentProbeFailed {
		t.Fatalf("wire = %+v", w)
	}
	if w.RefaultRate != nil {
		t.Fatal("refault rate from a single sample")
	}
	// Second sample: 50 more refaults over 5 s.
	f := fullFixture()
	f["memory.stat"] = strings.Replace(f["memory.stat"], "workingset_refault_file 100", "workingset_refault_file 150", 1)
	for n, b := range f {
		os.WriteFile(filepath.Join(dir, n), []byte(b), 0o600)
	}
	c.t = c.t.Add(5 * time.Second)
	s.Tick()
	if r := s.Wire().RefaultRate; r == nil || *r != 10 {
		t.Fatalf("refault rate = %v", r)
	}
	if got := *s.latest.EvOOMKill; got != 5 {
		t.Fatalf("oom_kill = %d", got)
	}
}

// The central rule: a signal that cannot be read is unavailable, never zero.
func TestSamplerUnavailableIsNotZero(t *testing.T) {
	f := fullFixture()
	delete(f, "memory.pressure") // PSI disabled in the kernel
	f["memory.max"] = "max\n"    // unlimited
	delete(f, "memory.events")
	dir := fixture(t, f)
	s := testSampler(dir, &clock{time.Unix(1, 0)}, nil)
	s.Tick()
	w := s.Wire()
	if w.Available || w.Reason == "" {
		t.Fatalf("wire = %+v", w)
	}
	if w.SomeAvg10 != nil || w.SomeAvg60 != nil || w.FullAvg10 != nil || w.FullAvg60 != nil {
		t.Fatalf("pressure figures present although unreadable: %+v", w)
	}
	if s.latest.Max != nil || s.latest.EvMax != nil {
		t.Fatal("unlimited / missing read as a number")
	}
	// Truncated file: also unavailable.
	os.WriteFile(filepath.Join(dir, "memory.pressure"), []byte("some avg10=1.0"), 0o600)
	s.Tick()
	if s.Wire().Available {
		t.Fatal("truncated PSI reported as available")
	}
	// An inactive_file above current cannot give a working set.
	os.WriteFile(filepath.Join(dir, "memory.stat"), []byte("inactive_file 9999999\n"), 0o600)
	s.Tick()
	if s.latest.WorkingSet != nil {
		t.Fatal("negative working set clamped to a number")
	}
}

func TestSamplerNoCgroupAtAll(t *testing.T) {
	s := testSampler(t.TempDir(), &clock{time.Unix(1, 0)}, nil)
	s.Tick()
	if w := s.Wire(); w == nil || w.Available || w.WorkingSet != nil {
		t.Fatalf("wire = %+v", w)
	}
}

func TestSamplerBoundaryFailureReadsNothing(t *testing.T) {
	reads := 0
	s := New("/x", Boundary{Reason: "host root"}, Sources{
		ReadFile: func(string) ([]byte, error) { reads++; return nil, errors.New("no") },
		Now:      time.Now,
	}, nil)
	s.Tick()
	if reads != 0 {
		t.Fatalf("read %d files of a cgroup that is not ours", reads)
	}
	if w := s.Wire(); w == nil || w.Available || w.Reason != "host root" {
		t.Fatalf("wire = %+v", w)
	}
}

func TestWireStaleAndBeforeFirstSample(t *testing.T) {
	c := &clock{time.Unix(1000, 0)}
	s := testSampler(fixture(t, fullFixture()), c, nil)
	if s.Wire() != nil {
		t.Fatal("axis present before the first sample")
	}
	s.Tick()
	c.t = c.t.Add(StaleAfter + time.Second)
	if w := s.Wire(); w.Available || w.SomeAvg10 != nil {
		t.Fatalf("stale sample served as current: %+v", w)
	}
}

func TestProbeFailureIsRecorded(t *testing.T) {
	c := &clock{time.Unix(1, 0)}
	s := testSampler(fixture(t, fullFixture()), c, nil)
	s.src.Probe = func() (time.Duration, error) { return 4 * time.Second, errors.New("timeout") }
	s.Tick()
	if w := s.Wire(); !w.AgentProbeFailed || *w.AgentLatencyMs != 4000 {
		t.Fatalf("wire = %+v", w)
	}
}

// --- per-session schedule ---

func mkLive(kinds map[string]int) ([]Live, map[int]procx.Info) {
	var live []Live
	tab := map[int]procx.Info{}
	pid := 100
	for k, n := range kinds {
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("%s-%02d", k, i)
			live = append(live, Live{Name: name, Kind: k, Root: pid})
			tab[pid] = procx.Info{PPID: 1, State: 'S'}
			tab[pid+1] = procx.Info{PPID: pid, State: 'S'}
			pid += 2
		}
	}
	return live, tab
}

func okRollup(pid int) (string, error) {
	return "Pss_Anon: 100 kB\nPss_Shmem: 1 kB\n", nil
}

// Unbiased: every session is measured equally often whatever its kind, and a kind with many
// sessions does not crowd out one with few.
func TestRotorMeasuresEverySessionEquallyOften(t *testing.T) {
	live, tab := mkLive(map[string]int{"claude": 9, "codex": 3, "shell": 1})
	var r rotor
	count := map[string]int{}
	const passes = 52 // chosen so every rotation completes: lcm-friendly
	for i := 0; i < passes; i++ {
		for _, o := range r.runPass(live, tab, okRollup).Obs {
			if o.Missing != "" {
				t.Fatalf("missing: %+v", o)
			}
			count[o.Name]++
		}
	}
	// Per-kind round-robin gives each kind the same number of slots, so a lone shell is
	// measured often and a claude session rarely; but within a kind the counts differ by at
	// most one rotation, i.e. the size of a session never enters.
	for _, k := range []string{"claude", "codex"} {
		lo, hi := 1<<30, 0
		for n, c := range count {
			if strings.HasPrefix(n, k) {
				lo, hi = min(lo, c), max(hi, c)
			}
		}
		if hi-lo > 1 {
			t.Errorf("%s: measured between %d and %d times", k, lo, hi)
		}
	}
	if count["shell-00"] == 0 {
		t.Fatal("a kind with one session was never measured")
	}
}

func TestPassBoundsSessionsAndProcesses(t *testing.T) {
	live, tab := mkLive(map[string]int{"claude": 40, "codex": 40})
	// Give every session 11 processes: 5 sessions would be 55 > MaxProcs.
	pid := 10000
	for _, l := range live {
		for i := 0; i < 10; i++ {
			tab[pid] = procx.Info{PPID: l.Root, State: 'S'}
			pid++
		}
	}
	reads := 0
	var r rotor
	p := r.runPass(live, tab, func(int) (string, error) { reads++; return okRollup(0) })
	if len(p.Obs) > MaxSessions {
		t.Fatalf("%d sessions in one pass", len(p.Obs))
	}
	if reads > MaxProcs {
		t.Fatalf("%d smaps_rollup reads in one pass, cap is %d", reads, MaxProcs)
	}
	var measured, budget int
	for _, o := range p.Obs {
		switch {
		case o.Missing == "budget":
			budget++
			if o.PssAnon != nil || o.Procs != 0 {
				t.Fatalf("budget-missing observation carries a partial sum: %+v", o)
			}
		case o.Missing == "":
			measured++
		}
	}
	if measured != 4 || budget != 1 {
		t.Fatalf("measured %d, over budget %d; want 4 and 1", measured, budget)
	}
}

func TestPassUnreadableProcessMakesObservationMissing(t *testing.T) {
	live, tab := mkLive(map[string]int{"claude": 1})
	n := 0
	var r rotor
	p := r.runPass(live, tab, func(pid int) (string, error) {
		n++
		if n == 2 {
			return "", fs.ErrPermission
		}
		return okRollup(pid)
	})
	o := p.Obs[0]
	if o.Missing != "unreadable" || o.PssAnon != nil || p.Coverage["claude"].Measured != 0 {
		t.Fatalf("obs = %+v coverage = %+v", o, p.Coverage)
	}
	// A process that exited since the table was read makes the sum partial: missing, not skipped.
	n = 0
	p = r.runPass(live, tab, func(pid int) (string, error) {
		n++
		if n == 2 {
			return "", fs.ErrNotExist
		}
		return okRollup(pid)
	})
	if o := p.Obs[0]; o.Missing != "exited" || o.PssAnon != nil || p.Coverage["claude"].Measured != 0 {
		t.Fatalf("partial sum recorded: obs = %+v coverage = %+v", o, p.Coverage)
	}
	// Every process gone: not a measured zero.
	p = r.runPass(live, tab, func(int) (string, error) { return "", fs.ErrNotExist })
	if o := p.Obs[0]; o.Missing != "exited" || o.PssAnon != nil || p.Coverage["claude"].Measured != 0 {
		t.Fatalf("all-exited recorded as measured: obs = %+v coverage = %+v", o, p.Coverage)
	}
	// A kernel without Pss_Anon.
	p = r.runPass(live, tab, func(int) (string, error) { return "Pss: 1 kB\n", nil })
	if p.Obs[0].Missing != "unparsable" {
		t.Fatalf("obs = %+v", p.Obs[0])
	}
}

func TestPassSkippedWhilePressureIsHigh(t *testing.T) {
	f := fullFixture()
	f["memory.pressure"] = "some avg10=30.00 avg60=1.0 avg300=0 total=1\nfull avg10=9.00 avg60=1.0 avg300=0 total=1\n"
	live, tab := mkLive(map[string]int{"claude": 2})
	reads := 0
	s := testSampler(fixture(t, f), &clock{time.Unix(1, 0)}, NewSeries(filepath.Join(t.TempDir(), "s.jsonl"), 0))
	s.src.Live = func() ([]Live, error) { return live, nil }
	s.src.Procs = func() map[int]procx.Info { return tab }
	s.src.Rollup = func(int) (string, error) { reads++; return okRollup(0) }
	for i := 0; i < recordEvery; i++ {
		s.Tick()
	}
	if reads != 0 {
		t.Fatalf("%d smaps_rollup reads under stage-2 pressure", reads)
	}
	b, _ := os.ReadFile(s.series.path)
	if !strings.Contains(string(b), `"skipped":"pressure"`) {
		t.Fatalf("series line does not say the pass was skipped: %s", b)
	}
}

// --- the series, and the sampler's own cost ---

func TestSeriesIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d", "series.jsonl")
	s := NewSeries(path, 1000)
	line := []byte(strings.Repeat("x", 99))
	for i := 0; i < 10000; i++ {
		if err := s.Append(line); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	ents, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range ents {
		fi, _ := e.Info()
		total += fi.Size()
	}
	if len(ents) != 2 || total > 2000 {
		t.Fatalf("%d files, %d bytes after 10000 appends with a 1000-byte cap", len(ents), total)
	}
}

// The cost of running: constant work per tick whatever the number of sessions, and a series
// line per minute that stays bounded.
func TestSamplerCostIsBounded(t *testing.T) {
	live, tab := mkLive(map[string]int{"claude": 200, "codex": 200})
	dir := fixture(t, fullFixture())
	series := NewSeries(filepath.Join(t.TempDir(), "s.jsonl"), 4096)
	reads, rollups := 0, 0
	c := &clock{time.Unix(1000, 0)}
	s := New(dir, Boundary{OK: true}, Sources{
		ReadFile: func(p string) ([]byte, error) { reads++; return os.ReadFile(p) },
		Now:      c.now,
		Probe:    func() (time.Duration, error) { return time.Millisecond, nil },
		Live:     func() ([]Live, error) { return live, nil },
		Procs:    func() map[int]procx.Info { return tab },
		Rollup:   func(int) (string, error) { rollups++; return okRollup(0) },
	}, series)
	const ticks = recordEvery * 200
	for i := 0; i < ticks; i++ {
		c.t = c.t.Add(Interval)
		s.Tick()
	}
	if want := ticks * 5; reads != want {
		t.Errorf("%d file reads over %d ticks, want exactly %d (5 cgroup files per tick)", reads, ticks, want)
	}
	if max := 200 * MaxProcs; rollups > max {
		t.Errorf("%d smaps_rollup reads over 200 passes, cap is %d", rollups, max)
	}
	var total int64
	for _, p := range []string{series.path, series.path + ".1"} {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	if total > 2*4096 {
		t.Errorf("series grew to %d bytes with a 4096-byte cap", total)
	}
	// No per-tick state grows: the sampler keeps one latest and one previous sample.
	if s.acc.n != 0 || s.ticks != ticks {
		t.Errorf("accumulator not reset: %+v", s.acc)
	}
}

func TestTickAllocationsAreSmallAndFlat(t *testing.T) {
	dir := fixture(t, fullFixture())
	c := &clock{time.Unix(1000, 0)}
	s := testSampler(dir, c, nil)
	s.Tick()
	// A tick without a pass allocates a handful of strings and one Sample; a regression that
	// kept whole smaps or per-session buffers per tick would be orders of magnitude over.
	a := testing.AllocsPerRun(50, func() {
		c.t = c.t.Add(Interval)
		s.Tick()
		s.ticks = 1 // stay clear of the record boundary: this measures the 5 s tick alone
	})
	if a > 150 {
		t.Fatalf("%.0f allocations per tick", a)
	}
}

// PSI that cannot be read is not "low pressure": the pass must not run on a guess.
func TestPassSkippedWhenPressureUnknown(t *testing.T) {
	f := fullFixture()
	delete(f, "memory.pressure")
	live, tab := mkLive(map[string]int{"claude": 2})
	reads := 0
	s := testSampler(fixture(t, f), &clock{time.Unix(1, 0)}, NewSeries(filepath.Join(t.TempDir(), "s.jsonl"), 0))
	s.src.Live = func() ([]Live, error) { return live, nil }
	s.src.Procs = func() map[int]procx.Info { return tab }
	s.src.Rollup = func(int) (string, error) { reads++; return okRollup(0) }
	for i := 0; i < recordEvery; i++ {
		s.Tick()
	}
	b, _ := os.ReadFile(s.series.path)
	if reads != 0 || !strings.Contains(string(b), `"skipped":"pressure_unknown"`) {
		t.Fatalf("%d rollup reads; series: %s", reads, b)
	}
}

// A tmux server stalled by the pressure being measured must cost one deadline, not the sampler.
func TestLiveSessionsTimesOut(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	old := tmuxTimeout
	tmuxTimeout = 200 * time.Millisecond
	defer func() { tmuxTimeout = old }()
	start := time.Now()
	live, err := liveSessions()
	if err == nil || live != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("live=%v err=%v after %s", live, err, time.Since(start))
	}
}

func TestPassSkippedWhenTmuxUnavailable(t *testing.T) {
	s := testSampler(fixture(t, fullFixture()), &clock{time.Unix(1, 0)}, NewSeries(filepath.Join(t.TempDir(), "s.jsonl"), 0))
	s.src.Live = func() ([]Live, error) { return nil, errors.New("timed out") }
	for i := 0; i < recordEvery; i++ {
		s.Tick()
	}
	b, _ := os.ReadFile(s.series.path)
	if !strings.Contains(string(b), `"skipped":"live_unavailable"`) {
		t.Fatalf("series: %s", b)
	}
}
