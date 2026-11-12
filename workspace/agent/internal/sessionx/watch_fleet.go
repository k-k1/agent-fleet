package sessionx

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/watch"
)

// The fleet watch source (ADR 0133 §9) reads other sessions of this Workspace through the same
// functions the session list and the usage-limit sweep use, so it has no classifier of its own:
// idle is what wireSession reports (the state fleetgraph.ObserveState records), and a limit is
// the episode rate_limit_resume.go keeps.

// Side effects, replaceable by tests.
var (
	fleetWire     = wireSession
	fleetAlive    = SessionAlive
	fleetNow      = time.Now
	fleetPeerList = PeerReachableSessions
	fleetPeersOn  = uiprefs.PeerMessaging
)

type watchFleet struct{}

// Visible is what list_child_sessions or list_peer_sessions would show the caller: its own
// children always, any other session of the Workspace while peer messaging is on. Both lists are
// of this Workspace, which belongs to one member, so no other member's session can pass.
func (watchFleet) Visible(caller, name string) bool {
	if !session.ValidName(caller) || !session.ValidName(name) || caller == name {
		return false
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Archived {
		return false
	}
	if stalledTreeParent(m) == caller {
		return true
	}
	if !fleetPeersOn() {
		return false
	}
	for _, p := range fleetPeerList(caller) {
		if p.Name == name {
			return true
		}
	}
	return false
}

func (watchFleet) Observe(name string) (watch.FleetObservation, bool) {
	m, ok := session.ReadMeta(name)
	if !ok || m.Archived {
		return watch.FleetObservation{}, false
	}
	alive := fleetAlive(m)
	obs := watch.FleetObservation{Alive: alive, Limit: fleetLimitEpisode(m.Name, fleetNow())}
	if alive {
		s := fleetWire(m, true)
		state, _ := fleetgraph.NormalizeState(s.State)
		obs.Idle = state == fleetgraph.StateIdle
		obs.LastTurnEndAt = s.LastTurnEndAt
	}
	return obs, true
}

// fleetLimitEpisode names the usage-limit episode that holds the session back now, "" when none
// does. An episode with a reset instant is over at that instant (the booked resume is sent a
// little later, and the session can work from the reset on); one without an instant, or a spend
// limit, lasts until the sweep retires it.
func fleetLimitEpisode(name string, now time.Time) string {
	st, ok := RateLimitStates.Read(name)
	if !ok || st.At == "" {
		return ""
	}
	if !st.Spend && st.ResumeAt != "" {
		if t, err := time.Parse(time.RFC3339, st.ResumeAt); err == nil && !now.Before(t) {
			return ""
		}
	}
	return st.At
}
