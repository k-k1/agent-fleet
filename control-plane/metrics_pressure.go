package main

import (
	"context"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
)

// The docker local path answers the gauges from the host's cgroup read, which the Agent
// never sees, so `mem_pressure` (ADR 0120 phase 0) would be absent there for good. It
// exists only in the Agent's answer, and the Agent alone owns its semantics (boundary
// check, available:false with a reason, latency probe), so the CP asks the Agent for that
// one axis and merges nothing else; the other gauges stay from the local read.
//
// workspaceStats runs on the 4 s events tick for every member, so the ask never sits on
// that path: the tick reads a per-workspace cache and, when it is stale, starts one
// background fetch with a short timeout. A slow, old or dead Agent therefore costs the
// local gauges nothing; it only leaves the axis absent.
const (
	// pressureRefresh is the Agent's sampling period; asking more often returns the same sample.
	pressureRefresh = 5 * time.Second
	// pressureMaxAge bounds how long a sample is shown after the Agent stopped answering:
	// a frozen figure would read as a calm machine.
	pressureMaxAge = 30 * time.Second
	// pressureBackoff spaces retries after a failure (an old Agent answers 404 every time).
	pressureBackoff = 15 * time.Second
)

// pressureFetchTimeout is a var so tests can shorten it.
var pressureFetchTimeout = 2 * time.Second

type pressureEntry struct {
	run      string       // container run this entry belongs to
	gen      uint64       // identifies this entry; a response for another generation is discarded
	mp       *memPressure // nil = unmeasured; never a zero-valued stand-in
	okAt     time.Time    // when mp was fetched
	tryAt    time.Time    // when the last fetch was started
	failed   bool         // the last fetch failed
	inflight bool
}

// pressureSweepEvery / pressureIdleAfter bound the map: entries of workspaces nobody has polled
// for pressureIdleAfter (deleted or replaced members) are collected, never one with a fetch in flight.
const (
	pressureSweepEvery = time.Minute
	pressureIdleAfter  = 10 * time.Minute
)

type pressureCache struct {
	mu      sync.Mutex
	m       map[string]*pressureEntry
	nextGen uint64
	sweptAt time.Time
}

var memPressures = &pressureCache{m: map[string]*pressureEntry{}}

// lookup returns the sample to show and whether the caller must start a fetch (and has then
// claimed the single in-flight slot, identified by gen). The returned pointer is shared and
// must not be mutated.
func (c *pressureCache) lookup(key, run string, now time.Time) (mp *memPressure, gen uint64, fetch bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	e := c.m[key]
	if e == nil || e.run != run {
		// A different run of the container (replaced, or restarted in place) is a different
		// machine: its predecessor's sample and any fetch still in flight for it are dropped.
		c.nextGen++
		e = &pressureEntry{gen: c.nextGen, run: run}
		c.m[key] = e
	}
	if e.mp != nil && now.Sub(e.okAt) < pressureMaxAge {
		mp = e.mp
	}
	wait := pressureRefresh
	if e.failed {
		wait = pressureBackoff
	}
	if !e.inflight && (e.tryAt.IsZero() || now.Sub(e.tryAt) >= wait) {
		e.inflight = true
		e.tryAt = now
		fetch = true
	}
	return mp, e.gen, fetch
}

func (c *pressureCache) sweepLocked(now time.Time) {
	if now.Sub(c.sweptAt) < pressureSweepEvery {
		return
	}
	c.sweptAt = now
	for k, e := range c.m {
		last := e.tryAt
		if e.okAt.After(last) {
			last = e.okAt
		}
		if !e.inflight && now.Sub(last) > pressureIdleAfter {
			delete(c.m, k)
		}
	}
}

// invalidate forgets a workspace's sample (it stopped, so the next run is a different machine)
// and orphans any fetch in flight: finish rejects a response whose generation is gone.
func (c *pressureCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// finish records a fetch result. A failure drops the sample at once: a value kept through an
// outage would read as a calm machine.
func (c *pressureCache) finish(key string, gen uint64, mp *memPressure, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[key]
	if e == nil || e.gen != gen {
		return
	}
	e.inflight = false
	e.failed = mp == nil
	e.mp, e.okAt = mp, now
	if mp == nil {
		e.okAt = time.Time{}
	}
}

// agentMemPressure is the local path's mem_pressure: whatever the cache holds, refreshed in
// the background. nil means absent from the payload.
func agentMemPressure(ctx context.Context, m *manager, rt runtime.Runtime, run string) *memPressure {
	key := rt.Name()
	mp, gen, fetch := memPressures.lookup(key, run, time.Now())
	if fetch {
		// Detached from the tick's context: the tick returns before the fetch does.
		bg := context.WithoutCancel(ctx)
		go func() {
			fctx, cancel := context.WithTimeout(bg, pressureFetchTimeout)
			defer cancel()
			var got *memPressure
			if s, err := m.agentStats(fctx, rt); err == nil {
				got, _ = s["mem_pressure"].(*memPressure)
			}
			memPressures.finish(key, gen, got, time.Now())
		}()
	}
	return mp
}
