package sessionx

// The source side of moving a session to another agent kind (ADR 0115 decision 11): the
// lifecycle gate every input path holds, and the state transitions a move makes on its source.
// The gate itself is internal/lifegate; this file is where sessionx meets it.
//
// The transitions are begin (check the state under the exclusive gate, write moving), rollback
// (clear moving, repair a booking the move spent) and commit (moving becomes movedTo, the
// usage-limit watch is settled). Stopping the source and taking its spooled peer messages
// belong to the move itself, which owns the order of those steps.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/lifegate"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// errCodeSessionMoved refuses input to a session that is moving or was moved. The body names
// the target, so a peer sender learns where to send instead.
const errCodeSessionMoved = "session_moved"

// sessionMovedWire is the session_moved refusal: httpx.WriteErr's shape plus the target.
type sessionMovedWire struct {
	Error sessionMovedErr `json:"error"`
}

type sessionMovedErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Target  string `json:"target"`
}

// enterSessionGate takes name's lease for an HTTP input path and answers a refusal itself. A
// lease the request's context carries (an in-process delivery that holds one) is borrowed.
func enterSessionGate(w http.ResponseWriter, r *http.Request, name string) (*lifegate.Lease, bool) {
	l, err := lifegate.Enter(r.Context(), name)
	if err != nil {
		writeGateErr(w, err)
		return nil, false
	}
	return l, true
}

// writeGateErr answers a gate refusal, or a step called without the lease it requires.
func writeGateErr(w http.ResponseWriter, err error) {
	var moved *lifegate.MovedError
	var held *lifegate.HeldError
	switch {
	case errors.As(err, &held):
		httpx.WriteErr(w, http.StatusConflict, routes.CodeSwitchBusy, held.Error())
	case errors.As(err, &moved):
		httpx.WriteJSON(w, http.StatusConflict, sessionMovedWire{Error: sessionMovedErr{
			Code: errCodeSessionMoved, Message: moved.Error(), Target: moved.Target,
		}})
	case errors.Is(err, lifegate.ErrNotHeld):
		httpx.WriteErr(w, http.StatusInternalServerError, "session_gate", err.Error())
	default: // the caller went away while a move held the gate
		httpx.WriteErr(w, http.StatusServiceUnavailable, "session_gate", err.Error())
	}
}

// noteBookingRefusedDuringMove handles a scheduled send the gate refused because a move closed
// input, when it was the session's own usage-limit booking: that once-schedule is spent, and the
// watch never replaces an id the episode still holds. While the move runs, the refusal is
// recorded for its rollback to repair; if the move has rolled back by the time this runs (the
// request stalled between its refusal and here), nobody else will repair it, so it is repaired
// here. A committed move cancelled the booking and closed the episode, and needs nothing.
func noteBookingRefusedDuringMove(name, source, scheduleID string, now time.Time) {
	if scheduleID == "" || injectionSource(source) != TurnSourceSchedule {
		return
	}
	if beforeNoteRefusal != nil {
		beforeNoteRefusal()
	}
	episodeMu := rateLimitEpisodeLock(name)
	episodeMu.Lock()
	defer episodeMu.Unlock()
	st, ok := RateLimitStates.Read(name)
	if !ok || st.ScheduleID != scheduleID {
		return
	}
	// Under the meta lock, so a rewrite by an ordinary writer is never read half-done. A meta
	// that still cannot be read is taken as moving: the gate did refuse this booking, so it is
	// spent either way, and if the move has in fact rolled back, the sweep repairs the record
	// (rateLimitTickSession) — dropping it would leave the dead id in place for good.
	var m session.Meta
	ok = false
	WithSessionMetaLock(func() { m, ok = session.ReadMeta(name) })
	switch {
	case ok && m.MovedTo != "":
		return
	case !ok || m.Moving != "":
		if st.FiredDuringMove != scheduleID {
			st.FiredDuringMove = scheduleID
			_ = RateLimitStates.Write(name, st)
			log.Printf("move: %s: its usage-limit resume %s fired while its input was closed", name, scheduleID)
		}
	default:
		log.Printf("move: %s: its usage-limit resume %s was refused by a move that has rolled back since", name, scheduleID)
		rebookSpentRateLimitBooking(m, st, now)
	}
}

// Test seams: beforeNoteRefusal runs between a refusal and its record, afterMoveRepair inside a
// rollback between the repair and the reopening.
var beforeNoteRefusal, afterMoveRepair func()

// moveSourceState is the source's state and its pending interaction ("" for none), as the move
// judges it. A variable so tests need neither tmux nor a runtime.
var moveSourceState = func(m session.Meta) (state, blocker string) {
	return DriveState(m, SessionAlive(m), false), promptBlocker(m.Name)
}

// moveSourceInFlight reports that the source's Managed handle has a turn in flight that its state
// may not show yet. A pump releases its lease once the runtime has the input, and the runtime's
// own record (copilot's events.jsonl, opencode's store, muse's turn/started) can lag that; in the
// gap DriveState still reads the previous turn's idle while the queue no longer holds the entry,
// so a move would leave it out of the handover and the source would still run it.
var moveSourceInFlight = func(m session.Meta) bool {
	return m.DriverKind() == session.DriverManaged && managedInFlight(m)
}

// movableSourceState lists the states a move may start from. idle, limited and spendLimit are
// ADR 0115's; stopped is added because a source parked at its limit for days is usually stopped
// by the idle reaper by then, and a stopped session can start nothing either (a stopped Managed
// cursor is the exception, refused by beginSessionMove: errMoveCursorStopped). Everything else
// is refused: a working source is mid-turn, and a pending question, plan or permission would be
// silently abandoned.
func movableSourceState(st string) bool {
	switch st {
	case "idle", "stopped", agents.StateLimited, agents.StateSpendLimit:
		return true
	}
	return false
}

// moveStateError refuses a move from a state it may not start in.
type moveStateError struct {
	Session string
	State   string
}

func (e *moveStateError) Error() string {
	return fmt.Sprintf("session %s is %s; a move needs it idle, stopped or at its usage limit", e.Session, e.State)
}

var errMoveSourceGone = errors.New("the session to move does not exist")

// errMoveCursorStopped refuses a stopped Managed cursor source, the one exception to moving a
// stopped session: its conversation lived only in the runtime handle, which is gone, so there
// is nothing to write the handover from (ADR 0115, "What each kind's transcript yields").
var errMoveCursorStopped = errors.New("a stopped Managed cursor session has no conversation left to move; " +
	"its history lived only in the running agent")

// sourceMove is one move of a source, from begin until commit or rollback.
type sourceMove struct {
	source string
	target string
}

// beginSessionMove closes source's input for a move to target (ADR 0115 decision 11.2). It
// waits under ctx for the inputs already admitted to commit, checks the state, and writes
// moving; from then on every gated path refuses with session_moved. It must not be called while
// holding a lease on source.
func beginSessionMove(ctx context.Context, source, target string) (*sourceMove, error) {
	if !session.ValidName(target) || target == source {
		return nil, fmt.Errorf("invalid move target %q", target)
	}
	x, err := lifegate.Lock(ctx, source)
	if err != nil {
		return nil, err
	}
	defer x.Unlock()
	m, ok := session.ReadMeta(source)
	if !ok {
		return nil, errMoveSourceGone
	}
	if err := checkMoveSource(m); err != nil {
		return nil, err
	}
	// Checked and atomic: after a restart the meta is the only record that input is closed, so the
	// in-memory mirror is closed, and success returned, only once moving is on disk. The same holds
	// for every transition below.
	if _, ok, err := updateSessionMetaChecked(source, func(cur *session.Meta) bool {
		if cur.Moving != "" || cur.MovedTo != "" {
			return false
		}
		cur.Moving = target
		return true
	}); err != nil {
		return nil, fmt.Errorf("recording the move of %s: %w", source, err)
	} else if !ok {
		return nil, errMoveSourceGone
	}
	x.SetClosed(target, false)
	log.Printf("move: %s: input closed for the move to %s", source, target)
	return &sourceMove{source: source, target: target}, nil
}

// checkMoveSource is the state check of decision 11.2: nil when a move may start from m. The
// move runs it under the exclusive gate; the preview runs it without, as an answer that can be
// stale by the time the user confirms.
func checkMoveSource(m session.Meta) error {
	if m.MovedTo != "" {
		return &lifegate.MovedError{Session: m.Name, Target: m.MovedTo, Done: true}
	}
	if m.Moving != "" {
		return &lifegate.MovedError{Session: m.Name, Target: m.Moving}
	}
	if m.Kind == session.KindCursor && m.DriverKind() == session.DriverManaged && !ManagedAlive(m) {
		return errMoveCursorStopped
	}
	state, blocker := moveSourceState(m)
	if blocker != "" {
		return &moveStateError{Session: m.Name, State: blocker}
	}
	if !movableSourceState(state) {
		return &moveStateError{Session: m.Name, State: state}
	}
	if moveSourceInFlight(m) {
		return &moveStateError{Session: m.Name, State: "working"}
	}
	return nil
}

// rollback opens the source's input again after a move that created no target (ADR 0115
// decision 11.4). The usage-limit booking is repaired first, while input is still closed, so a
// sweep that runs as soon as it opens finds the episode already settled and books nothing twice.
func (mv *sourceMove) rollback(now time.Time) error {
	// The episode lock (rateLimitEpisodeLock) makes the repair, the reopening and a refusal's record
	// one step against each other: a refusal sees the move either still running or fully settled.
	episodeMu := rateLimitEpisodeLock(mv.source)
	episodeMu.Lock()
	defer episodeMu.Unlock()
	repairRateLimitBookingAfterMove(mv.source, now)
	if afterMoveRepair != nil {
		afterMoveRepair()
	}
	if _, ok, err := updateSessionMetaChecked(mv.source, func(cur *session.Meta) bool {
		if cur.Moving != mv.target {
			return false
		}
		cur.Moving = ""
		return true
	}); err != nil {
		return fmt.Errorf("reopening %s: %w", mv.source, err) // still closed, on disk and in memory
	} else if !ok {
		if m, exists := session.ReadMeta(mv.source); exists && m.Moving != "" {
			return fmt.Errorf("session %s is moving to %s, not %s", mv.source, m.Moving, mv.target)
		}
	}
	lifegate.Reopen(mv.source)
	log.Printf("move: %s: rolled back the move to %s; input is open again", mv.source, mv.target)
	return nil
}

// commit records that the target exists (ADR 0115 decision 11.5): moving becomes movedTo, which
// keeps the source closed until its user reopens it, and the usage-limit watch is settled.
//
// The booking's id is recorded for cancellation before anything else changes, and the episode
// lets go of it only after that: an Agent that stops at any point leaves the id in one store
// or the other. A record that cannot be written fails the commit with nothing changed.
func (mv *sourceMove) commit() error {
	episodeMu := rateLimitEpisodeLock(mv.source)
	episodeMu.Lock()
	m, ok := session.ReadMeta(mv.source)
	if !ok || m.Moving != mv.target {
		episodeMu.Unlock()
		return fmt.Errorf("session %s is not moving to %s", mv.source, mv.target)
	}
	st, hasEpisode := RateLimitStates.Read(mv.source)
	if hasEpisode && st.ScheduleID != "" {
		if err := recordRateLimitCancel(mv.source, st.ScheduleID); err != nil {
			episodeMu.Unlock()
			return fmt.Errorf("recording the cancellation of the usage-limit resume: %w", err)
		}
	}
	if _, ok, err := updateSessionMetaChecked(mv.source, func(cur *session.Meta) bool {
		if cur.Moving != mv.target {
			return false
		}
		cur.Moving, cur.MovedTo = "", mv.target
		return true
	}); err != nil || !ok {
		// The source stays moving with its episode; the recorded id is treated as spent by a
		// rollback (repairRateLimitBookingAfterMove), so a failed commit loses no resume.
		episodeMu.Unlock()
		if err != nil {
			return fmt.Errorf("recording the move of %s: %w", mv.source, err)
		}
		return fmt.Errorf("session %s is not moving to %s", mv.source, mv.target)
	}
	lifegate.SetClosed(mv.source, mv.target, true)
	lifegate.DropReopen(mv.source)
	if hasEpisode {
		RateLimitStates.Remove(mv.source) // nothing shows the source as waiting for a reset
	}
	episodeMu.Unlock()
	if hasEpisode && st.ScheduleID != "" {
		// Outside the lock: the CP may take its timeout. A failure stays recorded for the sweep,
		// and a booking that fires before then is refused by the gate.
		finishRateLimitCancel(st.ScheduleID)
	}
	log.Printf("move: %s: moved to %s", mv.source, mv.target)
	return nil
}

// reopenMovedSession is the user's explicit "resume here anyway" (ADR 0115 decision 11.6): the
// only way a moved source takes input again.
func reopenMovedSession(name string) error {
	if _, ok, err := updateSessionMetaChecked(name, func(cur *session.Meta) bool {
		if cur.MovedTo == "" {
			return false
		}
		cur.MovedTo = ""
		return true
	}); err != nil {
		return fmt.Errorf("reopening %s: %w", name, err)
	} else if !ok {
		return fmt.Errorf("session %s was not moved", name)
	}
	lifegate.Reopen(name)
	return nil
}

// repairRateLimitBookingAfterMove books the resume again when the source's booking is spent:
// it fired during the move and the gate refused it, or it was recorded for cancellation (a
// commit that recorded it and then stopped or failed; the sweep may have cancelled it since).
// The watch would never replace it: scheduleRateLimitResume declines while the episode holds an
// id. Called with the episode lock held.
func repairRateLimitBookingAfterMove(name string, now time.Time) {
	st, ok := RateLimitStates.Read(name)
	if !ok || st.ScheduleID == "" {
		return
	}
	if st.FiredDuringMove != st.ScheduleID && !rateLimitCancelRecorded(st.ScheduleID) {
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		return
	}
	rebookSpentRateLimitBooking(m, st, now)
}

// rebookSpentRateLimitBooking cancels the spent row (or it stays in the Console's list) and books
// the resume again, at the original instant or as soon as the scheduler allows when that has
// passed. Called with the episode lock held.
func rebookSpentRateLimitBooking(m session.Meta, st rateLimitState, now time.Time) {
	name := m.Name
	_ = cancelRateLimitSchedule(name, st.ScheduleID)
	st.ScheduleID, st.FiredDuringMove = "", ""
	if !uiprefs.RateLimitAutoResume() {
		_ = RateLimitStates.Write(name, st)
		return
	}
	at := now.Add(rateLimitResumeLead)
	if t, err := time.Parse(time.RFC3339, st.ResumeAt); err == nil && t.After(at) {
		at = t
	}
	st.ResumeAt = at.Format(time.RFC3339)
	st.ScheduleTries = 1 // this attempt; a failure leaves the sweep its usual retries
	id, err := putRateLimitSchedule(m, at)
	if err != nil {
		// Left without an id, so the sweep's follow-up books it as it does any failed booking.
		log.Printf("move: %s: could not book the usage-limit resume again: %v", name, err)
	} else {
		st.ScheduleID = id
		log.Printf("move: %s: booked the usage-limit resume again at %s (schedule=%s)", name, st.ResumeAt, id)
	}
	_ = RateLimitStates.Write(name, st)
}
