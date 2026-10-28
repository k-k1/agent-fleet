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
	mp       *memPressure // nil = unmeasured; never a zero-valued stand-in
	okAt     time.Time    // when mp was fetched
	tryAt    time.Time    // when the last fetch was started
	failed   bool         // the last fetch failed
	inflight bool
}

type pressureCache struct {
	mu sync.Mutex
	m  map[string]*pressureEntry
}

var memPressures = &pressureCache{m: map[string]*pressureEntry{}}

// lookup returns the sample to show and whether the caller must start a fetch (and has then
// claimed the single in-flight slot). The returned pointer is shared and must not be mutated.
func (c *pressureCache) lookup(key string, now time.Time) (mp *memPressure, fetch bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[key]
	if e == nil {
		e = &pressureEntry{}
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
	return mp, fetch
}

func (c *pressureCache) finish(key string, mp *memPressure, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[key]
	if e == nil {
		return
	}
	e.inflight = false
	e.failed = mp == nil
	if mp != nil {
		e.mp, e.okAt = mp, now
	}
}

// agentMemPressure is the local path's mem_pressure: whatever the cache holds, refreshed in
// the background. nil means absent from the payload.
func agentMemPressure(ctx context.Context, m *manager, rt runtime.Runtime) *memPressure {
	key := rt.Name()
	mp, fetch := memPressures.lookup(key, time.Now())
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
			memPressures.finish(key, got, time.Now())
		}()
	}
	return mp
}
