package sessionx

// Holding back an agy turn end that is only an interim one (#4267). agy backgrounds a long
// run_command about 2 s after it starts and ends the turn there; the work goes on, and agy is
// meant to start the real follow-up turn when it finishes (or the background-done nudge,
// background_wake.go, does). Reporting the interim end told parents, watchers and queue runs
// that the session was done while it was not, and the real end then went unreported: the
// interim one had consumed the "working" the notification is gated on.
//
// So for a kind that opts in to the nudge (agents.BackgroundWaker: agy), the turn end is neither
// recorded (LastTurnEndAt, fleet.session.idle) nor notified (completion notice, report, idle
// persisted) while
//   - a tool process from the turn still runs (BackgroundReporter), or
//   - the nudge watch has an open period whose finished turn is still the newest one, i.e. the
//     work ended and the follow-up turn has neither started on its own nor been nudged yet.
//
// The session list says BackgroundBusy for the second case too (wireSession), so the Control
// Plane's queue and idle-stop see one consistent "not finished" instead of an idle row.
//
// Bounds, so a held end is always told eventually: a tool process that never exits (a dev server
// the turn left running) stops holding after turnEndHoldMax, the Control Plane's default
// background_hold_max; an open period closes backgroundWakeGiveUp after the work ended, or as
// soon as a turn follows.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// turnEndHoldMax bounds the hold behind a running tool process, from when the watch first saw it.
const turnEndHoldMax = 2 * time.Hour

// turnEndNow is the hold's clock, replaceable by tests.
var turnEndNow = time.Now

// turnEndHeld reports whether an idle just read off m's source is an interim end to hold back.
// Callers ask only once the cheap gates have passed (a turn in flight, its state idle).
func turnEndHeld(m session.Meta) bool {
	// A retired or archived session's period is dropped by the watch; without this a pane left
	// behind by a failed halt would hold its end with no bound at all.
	if m.Archived || isRetired(m) || !backgroundWakeOptedIn(m.Kind) {
		return false
	}
	mark, since, open := backgroundWakePending(m.Name)
	if backgroundWakeProbe(m) {
		// Not seen by the watch yet (it ticks every 15 s): hold. Seen: hold up to the bound.
		return !open || turnEndNow().Sub(since) < turnEndHoldMax
	}
	return open && nudgeStillAhead(m, mark)
}

// nudgeStillAhead: the open period's finished turn is still the newest, so no turn has followed
// the background work. A mark that cannot be read now, or was never taken, does not hold: the
// nudge cannot fire on it either.
func nudgeStillAhead(m session.Meta, mark string) bool {
	if mark == "" {
		return false
	}
	cur, ok := backgroundWakeMark(m)
	return ok && cur == mark
}

// backgroundWakeAwaiting is the wire's reading of the second case: the work is over, the
// follow-up turn is not here yet. Only asked of a live idle row with no background work.
func backgroundWakeAwaiting(m session.Meta) bool {
	if m.Archived || isRetired(m) || !backgroundWakeOptedIn(m.Kind) {
		return false
	}
	mark, _, open := backgroundWakePending(m.Name)
	return open && nudgeStillAhead(m, mark)
}

// bgReasonProcess is the wire's "process" background reason (claude.BGReasonProcess).
const bgReasonProcess = "process"

// withAwaitedTurn is a live idle row's background pair with the awaited follow-up turn folded
// in: the work is over but the session is not finished, which the list and the mirror header
// both say as background work.
func withAwaitedTurn(m session.Meta, busy bool, reason string) (bool, string) {
	if busy || !backgroundWakeAwaiting(m) {
		return busy, reason
	}
	return true, bgReasonProcess
}
