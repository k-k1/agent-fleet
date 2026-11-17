package mempressure

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// minuteSampler builds a sampler whose CPU counter is a function of the fake clock, so a test
// states the CPU rate it expects ("half a core") and reads it back from the series.
func minuteSampler(t *testing.T, usec func(time.Time) (uint64, bool), lid string) (*Sampler, *clock, *Series) {
	t.Helper()
	dir := fixture(t, fullFixture())
	c := &clock{time.Unix(1000, 0)}
	series := NewSeries(filepath.Join(t.TempDir(), "series.jsonl"), 0)
	s := New(dir, Boundary{OK: true}, Sources{
		ReadFile: os.ReadFile,
		Now:      c.now,
		CPUUsec:  func() (uint64, bool) { return usec(c.t) },
		LaunchID: lid,
	}, series)
	return s, c, series
}

func tickMinute(s *Sampler, c *clock) {
	for i := 0; i < recordEvery; i++ {
		c.t = c.t.Add(Interval)
		s.Tick()
	}
}

func readLines(t *testing.T, series *Series) []Record {
	t.Helper()
	b, err := os.ReadFile(series.path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, ln := range splitLines(b) {
		var r Record
		if err := json.Unmarshal(ln, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	return out
}

// A window's CPU is the usage_usec delta over the window's own wall time, and the very first
// window is measured too (the baseline is taken at the first tick, not at the first flush).
func TestWindowCPUIsAFixedWindowDelta(t *testing.T) {
	// 0.5 core: 500000 µs of CPU per wall second.
	half := func(at time.Time) (uint64, bool) { return uint64(at.Unix()-1000) * 500000, true }
	s, c, series := minuteSampler(t, half, "L1")
	tickMinute(s, c)
	tickMinute(s, c)
	got := readLines(t, series)
	if len(got) != 2 {
		t.Fatalf("%d lines", len(got))
	}
	for i, r := range got {
		if r.CPUPct == nil || *r.CPUPct < 49.99 || *r.CPUPct > 50.01 {
			t.Fatalf("window %d cpu_pct = %v, want 50", i, r.CPUPct)
		}
		if r.LID != "L1" {
			t.Fatalf("window %d lid = %q", i, r.LID)
		}
	}
}

// Unmeasured is never zero: a counter that went backwards (a container recreated under the same
// Agent state), an unreadable counter and a window far from a minute all leave the figure out.
func TestWindowCPUUnmeasuredIsAbsent(t *testing.T) {
	reset := false
	usec := func(at time.Time) (uint64, bool) {
		if reset {
			return 10, true
		}
		return uint64(at.Unix()-1000) * 500000, true
	}
	s, c, series := minuteSampler(t, usec, "L1")
	tickMinute(s, c)
	reset = true
	tickMinute(s, c) // counter fell: no delta
	reset = false
	tickMinute(s, c) // next delta is against the bad reading: usage jumped up, but cur >= prev, so measured
	got := readLines(t, series)
	if got[0].CPUPct == nil {
		t.Fatal("first window should be measured")
	}
	if got[1].CPUPct != nil {
		t.Fatalf("counter reset window reported %v", *got[1].CPUPct)
	}

	none := func(time.Time) (uint64, bool) { return 0, false }
	s2, c2, series2 := minuteSampler(t, none, "")
	tickMinute(s2, c2)
	if r := readLines(t, series2)[0]; r.CPUPct != nil || r.LID != "" {
		t.Fatalf("unreadable counter / no launch id: %+v", r)
	}

	// A sampler that stalled for 10 minutes: the delta is not a one-minute figure.
	s3, c3, series3 := minuteSampler(t, func(at time.Time) (uint64, bool) { return uint64(at.Unix()-1000) * 500000, true }, "L")
	s3.Tick() // baseline
	c3.t = c3.t.Add(10 * time.Minute)
	for i := 0; i < recordEvery; i++ {
		s3.Tick()
	}
	if r := readLines(t, series3)[0]; r.CPUPct != nil {
		t.Fatalf("stalled window reported %v", *r.CPUPct)
	}
}

func appendJSON(t *testing.T, s *Series, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := s.Append(b); err != nil {
		t.Fatal(err)
	}
}

// The cursor moves past lines with no launch id, a page is cut at the limit with the cursor on
// the last window returned, and a torn line does not hide what is before and after it.
func TestWindowsSinceCursorSemantics(t *testing.T) {
	s := NewSeries(filepath.Join(t.TempDir(), "series.jsonl"), 0)
	cpu := 12.5
	appendJSON(t, s, map[string]any{"t": 100, "ws": 5})                    // pre-feature line: no lid
	appendJSON(t, s, map[string]any{"t": 160, "lid": "A", "cpu_pct": cpu}) //
	appendJSON(t, s, map[string]any{"t": 220, "lid": "A", "ws": 7})        //
	if err := s.Append([]byte(`{"t":280,"lid":"A","ws":`)); err != nil {   // torn
		t.Fatal(err)
	}
	appendJSON(t, s, map[string]any{"t": 340, "lid": "B", "full60": 0.5, "some10_max": 1.5})

	p, err := s.WindowsSince(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Windows) != 3 || p.Cursor != 340 || p.More {
		t.Fatalf("page = %+v", p)
	}
	if p.Windows[0].CPUPct == nil || *p.Windows[0].CPUPct != cpu || p.Windows[0].WorkingSet != nil {
		t.Fatalf("absent figure came back as a value: %+v", p.Windows[0])
	}
	if p.Windows[2].LID != "B" || p.Windows[2].Full60 == nil || p.Windows[2].CPUPct != nil {
		t.Fatalf("%+v", p.Windows[2])
	}

	// Cut at the limit: the cursor sits on the last window returned, so the rest is next page.
	p, _ = s.WindowsSince(0, 2)
	if len(p.Windows) != 2 || !p.More || p.Cursor != 220 {
		t.Fatalf("limited page = %+v", p)
	}
	p, _ = s.WindowsSince(p.Cursor, 2)
	if len(p.Windows) != 1 || p.Windows[0].T != 340 || p.More || p.Cursor != 340 {
		t.Fatalf("second page = %+v", p)
	}
	// Nothing after the cursor: same cursor back, never zero and never an error.
	p, _ = s.WindowsSince(340, 10)
	if len(p.Windows) != 0 || p.Cursor != 340 || p.Windows == nil {
		t.Fatalf("empty page = %+v", p)
	}
	// Only lid-less lines after the cursor: the cursor still advances past them.
	appendJSON(t, s, map[string]any{"t": 400})
	p, _ = s.WindowsSince(340, 10)
	if len(p.Windows) != 0 || p.Cursor != 400 {
		t.Fatalf("lid-less tail = %+v", p)
	}
}

// Both generations are read, oldest first, so a rotation between two polls loses nothing.
func TestWindowsSinceSpansRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "series.jsonl")
	s := NewSeries(path, 100)
	for i := 1; i <= 8; i++ {
		appendJSON(t, s, map[string]any{"t": i * 60, "lid": "A", "ws": i})
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("test needs a rotation to have happened")
	}
	p, _ := s.WindowsSince(0, 100)
	if len(p.Windows) < 3 {
		t.Fatalf("only %d windows survive", len(p.Windows))
	}
	for i := 1; i < len(p.Windows); i++ {
		if p.Windows[i].T <= p.Windows[i-1].T {
			t.Fatalf("not ascending: %+v", p.Windows)
		}
	}
	if last := p.Windows[len(p.Windows)-1]; last.T != 480 || p.Cursor != 480 {
		t.Fatalf("last = %+v cursor %d", last, p.Cursor)
	}
}
