package sessionx

// Turn ends that only a POLL can observe (docs/log/89 §89.3).
//
// agy / copilot / cursor / kiro ship no Stop hook, so on the TUI route nothing tells us a turn
// finished: the end has to be READ off the kind's own state source (agy's conversation DB,
// copilot's events.jsonl, cursor's JSONL tail, kiro's TUI string contract) by whoever happens
// to poll. Two routes poll them, and they must do DIFFERENT amounts of work:
//
//   - DriveState (get_session_status / the chat chip / the mirror) — the ONLY place the
//     completion can be turned into a notification and the operator's completion report
//     (docs/log/30 ②). It has always done this and still does, unchanged.
//   - wireSession (GET /sessions — the list the Console hammers and list_child_sessions reads)
//     — records WHEN the turn ended and nothing else. A read route that fires notifications and
//     consumes docs/log/51's report arm is a defect of its own, which is why §89.3 left the
//     listing lagging rather than call MarkTurnEnd here.
//
// The trap this file exists to avoid: "record the end" must NOT be spelled as "persist idle".
// MarkTurnEnd's write (status.PersistTurnEnd) settles the state to idle, and the notification
// gate below is `the persisted state is still working` — so a list poll that persisted idle
// would consume the gate, and the completion notification and report of all four kinds would
// silently stop arriving. status.RecordTurnEnd therefore records the time in a store of its
// own, touching no part of the status record — not even to add a field to it, which as a
// read-modify-write destroyed the notification route's concurrent write outright
// (status.ObservedTurnEnd). The two gates below stay deliberately different:
//
//	notify: the persisted state is "working"                (fires once per turn, unchanged)
//	record: … and this turn's end is not stamped yet        (idempotent, consumes nothing)

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// notifyPolledTurnEnd is the DriveState half: the poll observed state (from the kind's own
// source) says the turn is over, so persist idle AND fire the notification. Gated on the
// persisted state being "working" so repeated polls report once; a duplicate from two
// concurrent polls is absorbed by handleChatReport's disarm. Under managed the driver's runTurn
// already fired it and the persisted state is idle — no double fire.
func notifyPolledTurnEnd(m session.Meta, state string) {
	sid := session.UUID(m.Dir, m.Name)
	if state != "idle" || status.LiveState(sid) != "working" || !idleEndsThisTurn(m, sid) {
		return
	}
	agents.MarkTurnEnd(sid, agents.TurnCompleted)
}

// recordTurnMark stores, at the status record's working write rev, where the kind's own source
// stands, so a later poll can tell an idle that closed THIS turn from the previous turn's. Kinds
// without TurnMark (kiro) and a source that cannot be read record nothing: the window applies.
func recordTurnMark(m session.Meta, sid, rev string) {
	t := liveStateTraits(m.Kind)
	if !t.PolledTurnEnd || t.TurnMark == nil || t.EndedAfter == nil {
		return
	}
	if mark, ok := t.TurnMark(m); ok {
		status.RecordTurnMark(sid, rev, mark)
	}
}

// polledIdentityBound caps how long a source that shows idle with no end after the turn's mark
// is believed to be showing the previous turn's. Past it the turn is one whose input produced
// nothing (rejected, swallowed), and the idle is its end, as the window would have taken it:
// holding it longer would leave the persisted "working" for good.
const polledIdentityBound = 2 * time.Minute

// idleEndsThisTurn reports whether an idle just read from m's source is the end of the turn the
// status record started, for both routes that record one. With a turn identity (the mark taken
// at the working write and a source that can say where its newest end lies) the answer is exact
// and immediate: an end after the mark counts at once, however quickly the turn ran, and one
// before it never does. Without one (kiro, no mark because the Agent never saw the start, an
// unreadable or rotated source) it is the settle window below.
func idleEndsThisTurn(m session.Meta, sid string) bool {
	t := liveStateTraits(m.Kind)
	if t.EndedAfter != nil {
		if mark, ok := status.TurnMarkOf(sid); ok {
			if after, known := t.EndedAfter(m, mark); known {
				return after || turnOlderThan(sid, polledIdentityBound)
			}
		}
	}
	return !turnJustStarted(sid)
}

// turnOlderThan reports whether the status record's working write is at least d old.
func turnOlderThan(sid string, d time.Duration) bool {
	st, ok := status.Read(sid)
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339, st.TS)
	return err == nil && polledEndNow().Sub(at) >= d
}

// polledEndSettleDefault is the production value of polledEndSettle. It is a heuristic, not a
// measurement of how long any kind's source lags: a stale idle that outlives it is still taken as
// the new turn's end. It stays short because the same gate delays the completion notification of
// a turn that really ended inside it. Only the fallback now: a kind with a turn identity
// (idleEndsThisTurn) never consults it.
const polledEndSettleDefault = 10 * time.Second

// polledEndSettle is how long after a turn's start (the status record's "working" write) a poll's
// idle is not yet taken as that turn's end, by BOTH routes that record one (the list's stamp and
// DriveState's notification). A hook-less kind's state source can still show the
// PREVIOUS turn's idle for a moment after a new input; stamping it would give the new turn an end
// before it ran, and the real end then finds the turn already stamped (the observation is one per
// status Rev) and keeps the early time, so a watch on the session counts one turn end instead of
// two. A real end inside the window is stamped by a later poll, a few seconds late, which the
// list already tolerates. Measured limit: a stale idle that outlives the window is still stamped.
var polledEndSettle = polledEndSettleDefault

// polledEndNow is the clock of that window, replaceable by tests.
var polledEndNow = time.Now

// turnJustStarted reports whether the status record's working write is younger than the window.
// TS has whole-second resolution, so the window is effectively one second longer at most.
func turnJustStarted(sid string) bool {
	if polledEndSettle <= 0 {
		return false
	}
	st, ok := status.Read(sid)
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339, st.TS)
	return err == nil && polledEndNow().Sub(at) < polledEndSettle
}

// recordPolledTurnEnd is the sessions-list half: stamp when the turn ended, fire nothing.
//
// state is what the kind's WireLive already read, reused so the list does not pay for the same
// source twice. The cheap status-store gate still comes first: with no turn in flight, or with
// this one's end already stamped, there is nothing to learn.
func recordPolledTurnEnd(m session.Meta, state string) {
	sid := session.UUID(m.Dir, m.Name)
	if !status.TurnEndUnrecorded(sid) {
		return
	}
	// Only a PolledTurnEnd kind: WireLive already read the same source DriveState reads. Every
	// other kind has a hook (or a managed driver) that reports its own end of turn, so an idle
	// here is not evidence that a turn ended — for claude it is exactly the "idle we cannot
	// explain" the TurnEnd bit exists to keep out.
	if !liveStateTraits(m.Kind).PolledTurnEnd {
		return
	}
	if state != "idle" || !idleEndsThisTurn(m, sid) {
		return
	}
	status.RecordTurnEnd(sid)
}
