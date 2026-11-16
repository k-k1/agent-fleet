package sessionx

// Which input may start a retired session again (ADR 0137 §6, the note of 2026-11-16). What
// starts it, and takes its mark off, is a deliberate act: the member's own (Start, a carried
// answer, a message typed to the session), or one of the member's own sessions or connections
// (a peer or an operator message, resume_session). What fires by itself never does: a schedule, a
// notice, a booked usage-limit resume, pull request feedback, a plan-review reviewer. When an
// origin cannot be told, it is not deliberate.

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// StartOriginQuery and StartOriginAutomatic are how a caller that fires by itself says so on
// POST /sessions/{name}/start (?origin=automatic). A caller that says nothing, or anything else,
// counts as deliberate: an older Control Plane relaying the Console's Start must keep working. An
// older Agent ignores the parameter it does not know.
const (
	// StartPeerFromQuery names the sending session on a start that precedes a peer message.
	StartPeerFromQuery = "peer_from"

	StartOriginQuery     = "origin"
	StartOriginAutomatic = "automatic"
)

// startIsAutomatic reports whether r is a start the Agent makes on its own (asAutomaticStart) or
// one its caller says fires by itself (?origin=automatic).
func startIsAutomatic(r *http.Request) bool {
	if r.Context().Value(autoStartKey{}) != nil {
		return true
	}
	if r.URL == nil {
		return false
	}
	q := r.URL.Query()
	if q.Get(StartOriginQuery) == StartOriginAutomatic {
		return true
	}
	// A peer message to a stopped session starts it first (mcpx.agentResumeAndSend) and says who
	// sends it: the start of a session for its own auto plan reviewer's reply is automatic. The
	// claim can only make a start stricter.
	if from := q.Get(StartPeerFromQuery); from != "" {
		// Read under the meta lock like the check that follows: a read that lands in the middle of
		// a writer must not turn an automatic sender into a deliberate one. A meta that exists but
		// cannot be read is in doubt, which is not deliberate.
		name := r.PathValue("name")
		dst, ok := readMetaStable(name)
		if (ok && planReviewerAutoReply(dst, from)) || (!ok && metaFileExists(name)) {
			return true
		}
	}
	return false
}

// autoInputKey marks a request to HandleSessionInput as one the Agent makes on its own (the plan
// review's follow-up to its reviewer): never deliberate, whatever fields its body carries.
type autoInputKey struct{}

// deliberateInput decides, from the fields of a /input body, whether the input is of a deliberate
// origin. Peer: peer_from. Operator: source "operator" (or none, with report_to), and "mcp", which
// the route stamps for a connection of the member's. The member: no origin field at all, or the
// chat bridge's "discord" / "slack". Schedule: schedule_id, schedule_slot, schedule_delivery or a
// schedule source. Any other source (notice, auto-resume, queue, spawn, one nobody knows) is
// automatic.
func deliberateInput(source, peerFrom, scheduleID, scheduleSlot string, scheduleDelivery bool) bool {
	if scheduleID != "" || scheduleSlot != "" || scheduleDelivery {
		return false
	}
	if peerFrom != "" {
		return true
	}
	switch source {
	case "", TurnSourceOperator, TurnSourceMCP, TurnSourceDiscord, TurnSourceSlack:
		return true
	}
	return false
}

// deliberateOrigin is deliberateInput for the in-process callers that carry an agents.Origin.
func deliberateOrigin(o agents.Origin) bool {
	switch o.Kind {
	case agents.OriginMember, agents.OriginDiscord, agents.OriginSlack, agents.OriginPeer,
		agents.OriginOperator, agents.OriginMCP:
		return true
	}
	return false
}

// planReviewerAutoReply reports whether a peer message to dst is the reply of a reviewer the
// Agent runs for it on its own (a plan review with auto). That reviewer was started by the
// Agent, not by the member, so its reply does not start a retired planner. The evidence is that
// the reviewer entry has an auto loop at all, not that it is enabled now: a verdict ends the loop
// (Enabled=false) before a refused or spooled reply is sent or delivered again, and the origin of
// that message must not change with it. When in doubt it is not deliberate.
func planReviewerAutoReply(dst session.Meta, from string) bool {
	for _, e := range dst.PlanReviewers {
		if e.Reviewer == from && e.Auto != nil {
			return true
		}
	}
	return false
}
