package agents

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ManagedProgressReporter is an optional Driver capability: when a Managed session last showed
// a sign of life, for the one-hour bound on a busy row's Workspace hold (sessionx/progress.go).
// A kind implements it only after the signal was MEASURED to stay fresh through a long silent
// tool (ADR 0055, 2026-10-17 addendum); ok=false means "cannot tell", which the caller holds on.
type ManagedProgressReporter interface {
	ManagedProgress(m session.Meta) (at time.Time, ok bool)
}

// activityFresh is how recent the runtime's last message must be for the tool probe to be
// skipped: against an hour-scale lapse "now" and "a minute ago" are the same answer, and the
// probe reads the process table.
const activityFresh = time.Minute

// Activity stamps when a Managed runtime was last heard from. The zero value is ready.
type Activity struct{ ns atomic.Int64 }

// Touch records a message from the runtime. It is one atomic store, safe on a read loop.
func (a *Activity) Touch() { a.ns.Store(time.Now().UnixNano()) }

// At is the last Touch; ok=false when the runtime was never heard from.
func (a *Activity) At() (time.Time, bool) {
	n := a.ns.Load()
	if n == 0 {
		return time.Time{}, false
	}
	return time.Unix(0, n), true
}

// TouchAt records a message from the runtime at t; for restoring a stamp and for tests.
func (a *Activity) TouchAt(t time.Time) { a.ns.Store(t.UnixNano()) }

// Progress is the newest sign of life: the last message from the runtime, or now while
// toolAlive says a tool is running (a silent tool sends nothing, measured per kind). toolAlive
// returns observed=false when the table or the stream that would show a tool cannot be trusted
// (a failed /proc read, a gap in the event stream): "could not observe" is not "no tool", so a
// stale stamp then gives no answer and the row keeps holding. toolAlive may be nil for a kind
// that has no such probe, and is called only when the last message is not fresh.
func (a *Activity) Progress(now time.Time, toolAlive func() (alive, observed bool)) (time.Time, bool) {
	at, ok := a.At()
	if ok && now.Sub(at) < activityFresh {
		return at, true
	}
	if toolAlive != nil {
		alive, observed := toolAlive()
		switch {
		case alive:
			return now, true
		case !observed:
			return time.Time{}, false
		}
	}
	return at, ok
}

// InFlight is the set of calls a runtime announced and has not finished, for a runtime whose
// silent tool leaves no other trace. It is only trusted from a point where the stream is known
// to be continuous: the zero value, and a gap in the stream (reconnect, adopted daemon,
// runtime lost), are unknown until Reset says a turn boundary was seen on a live stream.
type InFlight struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	known bool
}

// Add opens a call; it is idempotent, so an event that restates a running call restores it.
func (f *InFlight) Add(id string) {
	if id == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ids == nil {
		f.ids = map[string]struct{}{}
	}
	f.ids[id] = struct{}{}
}

// Done closes a call.
func (f *InFlight) Done(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.ids, id)
}

// Reset drops every open call and trusts the set again: a turn boundary on a live stream means
// nothing from before it can still be running.
func (f *InFlight) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids, f.known = nil, true
}

// Unknown drops the set and stops trusting it until the next Reset.
func (f *InFlight) Unknown() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids, f.known = nil, false
}

// State is the toolAlive probe of Activity.Progress. A call in the set is alive even when the
// stream was interrupted since: it was seen running, and only its end event can close it.
func (f *InFlight) State() (alive, observed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ids) > 0, f.known
}
