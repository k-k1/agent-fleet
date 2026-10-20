package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Managed rows have no pane, so the signs progressOf reads for Terminal rows do not exist.
// A Managed row reports progress only for a kind whose driver implements
// agents.ManagedProgressReporter, which a kind earns by a MEASURED signal that stays fresh
// through a long silent tool (ADR 0055, 2026-10-17 addendum). Every other Managed kind keeps
// holding without bound: "could not observe" must never read as "not moving".

// managedProgressMeasured reports whether this Managed row's kind has a measured signal.
func managedProgressMeasured(m session.Meta) bool {
	_, ok := managedDrivers[NormalizeKind(m.Kind)].(agents.ManagedProgressReporter)
	return ok
}

// managedProgressAt asks the kind's driver when its live handle last showed a sign of life.
func managedProgressAt(m session.Meta) (time.Time, bool) {
	r, ok := managedDrivers[NormalizeKind(m.Kind)].(agents.ManagedProgressReporter)
	if !ok {
		return time.Time{}, false
	}
	return r.ManagedProgress(m)
}

// managedProgressOf is progressOf for a trusted Managed row: the newest of the driver's own
// sign and the status file's mtime (written on every state change). A driver that cannot tell
// (no live handle, never heard from) gives no answer, so the CP keeps holding. An mtime or
// stamp in the future is ignored for the reason progressOf gives.
func managedProgressOf(m session.Meta, p progressProbes) (time.Time, bool) {
	now := p.now()
	var newest time.Time
	take := func(t time.Time, ok bool) {
		if !ok || t.After(now.Add(progressSkew)) {
			return
		}
		if t.After(now) {
			t = now
		}
		if t.After(newest) {
			newest = t
		}
	}
	at, ok := p.managedAt(m)
	if !ok {
		return time.Time{}, false
	}
	take(at, true)
	take(p.statusAt(session.UUID(m.Dir, m.Name)))
	return newest, !newest.IsZero()
}
