package main

import (
	"sort"
	"time"
)

// idleForecast answers "when does this workspace stop, and if it doesn't, who is holding
// it open" (docs/log/75 P4). The reaper puts one in the manager on every sweep and the
// admin screen reads it.
//
// Without it an operator has nothing to look at when auto-stop appears not to work: the
// reaper only logs, and the only way to investigate was to docker exec into someone else's
// container and read a status file. There are now several ways to hold a workspace awake
// (waiting on a human, background work, presence, a pin), so an invisible decision is one
// the operator cannot explain.
type idleForecast struct {
	// Enabled reports whether tier 2 (workspace stop) is on for this tenant. A timeout of
	// 0 must read as "the feature is switched off", not "nothing scheduled" — otherwise a
	// misconfiguration is indistinguishable from an idle countdown.
	Enabled bool `json:"enabled"`
	// StopAt is when the stop would happen if the current observation holds. Meaningful
	// only while Holders is empty.
	StopAt time.Time `json:"stopAt,omitempty"`
	// Holders are the reasons not to stop. Empty = nothing is holding it (the clock is
	// running towards StopAt).
	Holders []idleHolder `json:"holders,omitempty"`
	// ObservedAt is when this was read. The screen has to be able to say as of when it is
	// talking: the value is a sweep interval old, so it must not be presented to the
	// second.
	ObservedAt time.Time `json:"observedAt"`
}

// idleHolder is one reason not to stop. Kind is the vocabulary the screen picks its wording
// from; Session names the session when there is one.
type idleHolder struct {
	// Kind: "working" (a turn is running) / "background" (background job or subagent) /
	// "pin" (pinned against auto-stop) / "watching" (a human is touching it) / "recent"
	// (recent activity) / "repojob" (a repository import is in flight — docs/log/78)
	Kind    string `json:"kind"`
	Session string `json:"session,omitempty"`
	// Until is the pin's expiry, set only when Kind=="pin".
	Until string `json:"until,omitempty"`
}

// holdersOf builds the reasons not to stop from one sweep's session list and presence. A
// pure function, fed the same inputs as the reaper's own decision and ordered the same way.
func holdersOf(sessions []sessionWire, watched bool, now time.Time, repoJobs int) []idleHolder {
	var out []idleHolder
	for _, s := range sessions {
		if !s.Alive {
			continue
		}
		switch {
		case keepAwake(s.KeepAwakeUntil, now):
			// A pin is checked before working: something the user declared explicitly
			// explains more than a turn that happens to be running right now, and it
			// points at the action that ends it (remove the pin).
			out = append(out, idleHolder{Kind: "pin", Session: s.Name, Until: s.KeepAwakeUntil})
		case s.BackgroundBusy:
			out = append(out, idleHolder{Kind: "background", Session: s.Name})
		case busyState(s.State):
			// Never enumerate state names here — go through the same predicate the
			// reaper's busy check (sessionActivity) uses. Listing them by hand made
			// compacting machineBusy for the reaper while the screen showed empty
			// holders and a StopAt (docs/log/75 decision 11).
			out = append(out, idleHolder{Kind: "working", Session: s.Name})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Session < out[j].Session })
	if repoJobs > 0 {
		// The one kind of work that belongs to no session. Leave it out and a workspace
		// mid-import looks like "holders empty, yet it sails past StopAt without
		// stopping", which the operator cannot explain.
		out = append(out, idleHolder{Kind: "repojob"})
	}
	if watched {
		// Placed after the session-derived reasons: "someone is watching" is not
		// something the reader can act on, whereas a running session points at a
		// concrete choice (wait for it, or stop it).
		out = append(out, idleHolder{Kind: "watching"})
	}
	return out
}

// putIdleForecast records the reaper's latest read of one workspace.
func (m *manager) putIdleForecast(wsID string, f idleForecast) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idleForecasts == nil {
		m.idleForecasts = map[string]idleForecast{}
	}
	m.idleForecasts[wsID] = f
}

// idleForecastFor returns the last recorded read, if any.
//
// A stale read is returned as is, never expired by a TTL: "no observation" and "an
// observation from a minute ago" mean different things on screen, and the second is
// something the reader can judge for themselves once ObservedAt is shown. A deployment
// whose reaper is not running never records anything at all, so that case stays
// distinguishable too.
func (m *manager) idleForecastFor(wsID string) (idleForecast, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.idleForecasts[wsID]
	return f, ok
}
