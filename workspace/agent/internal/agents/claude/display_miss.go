package claude

import (
	"sync"
	"time"
)

// The session card asks for a claude session's transcript three times per list poll (the Remote
// Control link, the context gauge, the last thing said). For a session with no transcript each of
// those asks is a sweep of every project directory on the claude mount, because jsonlMemo never
// remembers a miss — and sessions that never talked keep no transcript forever. Measured on a
// production workspace with 627 project directories: 3 sweeps per GET /sessions per stopped
// miss (ADR 0087).
//
// So the CARD alone may remember "no transcript" for a while. Nothing else may: the resume
// decision (SessionJSONLExists), delivery confirmation, completion reports, background-work
// detection (it feeds the reaper through BackgroundBusy) and the fork source all keep asking
// the disk. That is why this is a separate memo with a separate entry point instead of an
// option on pathMemo — a remembered miss there would make buildProgram pass --session-id for a
// session that has a log.
//
// A stopped session's transcript cannot grow, so its miss is trusted for long; a live one can
// get its first turn at any moment, so the card lags its transcript by at most the short TTL.
// A change of liveness discards the remembered miss.
const (
	displayMissTTLLive    = 15 * time.Second
	displayMissTTLStopped = 10 * time.Minute
)

// missMemo remembers misses for ttlLive / ttlStopped (each instance sets both).
type missMemo struct {
	mu                  sync.Mutex
	seen                map[string]missEntry
	ttlLive, ttlStopped time.Duration
}

type missEntry struct {
	at    time.Time
	alive bool
}

func (m *missMemo) holds(key string, alive bool, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.seen[key]
	if !ok || e.alive != alive {
		return false
	}
	ttl := m.ttlStopped
	if alive {
		ttl = m.ttlLive
	}
	return now.Sub(e.at) < ttl
}

func (m *missMemo) note(key string, alive bool, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]missEntry{}
	}
	m.seen[key] = missEntry{at: now, alive: alive}
}

func (m *missMemo) forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.seen, key)
}

var displayMisses = missMemo{ttlLive: displayMissTTLLive, ttlStopped: displayMissTTLStopped}

// displayHasTranscript reports whether sid has a transcript, for the session card only. A false
// may be up to displayMissTTLLive / displayMissTTLStopped old; never call it from anything that
// decides what to launch, deliver, report or reap.
func displayHasTranscript(sid string, alive bool) bool {
	key := ConfigDir() + "\x00" + sid
	now := time.Now()
	if displayMisses.holds(key, alive, now) {
		return false
	}
	if len(jsonlPaths(sid)) > 0 {
		displayMisses.forget(key)
		return true
	}
	displayMisses.note(key, alive, now)
	return false
}
