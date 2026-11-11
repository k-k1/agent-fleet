package sessionx

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// mainUsageCache keeps each session's folded main-transcript usage, keyed by the stamp the kind's
// UsageSource reports (size and mtime of every transcript copy, plus attribution). Without it the
// all-sessions usage read parses every main transcript on every call: measured on a 141-session
// workspace, 28 s on the first call after an agent restart (the relay gives up at 15 s) and 6-11 s
// warm. With it only a transcript that changed is parsed again.
type mainUsageCache struct {
	mu   sync.Mutex
	m    map[string]mainUsageEntry
	idle time.Duration
}

type mainUsageEntry struct {
	createdAt string // a recreated name is a new session; never serve its predecessor's figure
	stamp     string
	val       sessionUsage
	used      time.Time
}

func newMainUsageCache(idle time.Duration) *mainUsageCache {
	return &mainUsageCache{m: map[string]mainUsageEntry{}, idle: idle}
}

var mainUsage = newMainUsageCache(10 * time.Minute)

// get returns m's main-transcript usage; see getBy.
func (c *mainUsageCache) get(m session.Meta, now time.Time) sessionUsage {
	u, _ := c.getBy(m, now, time.Time{})
	return u
}

// getBy returns m's main-transcript usage, or ok=false without parsing when the figure is not
// cached and deadline (zero: none) has passed. The stamp is taken before the read, so a write that lands
// during the parse leaves the cached stamp behind the file and the next call parses again. A kind
// with no stamp, or a read that reports it may be stale, is never cached. Entries not read for
// idle are dropped on every call, so deleted and archived sessions cannot accumulate.
func (c *mainUsageCache) getBy(m session.Meta, now, deadline time.Time) (sessionUsage, bool) {
	stamp, load := UsageSourceOf(m)
	if stamp != "" {
		c.mu.Lock()
		e, ok := c.m[m.Name]
		if ok && e.createdAt == m.CreatedAt && e.stamp == stamp {
			if now.After(e.used) {
				e.used = now
				c.m[m.Name] = e
			}
			c.pruneLocked(now)
			c.mu.Unlock()
			return cloneUsage(e.val), true
		}
		c.mu.Unlock()
	}
	if !deadline.IsZero() && time.Now().After(deadline) {
		return sessionUsage{}, false
	}
	turns, ok := load()
	u := AggregateUsage(turns)
	if stamp == "" || !ok {
		return u, true
	}
	c.mu.Lock()
	c.m[m.Name] = mainUsageEntry{createdAt: m.CreatedAt, stamp: stamp, val: cloneUsage(u), used: now}
	c.pruneLocked(now)
	c.mu.Unlock()
	return u, true
}

func (c *mainUsageCache) pruneLocked(now time.Time) {
	for name, e := range c.m {
		if now.Sub(e.used) > c.idle {
			delete(c.m, name)
		}
	}
}

// cloneUsage copies the one pointer a caller could write through.
func cloneUsage(u sessionUsage) sessionUsage {
	if u.Context != nil {
		c := *u.Context
		u.Context = &c
	}
	return u
}

func (c *mainUsageCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// mainUsageColdBudget bounds how long one all-sessions read spends parsing transcripts that are
// not cached. The relay gives up at 15 s; the first call after an agent restart has an empty
// cache, so without a bound it parses every transcript (28 s measured on 141 sessions). Past the
// budget the remaining uncached sessions are left out and counted as pending while a background
// pass fills the cache, so the next call is warm.
var mainUsageColdBudget = 8 * time.Second

var mainUsageWarming atomic.Bool

// WarmMainUsage fills the cache for every transcript-capable session, one at a time, so the first
// all-sessions read after an agent restart finds it warm. Only one pass runs at a time.
func WarmMainUsage() {
	if !mainUsageWarming.CompareAndSwap(false, true) {
		return
	}
	defer mainUsageWarming.Store(false)
	for _, m := range session.ListMetas() {
		if m.Archived || !AgentOf(m.Kind).Caps().CanTranscript {
			continue
		}
		mainUsage.get(m, time.Now())
	}
}
