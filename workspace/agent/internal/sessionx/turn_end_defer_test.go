package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Step types as measured on agy (pending_test.go): user, model, tool, background notice.
const (
	agyUser, agyModel, agyTool, agyNotice = 14, 15, 132, 101
)

// interimFixture is a real agy conversation DB whose turn, started by an input at idx 2, ran a
// tool that agy backgrounded and ended at idx 5 (the interim end). toolAlive drives the probe.
func interimFixture(t *testing.T, name string) (m session.Meta, sid string, add func([][2]int, int), toolAlive *bool) {
	t.Helper()
	m, sid, add = agyIdentityFixture(t, name)
	add([][2]int{{0, agyUser}, {1, agyModel}}, 1) // the previous turn
	markSessionWorkingFrom(m.Name, captureTurnMark(m.Name))
	add([][2]int{{2, agyUser}, {3, agyModel}, {4, agyTool}, {5, agyModel}}, 5)
	alive := true
	toolAlive = &alive
	op := backgroundWakeProbe
	backgroundWakeProbe = func(session.Meta) bool { return *toolAlive }
	bgWakeMu.Lock()
	bgWakePeriods = map[string]*bgWakePeriod{}
	bgWakeMu.Unlock()
	t.Cleanup(func() { backgroundWakeProbe = op })
	return m, sid, add, toolAlive
}

// notEnded fails when the status record or the list already counts the turn as over.
func notEnded(t *testing.T, m session.Meta, sid, when string) {
	t.Helper()
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd || lastTurnEndAt(m) != "" {
		t.Fatalf("%s: the interim end was reported: %+v lastTurnEndAt=%q", when, st, lastTurnEndAt(m))
	}
}

func poll(m session.Meta) {
	DriveState(m, true, false)
	recordPolledTurnEnd(m, "idle")
}

// The background work runs, ends, and agy starts the follow-up turn by itself: only that turn's
// end is reported, once.
func TestTurnEndHeld_InterimEndUntilTheSelfStartedTurnEnds(t *testing.T) {
	m, sid, add, toolAlive := interimFixture(t, "agy_hold_self")
	if got := DriveState(m, true, false); got != "idle" {
		t.Fatalf("control: the interim end should read idle, got %q", got)
	}
	poll(m)
	notEnded(t, m, sid, "tool running, not yet seen by the watch")

	now := time.Now()
	backgroundWakeObserve(m, bgWakeLive{state: "idle", busy: true}, now)
	poll(m)
	notEnded(t, m, sid, "tool running")

	*toolAlive = false
	backgroundWakeObserve(m, bgWakeLive{state: "idle"}, now.Add(15*time.Second))
	poll(m)
	notEnded(t, m, sid, "work over, follow-up turn not here yet")
	if busy, _ := withAwaitedTurn(m, false, ""); !busy {
		t.Fatal("the list reads a session awaiting its follow-up turn as finished")
	}

	add([][2]int{{6, agyNotice}, {7, agyModel}}, 7) // agy resumed and ended for real
	poll(m)
	st, _ := status.Read(sid)
	if st.State != "idle" || !st.TurnEnd || lastTurnEndAt(m) == "" {
		t.Fatalf("the real end was not reported: %+v lastTurnEndAt=%q", st, lastTurnEndAt(m))
	}
	if busy, _ := withAwaitedTurn(m, false, ""); busy {
		t.Fatal("still awaiting a turn that has come and gone")
	}
}

// The follow-up turn does not come and the nudge starts it: the interim end is never reported,
// the nudged turn's end is, once.
func TestTurnEndHeld_NudgedTurnIsTheOneReported(t *testing.T) {
	m, sid, add, toolAlive := interimFixture(t, "agy_hold_nudge")
	now := time.Now()
	backgroundWakeObserve(m, bgWakeLive{state: "idle", busy: true}, now)
	*toolAlive = false
	backgroundWakeObserve(m, bgWakeLive{state: "idle"}, now.Add(15*time.Second))
	poll(m)
	notEnded(t, m, sid, "awaiting the nudge")

	// What backgroundWakeSend does through injectSessionPrompt: baseline, input, working.
	pre := captureTurnMark(m.Name)
	markSessionWorkingFrom(m.Name, pre)
	backgroundWakeDone(m.Name)
	poll(m)
	notEnded(t, m, sid, "nudge typed, its turn not in the DB yet")

	add([][2]int{{6, agyUser}, {7, agyModel}}, 7)
	poll(m)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("the nudged turn's end was not reported: %+v", st)
	}
}

// A process that never exits (a dev server the turn left running) holds the end only so long.
func TestTurnEndHeld_RunningToolIsBounded(t *testing.T) {
	m, sid, _, _ := interimFixture(t, "agy_hold_bound")
	start := time.Now()
	backgroundWakeObserve(m, bgWakeLive{state: "idle", busy: true}, start)
	turnEndNow = func() time.Time { return start.Add(turnEndHoldMax - time.Minute) }
	t.Cleanup(func() { turnEndNow = time.Now })
	poll(m)
	notEnded(t, m, sid, "inside the bound")
	turnEndNow = func() time.Time { return start.Add(turnEndHoldMax) }
	poll(m)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("an end held past the bound: %+v", st)
	}
}

// A period that will not nudge (closed: cap, retired, no chance within the give-up bound) stops
// holding, and the end it held is reported.
func TestTurnEndHeld_ClosedPeriodReleasesTheEnd(t *testing.T) {
	m, sid, _, toolAlive := interimFixture(t, "agy_hold_closed")
	now := time.Now()
	backgroundWakeObserve(m, bgWakeLive{state: "idle", busy: true}, now)
	*toolAlive = false
	backgroundWakeObserve(m, bgWakeLive{state: "idle"}, now.Add(15*time.Second))
	poll(m)
	notEnded(t, m, sid, "period open")
	backgroundWakeObserve(m, bgWakeLive{state: "question"}, now.Add(15*time.Second+backgroundWakeGiveUp))
	if _, _, open := backgroundWakePending(m.Name); open {
		t.Fatal("the period outlived its give-up bound")
	}
	poll(m)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("a closed period kept holding the end: %+v", st)
	}
}

// Only the opted-in kind is held: copilot's end is reported with the same probe lit.
func TestTurnEndHeld_OtherKindsAreNotHeld(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_hold")
	op := backgroundWakeProbe
	backgroundWakeProbe = func(session.Meta) bool { return true }
	t.Cleanup(func() { backgroundWakeProbe = op })
	startTurn(m)
	appendEvents(t, m, `{"type":"user.message"}`)
	turnEnded()
	appendEvents(t, m, `{"type":"user.message"}`, `{"type":"assistant.turn_end"}`)
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("a kind that did not opt in was held: %+v", st)
	}
}

// The list alone (nobody opened the session): it must not stamp the interim end either, or
// fleet.session.idle fires for it.
func TestTurnEndHeld_ListDoesNotStampTheInterimEnd(t *testing.T) {
	m, sid, _, toolAlive := interimFixture(t, "agy_hold_list")
	recordPolledTurnEnd(m, "idle")
	if status.ObservedTurnEnd(sid) != "" {
		t.Fatal("the list stamped the interim end while the tool ran")
	}
	*toolAlive = false
	recordPolledTurnEnd(m, "idle")
	if status.ObservedTurnEnd(sid) == "" {
		t.Fatal("control: with no tool and no open period the end is stamped")
	}
}

// The list stamps the end once the bound ran out (recordPolledTurnEnd alone, nobody opened the
// session); the watch's next busy tick must not take that stamp for a new turn and restart the
// bound, or the completion is held for another two hours.
func TestTurnEndHeld_LateStampDoesNotRestartTheBound(t *testing.T) {
	m, sid, _, _ := interimFixture(t, "agy_hold_restamp")
	start := time.Now()
	backgroundWakeObserve(m, bgWakeLive{state: "idle", busy: true}, start)
	turnEndNow = func() time.Time { return start.Add(turnEndHoldMax) }
	t.Cleanup(func() { turnEndNow = time.Now })
	recordPolledTurnEnd(m, "idle")
	stamped := lastTurnEndAt(m)
	if stamped == "" {
		t.Fatal("control: the list stamps the end past the bound")
	}
	backgroundWakeObserve(m, bgWakeLive{state: "idle", busy: true, turnEnd: stamped}, start.Add(turnEndHoldMax+15*time.Second))
	turnEndNow = func() time.Time { return start.Add(turnEndHoldMax + 20*time.Second) }
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("the late stamp restarted the hold: %+v", st)
	}
}

// A retired session's period is gone; its leftover pane and tool must not hold the end for ever,
// nor read as awaiting a turn.
func TestTurnEndHeld_RetiredIsNeverHeld(t *testing.T) {
	m, sid, _, toolAlive := interimFixture(t, "agy_hold_retired")
	backgroundWakeObserve(m, bgWakeLive{state: "idle", busy: true}, time.Now())
	*toolAlive = false
	backgroundWakeObserve(m, bgWakeLive{state: "idle"}, time.Now())
	*toolAlive = true
	m, _ = UpdateSessionMeta(m.Name, func(cur *session.Meta) bool { cur.RetiredAt = "2026-11-20T02:00:00Z"; return true })
	if busy, _ := withAwaitedTurn(m, false, ""); busy {
		t.Fatal("a retired session reads as awaiting a turn")
	}
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("a retired session's end was held: %+v", st)
	}
}
