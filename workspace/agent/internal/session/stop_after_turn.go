package session

// The "stop after this turn" arm (docs/log/85), as a predicate over Meta.
//
// It lives here, next to the field, because two packages have to agree on it and they cannot
// see each other: sessionx arms/cancels it and executes the halt, chatx's report reconciler
// decides when the turn has ended. A second copy of "is this arm still live" would be the
// same defect as busy state written in two places (docs/log/75) — the screen showing an arm
// the reconciler no longer honours, or the reverse.

import "time"

// StopArmMaxAge is how long an arm survives unconsumed.
//
// An arm is a statement about the turn that is running now, so it should be consumed within
// minutes. It can still be orphaned — the turn ends while the Agent is down, the session is
// resumed days later — and a stop that fires then folds away work nobody asked to stop. The
// window is generous enough to cover a long unattended run and short enough that the arm
// cannot outlive the user's memory of setting it.
const StopArmMaxAge = 6 * time.Hour

// StopArmedAt reports when the session was armed to stop at the end of its turn, and whether
// that arm is still live at now. An unparseable value reads as not armed: the instant is the
// lower bound the completion evidence is cut by, and an arm with no usable bound could be
// closed by an end-of-turn marker written before it.
func StopArmedAt(m Meta, now time.Time) (time.Time, bool) {
	if m.StopAfterTurnAt == "" {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, m.StopAfterTurnAt)
	if err != nil {
		return time.Time{}, false
	}
	if now.Sub(at) >= StopArmMaxAge {
		return at, false
	}
	return at, true
}
