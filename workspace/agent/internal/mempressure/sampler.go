package mempressure

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

const (
	// Interval is the cgroup sampling period (ADR 0120 decision 1).
	Interval = 5 * time.Second
	// recordEvery is how many samples fold into one series line, and one per-session pass.
	recordEvery = 12 // 60 s
	// StaleAfter is how old the latest sample may be before the axis reports unavailable.
	// A sampler that stopped is not "no pressure".
	StaleAfter   = 3 * Interval
	probeTimeout = 4 * time.Second
)

// Sample is one reading. Every figure is nil when it could not be read.
type Sample struct {
	At time.Time

	Some10, Some60, Full10, Full60 *float64
	SomeUsec, FullUsec             *uint64

	Current, Max, WorkingSet *uint64
	Anon, File, Shmem        *uint64
	RefaultFile              *uint64  // cumulative workingset_refault_file
	RefaultRate              *float64 // per second since the previous sample

	EvHigh, EvMax, EvOOM, EvOOMKill *uint64 // cumulative memory.events

	// Latency is the time of a loopback request to this Agent's own /healthz. On a failed
	// probe it is the time spent before giving up and ProbeFailed is set: a silent Agent is
	// the case the series exists to catch.
	Latency     *time.Duration
	ProbeFailed bool
}

// Wire is the `mem_pressure` axis of GET /workspace/stats (the latest value only; the series
// stays on disk). Available is false when pressure cannot be measured here, and then no figure
// is present: the Console must not draw an unreadable signal as a calm one.
type Wire struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`

	SomeAvg10 *float64 `json:"some_avg10,omitempty"`
	SomeAvg60 *float64 `json:"some_avg60,omitempty"`
	FullAvg10 *float64 `json:"full_avg10,omitempty"`
	FullAvg60 *float64 `json:"full_avg60,omitempty"`

	// WorkingSet is memory.current − inactive_file, the figure that does not count cache the
	// kernel can drop for free.
	WorkingSet  *uint64  `json:"working_set,omitempty"`
	RefaultRate *float64 `json:"refault_rate,omitempty"`

	AgentLatencyMs   *float64 `json:"agent_latency_ms,omitempty"`
	AgentProbeFailed bool     `json:"agent_probe_failed,omitempty"`

	SampledAtMs int64 `json:"sampled_at_ms,omitempty"`
}

// Sources are the sampler's inputs, injectable so tests need neither a cgroup nor tmux.
type Sources struct {
	ReadFile func(path string) ([]byte, error)
	Probe    func() (time.Duration, error)
	Live     func() ([]Live, error)
	Procs    func() map[int]procx.Info
	Rollup   func(pid int) (string, error)
	Now      func() time.Time
}

// Sampler reads the container cgroup every Interval. It is a single goroutine that keeps one
// latest sample and one minute's accumulator: memory use is constant in uptime.
type Sampler struct {
	dir    string
	src    Sources
	series *Series

	mu     sync.Mutex
	latest *Sample
	reason string // non-empty: the boundary check failed; nothing is read

	acc   accum
	ticks int
	rotor rotor
	pass  *Pass // last pass of this minute

	warnedWrite bool
}

// New builds a sampler. A non-OK boundary makes it report unavailable without reading anything.
func New(dir string, b Boundary, src Sources, series *Series) *Sampler {
	s := &Sampler{dir: dir, src: src, series: series}
	if !b.OK {
		s.reason = b.Reason
	}
	return s
}

// Tick takes one sample, and on every recordEvery-th one runs the per-session pass and writes a
// series line.
func (s *Sampler) Tick() {
	if s.reason != "" {
		return
	}
	cur := s.read()
	s.mu.Lock()
	s.latest = cur
	s.mu.Unlock()
	s.acc.add(cur)
	s.ticks++
	if s.ticks%recordEvery != 0 {
		return
	}
	var pass *Pass
	switch {
	case s.src.Live == nil:
	case cur.Full10 == nil:
		// Unknown pressure is not low pressure: a PSI that fails to read is likely the
		// moment the box is worst off, and the pass takes mmap locks.
		pass = &Pass{Skipped: "pressure_unknown"}
	case *cur.Full10 >= SkipFullAvg10:
		pass = &Pass{Skipped: "pressure"}
	default:
		live, err := s.src.Live()
		if err != nil {
			pass = &Pass{Skipped: "live_unavailable"}
		} else if len(live) > 0 {
			p := s.rotor.runPass(live, s.src.Procs(), s.src.Rollup)
			pass = &p
		}
	}
	s.flush(cur.At, pass)
}

func (s *Sampler) read() *Sample {
	cur := &Sample{At: s.src.Now()}
	if b, err := s.src.ReadFile(s.dir + "/memory.pressure"); err == nil {
		if p, err := ParsePSI(string(b)); err == nil {
			cur.Some10, cur.Some60 = &p.Some.Avg10, &p.Some.Avg60
			cur.Full10, cur.Full60 = &p.Full.Avg10, &p.Full.Avg60
			cur.SomeUsec, cur.FullUsec = &p.Some.TotalUsec, &p.Full.TotalUsec
		}
	}
	cur.Current = s.readUint("/memory.current")
	cur.Max = s.readUint("/memory.max")
	var st map[string]uint64
	if b, err := s.src.ReadFile(s.dir + "/memory.stat"); err == nil {
		st = ParseKV(string(b))
	}
	if v, ok := st["anon"]; ok {
		cur.Anon = &v
	}
	if v, ok := st["file"]; ok {
		cur.File = &v
	}
	if v, ok := st["shmem"]; ok {
		cur.Shmem = &v
	}
	if v, ok := st["workingset_refault_file"]; ok {
		cur.RefaultFile = &v
	}
	if in, ok := st["inactive_file"]; ok && cur.Current != nil && in <= *cur.Current {
		ws := *cur.Current - in
		cur.WorkingSet = &ws
	}
	if b, err := s.src.ReadFile(s.dir + "/memory.events"); err == nil {
		ev := ParseKV(string(b))
		pick := func(k string) *uint64 {
			if v, ok := ev[k]; ok {
				return &v
			}
			return nil
		}
		cur.EvHigh, cur.EvMax, cur.EvOOM, cur.EvOOMKill = pick("high"), pick("max"), pick("oom"), pick("oom_kill")
	}
	if p := s.latest; p != nil && p.RefaultFile != nil && cur.RefaultFile != nil && *cur.RefaultFile >= *p.RefaultFile {
		if dt := cur.At.Sub(p.At).Seconds(); dt > 0 {
			r := float64(*cur.RefaultFile-*p.RefaultFile) / dt
			cur.RefaultRate = &r
		}
	}
	if s.src.Probe != nil {
		d, err := s.src.Probe()
		cur.Latency, cur.ProbeFailed = &d, err != nil
	}
	return cur
}

func (s *Sampler) readUint(name string) *uint64 {
	b, err := s.src.ReadFile(s.dir + name)
	if err != nil {
		return nil
	}
	v, err := strconv.ParseUint(string(trimNL(b)), 10, 64)
	if err != nil { // "max" (no limit) lands here: unlimited is not a number
		return nil
	}
	return &v
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// Wire returns the axis for the stats endpoint: nil before the first sample, an unavailable
// axis when the boundary failed, the cgroup has no readable PSI, or the sampler has gone quiet.
func (s *Sampler) Wire() *Wire {
	s.mu.Lock()
	cur := s.latest
	s.mu.Unlock()
	if s.reason != "" {
		return &Wire{Reason: s.reason}
	}
	if cur == nil {
		return nil
	}
	now := s.src.Now()
	if now.Sub(cur.At) > StaleAfter {
		return &Wire{Reason: "sampler stalled", SampledAtMs: cur.At.UnixMilli()}
	}
	w := &Wire{SampledAtMs: cur.At.UnixMilli()}
	if cur.Some10 == nil || cur.Full10 == nil {
		w.Reason = "memory.pressure not readable"
	} else {
		w.Available = true
		w.SomeAvg10, w.SomeAvg60, w.FullAvg10, w.FullAvg60 = cur.Some10, cur.Some60, cur.Full10, cur.Full60
	}
	w.WorkingSet, w.RefaultRate = cur.WorkingSet, cur.RefaultRate
	if cur.Latency != nil {
		ms := float64(*cur.Latency) / float64(time.Millisecond)
		w.AgentLatencyMs, w.AgentProbeFailed = &ms, cur.ProbeFailed
	}
	return w
}

// Record is one series line: the last minute folded into maxima (for the stall) and last values
// (for the cumulative counters, from which any delta can be taken later).
type Record struct {
	T int64 `json:"t"` // unix seconds
	N int   `json:"n"` // samples folded

	PSIMissing int      `json:"psi_missing,omitempty"` // samples without a readable PSI
	Some10Max  *float64 `json:"some10_max,omitempty"`
	Full10Max  *float64 `json:"full10_max,omitempty"`
	Some60     *float64 `json:"some60,omitempty"`
	Full60     *float64 `json:"full60,omitempty"`
	SomeUsec   *uint64  `json:"some_usec,omitempty"`
	FullUsec   *uint64  `json:"full_usec,omitempty"`

	Current *uint64 `json:"cur,omitempty"`
	Max     *uint64 `json:"max,omitempty"`
	WS      *uint64 `json:"ws,omitempty"`
	Anon    *uint64 `json:"anon,omitempty"`
	File    *uint64 `json:"file,omitempty"`
	Shmem   *uint64 `json:"shmem,omitempty"`

	RefaultFile    *uint64  `json:"refault_file,omitempty"`
	RefaultRateMax *float64 `json:"refault_rate_max,omitempty"`

	EvHigh    *uint64 `json:"ev_high,omitempty"`
	EvMax     *uint64 `json:"ev_max,omitempty"`
	EvOOM     *uint64 `json:"ev_oom,omitempty"`
	EvOOMKill *uint64 `json:"ev_oom_kill,omitempty"`

	LatencyMaxMs *float64 `json:"lat_max_ms,omitempty"`
	ProbeFailed  int      `json:"probe_failed,omitempty"`

	Sessions *Pass `json:"sessions,omitempty"`
}

type accum struct {
	n          int
	psiMissing int
	some10Max  *float64
	full10Max  *float64
	rateMax    *float64
	latMax     *float64
	probeFail  int
}

func maxp(dst **float64, v *float64) {
	if v != nil && (*dst == nil || *v > **dst) {
		x := *v
		*dst = &x
	}
}

func (a *accum) add(c *Sample) {
	a.n++
	if c.Some10 == nil || c.Full10 == nil {
		a.psiMissing++
	}
	maxp(&a.some10Max, c.Some10)
	maxp(&a.full10Max, c.Full10)
	maxp(&a.rateMax, c.RefaultRate)
	if c.Latency != nil {
		ms := float64(*c.Latency) / float64(time.Millisecond)
		maxp(&a.latMax, &ms)
	}
	if c.ProbeFailed {
		a.probeFail++
	}
}

func (s *Sampler) flush(at time.Time, pass *Pass) {
	c := s.latest
	r := Record{
		T: at.Unix(), N: s.acc.n, PSIMissing: s.acc.psiMissing,
		Some10Max: s.acc.some10Max, Full10Max: s.acc.full10Max,
		Some60: c.Some60, Full60: c.Full60, SomeUsec: c.SomeUsec, FullUsec: c.FullUsec,
		Current: c.Current, Max: c.Max, WS: c.WorkingSet, Anon: c.Anon, File: c.File, Shmem: c.Shmem,
		RefaultFile: c.RefaultFile, RefaultRateMax: s.acc.rateMax,
		EvHigh: c.EvHigh, EvMax: c.EvMax, EvOOM: c.EvOOM, EvOOMKill: c.EvOOMKill,
		LatencyMaxMs: s.acc.latMax, ProbeFailed: s.acc.probeFail,
	}
	s.acc = accum{}
	// The log line is the pressure summary alone; the per-session observations are for the series.
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	log.Printf("mem-pressure: %s", b)
	if pass != nil {
		r.Sessions = pass
		if b, err = json.Marshal(r); err != nil {
			return
		}
	}
	if s.series == nil {
		return
	}
	if err := s.series.Append(b); err != nil && !s.warnedWrite {
		s.warnedWrite = true // once: a full disk would otherwise add a line a minute to the log it is full of
		log.Printf("mem-pressure: series not written: %v", err)
	}
}

// Run ticks until stop is closed. The first sample is one Interval after the call, so the latency
// probe does not race the HTTP server that starts right after Start.
func (s *Sampler) Run(stop <-chan struct{}) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Tick()
		}
	}
}

// Default is the Agent's sampler; nil until Start has run (or when switched off).
var (
	defMu   sync.RWMutex
	Default *Sampler
)

// Current is the axis for the stats endpoint, nil when the sampler is not running.
func Current() *Wire {
	defMu.RLock()
	d := Default
	defMu.RUnlock()
	if d == nil {
		return nil
	}
	return d.Wire()
}

// Enabled reports whether the sampler is on: by default, off only with AF_MEM_PRESSURE=off.
func Enabled() bool { return os.Getenv("AF_MEM_PRESSURE") != "off" }
