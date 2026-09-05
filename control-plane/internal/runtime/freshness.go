// freshness.go — the adapters' image-probe memo.
//
// It lives with the four Stale() probes that are its only writers and readers. The CP
// must not declare a second one: that variable could be reassigned (its tests used to do
// exactly that) while the adapters kept reading this one.
package runtime

import (
	"sync"
	"time"
)

// Freshness memoizes the adapters' image probes. /api/workspace is polled every 4s per
// open Console (and pushed over SSE), so an uncached docker inspect per request would
// multiply across tabs and users. The probed facts change only when an image is
// rebuilt or a container restarts, so a short TTL is plenty.
//
// Exported only so a test can swap in a clock-controlled cache; nothing outside this
// package reads it.
var Freshness = &TTLCache{m: map[string]TTLEntry{}}

type TTLEntry struct {
	v  string
	at time.Time
}

type TTLCache struct {
	mu  sync.Mutex
	m   map[string]TTLEntry
	now func() time.Time // tests
}

func (c *TTLCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the cached value for key, or runs load (OUTSIDE the lock, so one
// slow docker inspect never blocks another user's poll) and caches the result.
func (c *TTLCache) get(key string, ttl time.Duration, load func() string) string {
	c.mu.Lock()
	e, ok := c.m[key]
	fresh := ok && c.clock().Sub(e.at) < ttl
	c.mu.Unlock()
	if fresh {
		return e.v
	}
	v := load()
	c.mu.Lock()
	c.m[key] = TTLEntry{v: v, at: c.clock()}
	c.mu.Unlock()
	return v
}

// set overwrites an entry. Used when the caller has just learned the authoritative
// value (docker Start) and a stale cached one would produce a wrong answer.
func (c *TTLCache) set(key, v string) {
	c.mu.Lock()
	c.m[key] = TTLEntry{v: v, at: c.clock()}
	c.mu.Unlock()
}

// peek returns the last value stored under key REGARDLESS of the TTL, or "" if
// nothing was ever stored. It is the "last known good" reader: a caller that has
// just probed and got nothing can prefer an old-but-real answer over "unknown",
// where "unknown" would be more expensive than being slightly behind. Never use it
// for the comparison itself — that is what get's TTL exists for.
func (c *TTLCache) peek(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key].v
}
