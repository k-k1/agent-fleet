package sessionx

import (
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Session.BackgroundSince / BackgroundAgeSec: how long the current idle-with-background-work
// period has lasted, so the Control Plane can stop a long-lived background process (a dev
// server's wrapper shell) from holding its Workspace awake forever (#2338).
//
// Kept in memory per session: the first poll that saw BackgroundBusy starts the period and the
// first poll that sees it false (or the session dead) ends it. It is generic on purpose — every
// kind that reports BackgroundBusy goes through here, none has its own copy. An Agent restart
// starts the period over: each restart resets the bound, so repeated restarts more frequent than
// the bound can keep extending the hold.
//
// The age is computed on this clock for the same reason ProgressAgeSec is: the CP compares it
// with a threshold and must not depend on its own clock agreeing with the Workspace's.

var (
	backgroundSeenMu  sync.Mutex
	backgroundSeenMap = map[string]time.Time{}
)

// backgroundSinceOf records this poll's observation and returns when the current background
// period began (ok=false when there is none, which also forgets the session).
func backgroundSinceOf(name string, busy, alive bool, now time.Time) (time.Time, bool) {
	backgroundSeenMu.Lock()
	defer backgroundSeenMu.Unlock()
	if !alive || !busy {
		delete(backgroundSeenMap, name)
		return time.Time{}, false
	}
	since, ok := backgroundSeenMap[name]
	if !ok {
		since = now
		backgroundSeenMap[name] = since
	}
	return since, true
}

// pruneBackgroundSince drops the periods of sessions that no longer appear in the list (deleted,
// or archived, which the list skips), so the map cannot outgrow it. The list handler calls it
// with the metas it just read.
func pruneBackgroundSince(listed func(name string) bool) {
	backgroundSeenMu.Lock()
	defer backgroundSeenMu.Unlock()
	for name := range backgroundSeenMap {
		if !listed(name) {
			delete(backgroundSeenMap, name)
		}
	}
}

// fillBackgroundSince sets the two wire fields from backgroundSinceOf.
func fillBackgroundSince(s *session.Session, now time.Time) {
	since, ok := backgroundSinceOf(s.Name, s.BackgroundBusy, s.Alive, now)
	if !ok {
		return
	}
	s.BackgroundSince = since.Format(time.RFC3339)
	if age := int(now.Sub(since) / time.Second); age > 0 {
		s.BackgroundAgeSec = age
	}
}
