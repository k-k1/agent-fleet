package sessionx

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// mainUsageCache keeps each session's folded main-transcript usage, keyed by the stamp the kind's
// UsageSource reports (size and mtime of every transcript copy, plus attribution). Without it the
// all-sessions usage read parses every main transcript on every call: measured on a 141-session
// workspace, 28 s on the first call after an agent restart (the relay gives up at 15 s) and 6-11 s
// warm. With it only a transcript that changed is parsed again.
type mainUsageCache struct {
	mu     sync.Mutex
	m      map[string]mainUsageEntry
	flight map[string]*mainUsageFlight
	idle   time.Duration
	seq    atomic.Uint64
	// sourceOf is UsageSourceOf; tests give a cache their own to count or order the reads.
	sourceOf func(session.Meta) (string, func() ([]transcript.Turn, bool))
}

// mainUsageFlight is one parse in progress; callers of the same session and stamp wait for it.
type mainUsageFlight struct {
	done chan struct{}
	u    sessionUsage
	ok   bool
}

type mainUsageEntry struct {
	createdAt string // a recreated name is a new session; never serve its predecessor's figure
	stamp     string
	seq       uint64 // order in which stamps were taken; the later stamp's figure stands
	val       sessionUsage
	used      time.Time
}

func newMainUsageCache(idle time.Duration) *mainUsageCache {
	return &mainUsageCache{m: map[string]mainUsageEntry{}, flight: map[string]*mainUsageFlight{}, idle: idle, sourceOf: UsageSourceOf}
}

// get returns m's main-transcript usage; see getBy.
func (c *mainUsageCache) get(m session.Meta, now time.Time) sessionUsage {
	u, _ := c.getBy(m, now, time.Time{})
	return u
}

// getBy returns m's main-transcript usage, or ok=false without parsing when the figure is not
// cached and deadline (zero: none) has passed. The stamp is taken before the read, so a write
// that lands during the parse leaves the cached stamp behind the file and the next call parses
// again. A kind with no stamp, or a read that reports it may be stale, is never cached. Entries
// not read for idle are dropped on every call, so deleted and archived sessions cannot accumulate.
//
// Concurrent callers (the boot warm-up and a request) share one parse per session and stamp.
// When two parses of different stamps overlap, the one whose stamp was taken later wins the store
// (seq), so a slow parse of an older stamp never overwrites a newer entry.
func (c *mainUsageCache) getBy(m session.Meta, now, deadline time.Time) (sessionUsage, bool) {
	pastDeadline := func() bool { return !deadline.IsZero() && time.Now().After(deadline) }
	if pastDeadline() {
		// Past the budget an uncached session costs nothing, not even the stamp's disk reads:
		// 100+ of those on a loaded host would spend the time the budget exists to protect.
		c.mu.Lock()
		_, cached := c.m[m.Name]
		c.mu.Unlock()
		if !cached {
			return sessionUsage{}, false
		}
	}
	seq := c.seq.Add(1)
	stamp, load := c.sourceOf(m)
	var key string
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
		if pastDeadline() {
			c.mu.Unlock()
			return sessionUsage{}, false
		}
		key = m.Name + "\x00" + m.CreatedAt + "\x00" + stamp
		if f, ok := c.flight[key]; ok {
			c.mu.Unlock()
			<-f.done
			return cloneUsage(f.u), f.ok
		}
		f := &mainUsageFlight{done: make(chan struct{})}
		c.flight[key] = f
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.flight, key)
			c.mu.Unlock()
			close(f.done)
		}()
		turns, ok := load()
		u := AggregateUsage(turns)
		f.u, f.ok = cloneUsage(u), true
		if !ok {
			return u, true
		}
		c.mu.Lock()
		if e, ok := c.m[m.Name]; ok && e.createdAt == m.CreatedAt && e.seq > seq {
			if e.used.After(now) {
				now = e.used
			}
			c.mu.Unlock()
			return u, true // a parse whose stamp was taken later has already stored its figure
		}
		if e, ok := c.m[m.Name]; ok && e.used.After(now) {
			now = e.used // never move the idle clock backwards
		}
		c.m[m.Name] = mainUsageEntry{createdAt: m.CreatedAt, stamp: stamp, seq: seq, val: cloneUsage(u), used: now}
		c.pruneLocked(now)
		c.mu.Unlock()
		return u, true
	}
	if pastDeadline() {
		return sessionUsage{}, false
	}
	turns, _ := load()
	return AggregateUsage(turns), true
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

// mainUsageColdBudget bounds how long one all-sessions read waits for sessions whose figures are
// not cached (allSessionsUsage). The relay gives up at 15 s; a cold cache means parsing every
// transcript (28 s measured on 141 sessions). Past the budget the unfinished sessions are counted
// as pending while a background pass fills the cache, so the next call is warm.
var mainUsageColdBudget = 8 * time.Second

// usageCacheIdle is how long the usage read's caches keep a session nobody has read. Every entry
// is checked against its file stamps before it is served, so age never makes one wrong; the drop
// exists only so deleted and archived sessions do not pile up. A short idle turns a quiet spell
// into a cold call: at 10 minutes every first read after a break re-parsed every transcript
// (measured: 28 s, against 0.5 s warm on the same 117 sessions). The figures are a few words per
// session and per subagent, so keeping them for a day costs nothing worth saving.
const usageCacheIdle = 24 * time.Hour

var mainUsage = newMainUsageCache(usageCacheIdle)

// usageMetas lists the sessions the usage read reports: not archived, and of a kind that keeps a
// transcript (shell and ssm do not).
func usageMetas() []session.Meta {
	var out []session.Meta
	for _, m := range session.ListMetas() {
		if !m.Archived && AgentOf(m.Kind).Caps().CanTranscript {
			out = append(out, m)
		}
	}
	return out
}

// WarmSessionUsage starts filling the usage read's caches (main transcript and subagents) for every
// transcript-capable session, so the first all-sessions read after an agent restart finds them
// warm. It joins the running pass when there is one, and does not wait for it.
func WarmSessionUsage() { joinUsagePass(usageMetas()) }
