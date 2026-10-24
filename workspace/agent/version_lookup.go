package main

// version_lookup.go — the cache behind "what is the latest patch of this major?" for
// Settings → Toolchains (jdk-latest, node-latest).
//
// The answer comes from the network (Adoptium, nodejs.org) and the Console asks on
// every tab open, so it is cached per major: a success for latestTTL, a failure for
// latestFailTTL so an offline workspace does not retry on every open. Concurrent
// callers for one major share a single in-flight fetch. Without that, two tab opens
// both fetched and whichever finished last won the cache: a timeout landing after a
// success hid the update for the failure TTL.
//
// Callers must only ask for majors the picker offers; the keys are not otherwise
// bounded.

import (
	"errors"
	"sync"
	"time"
)

const (
	latestTTL     = 6 * time.Hour
	latestFailTTL = 10 * time.Minute
)

type latestEntry struct {
	version string
	err     string
	at      time.Time
}

func (e latestEntry) ttl() time.Duration {
	if e.err != "" {
		return latestFailTTL
	}
	return latestTTL
}

func (e latestEntry) result() (string, error) {
	if e.err != "" {
		return "", errors.New(e.err)
	}
	return e.version, nil
}

type latestCall struct {
	done chan struct{}
	e    latestEntry
}

type versionLookup struct {
	mu       sync.Mutex // guards cache and inflight
	cache    map[string]latestEntry
	inflight map[string]*latestCall
	// fetch and now are fields so tests can answer without the network or a clock.
	fetch func(major string) (string, error)
	now   func() time.Time
}

func newVersionLookup(fetch func(string) (string, error)) *versionLookup {
	return &versionLookup{
		cache:    map[string]latestEntry{},
		inflight: map[string]*latestCall{},
		fetch:    fetch,
		now:      time.Now,
	}
}

// latest returns the cached latest version of a major, fetching when the entry is
// missing or stale.
func (l *versionLookup) latest(major string) (string, error) {
	now := l.now()
	l.mu.Lock()
	if e, ok := l.cache[major]; ok && now.Sub(e.at) < e.ttl() {
		l.mu.Unlock()
		return e.result()
	}
	if c := l.inflight[major]; c != nil {
		l.mu.Unlock()
		<-c.done
		return c.e.result()
	}
	c := &latestCall{done: make(chan struct{})}
	l.inflight[major] = c
	l.mu.Unlock()

	v, err := l.fetch(major)
	c.e = latestEntry{version: v, at: now}
	if err != nil {
		c.e.err = err.Error()
	}
	l.mu.Lock()
	l.cache[major] = c.e
	delete(l.inflight, major)
	l.mu.Unlock()
	close(c.done)
	return c.e.result()
}

// toolchainLatestReply is the answer of GET /env/jdk-latest and /env/node-latest.
type toolchainLatestReply struct {
	Major string `json:"major"`
	// Current is the installed version the toolchain resolves to; "" = not installed
	// (or no readable version), so there is nothing to update and Latest is what
	// Install would download.
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
}

func isOffered(major string, opts []string) bool {
	for _, v := range opts {
		if v == major {
			return true
		}
	}
	return false
}
