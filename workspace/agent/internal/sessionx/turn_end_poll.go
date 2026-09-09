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
// silently stop arriving. status.RecordTurnEnd therefore stamps the timestamp ONLY, leaving the
// state machine untouched, and the two gates below stay deliberately different:
//
//	notify: the persisted state is "working"                (fires once per turn, unchanged)
//	record: … and this turn's end is not stamped yet        (idempotent, consumes nothing)

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
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
	if state != "idle" || status.LiveState(sid) != "working" {
		return
	}
	agents.MarkTurnEnd(sid, agents.TurnCompleted)
}

// recordPolledTurnEnd is the sessions-list half: stamp when the turn ended, fire nothing.
//
// state is what the kind's WireLive already read, reused so the list does not pay for the same
// source twice — except for agy, whose WireLive probes only for a pending interactive prompt
// (it surfaces no working/idle at all), so the end-of-turn reading has to be taken here. That
// extra read is why the cheap status-store gate comes first: with no turn in flight, or with
// this one's end already stamped, there is nothing to learn and the poll costs one small read.
func recordPolledTurnEnd(m session.Meta, state string) {
	sid := session.UUID(m.Dir, m.Name)
	if !status.TurnEndUnrecorded(sid) {
		return
	}
	switch m.Kind {
	case session.KindCopilot, session.KindCursor, session.KindKiro:
		// WireLive already read the same source DriveState reads.
	case session.KindAgy:
		state = agy.LiveState(m)
	default:
		// Every other kind has a hook (or a managed driver) that reports its own end of
		// turn, so an idle here is not evidence that a turn ended — for claude it is
		// exactly the "idle we cannot explain" the TurnEnd bit exists to keep out.
		return
	}
	if state != "idle" {
		return
	}
	status.RecordTurnEnd(sid)
}
