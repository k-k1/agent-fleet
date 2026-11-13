package sessionx

// The turn identity (docs/log/89 §89.3 follow-up, #4007): an idle read off a hook-less kind's
// source counts only when its newest end lies after the position the source stood at when the
// status record's working write opened the turn. The settle window is off here
// (polled_settle_test.go), so everything these tests reject is rejected by the identity alone.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

func appendEvents(t *testing.T, m session.Meta, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(copilot.EventsPath(copilot.SessionID(m)), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

// startTurn is what POST /input does: the baseline before delivery, then the working write.
func startTurn(m session.Meta) string {
	return markSessionWorkingFrom(m.Name, captureTurnMark(m.Name))
}

func TestTurnIdentityStaleIdleIsNotThisTurnsEnd(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_stale")
	turnEnded() // turn 1 is over; the source says idle
	startTurn(m)
	if _, ok := status.TurnMarkOf(sid); !ok {
		t.Fatal("control: the working write recorded no mark")
	}
	recordPolledTurnEnd(m, "idle")
	if got := status.ObservedTurnEnd(sid); got != "" {
		t.Fatalf("the previous turn's idle was stamped as the new turn's end: %q", got)
	}
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd {
		t.Fatalf("DriveState settled a turn whose source had not moved: %+v", st)
	}

	// The turn runs and really ends: counted once, at once, with no window to wait out.
	appendEvents(t, m, `{"type":"user.message"}`, `{"type":"assistant.turn_end"}`)
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd {
		t.Fatalf("the real end was not recorded: %+v", st)
	}
}

// A turn that ends before the first poll is still told, even inside what used to be the window.
func TestTurnIdentityFastTurnIsToldWithoutWaiting(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_fast")
	turnEnded()
	polledEndSettle = polledEndSettleDefault
	defer func() { polledEndSettle = 0 }()
	// Production order: baseline, delivery (the CLI runs the whole turn), THEN the working write.
	pre := captureTurnMark(m.Name)
	appendEvents(t, m, `{"type":"user.message"}`, `{"type":"assistant.turn_end"}`)
	markSessionWorkingFrom(m.Name, pre)
	recordPolledTurnEnd(m, "idle")
	if status.ObservedTurnEnd(sid) == "" {
		t.Fatal("a turn that ended before the working write was dropped as stale")
	}
}

// A source that shrank below the mark (rotated or truncated) cannot be compared: fall back to
// the window, not to "always idle" and not to "never".
func TestTurnIdentityRotatedSourceFallsBackToTheWindow(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_rotated")
	turnEnded()
	startTurn(m)
	if err := os.WriteFile(copilot.EventsPath(copilot.SessionID(m)), []byte(`{"type":"assistant.turn_end"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Window off (test default): the idle counts.
	recordPolledTurnEnd(m, "idle")
	if status.ObservedTurnEnd(sid) == "" {
		t.Fatal("a rotated source lost the end for good")
	}
}

func TestTurnIdentityRotatedSourceKeepsTheWindowGuard(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_rotated_win")
	turnEnded()
	startTurn(m)
	os.WriteFile(copilot.EventsPath(copilot.SessionID(m)), []byte(`{"type":"assistant.turn_end"}`+"\n"), 0o600)
	polledEndSettle = polledEndSettleDefault
	defer func() { polledEndSettle = 0 }()
	recordPolledTurnEnd(m, "idle")
	if status.ObservedTurnEnd(sid) != "" {
		t.Fatal("with no usable identity the window must still hold a just-started turn")
	}
}

// No mark (the Agent restarted before it could record one, or a plain status.Persist): window.
func TestTurnIdentityWithoutAMarkUsesTheWindow(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_nomark")
	turnEnded()
	status.Persist(sid, "working")
	polledEndSettle = polledEndSettleDefault
	defer func() { polledEndSettle = 0 }()
	recordPolledTurnEnd(m, "idle")
	if status.ObservedTurnEnd(sid) != "" {
		t.Fatal("window did not hold a just-started turn without a mark")
	}
}

// The mark outlives the Agent (it is on disk) but not the turn: a later status write retires it.
func TestTurnIdentityMarkExpiresWithTheStatusRecord(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_expire")
	turnEnded()
	startTurn(m)
	status.Persist(sid, "working")
	if _, ok := status.TurnMarkOf(sid); ok {
		t.Fatal("a mark taken for an earlier write still applies")
	}
}

// The previous process's shutdown flushes between the baseline and the new input reaching the
// source: an end with no start after the mark is not this turn's, window or not.
func TestTurnIdentityLatePreviousEndIsNotThisTurns(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_late")
	turnEnded()
	pre := captureTurnMark(m.Name)
	appendEvents(t, m, `{"type":"session.shutdown"}`)
	markSessionWorkingFrom(m.Name, pre)
	recordPolledTurnEnd(m, "idle")
	DriveState(m, true, false)
	if status.ObservedTurnEnd(sid) != "" {
		t.Fatal("a late previous end was stamped as the new turn's")
	}
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd {
		t.Fatalf("a late previous end settled the new turn: %+v", st)
	}
	// Control: once the new input shows up, its end counts at once, window or not.
	polledEndSettle = polledEndSettleDefault
	defer func() { polledEndSettle = 0 }()
	appendEvents(t, m, `{"type":"user.message"}`, `{"type":"assistant.turn_end"}`)
	recordPolledTurnEnd(m, "idle")
	if status.ObservedTurnEnd(sid) == "" {
		t.Fatal("the real end after the new input was held back")
	}
}

// A readable source that denies the end is believed however long the turn runs. Recorded shape:
// a huge tool result line (> the 128 KiB tail) pushes the turn's own start out of the tail
// read, which then classifies idle while the tool or model is still running.
func TestTurnIdentityLongRunningTurnIsNeverSynthesizedComplete(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_ident_long")
	turnEnded()
	startTurn(m)
	appendEvents(t, m, `{"type":"user.message"}`, `{"type":"assistant.turn_start"}`,
		`{"type":"tool.execution_complete","data":{"result":{"content":"`+strings.Repeat("x", 140*1024)+`"}}}`)
	if st := copilot.LiveState(m); st != "idle" {
		t.Fatalf("control: the tail read should misread this shape as idle, got %q", st)
	}
	now := time.Now().Add(3 * time.Minute)
	polledEndNow = func() time.Time { return now }
	defer func() { polledEndNow = time.Now }()
	recordPolledTurnEnd(m, "idle")
	DriveState(m, true, false)
	if status.ObservedTurnEnd(sid) != "" {
		t.Fatal("a running turn was stamped as ended by elapsed time")
	}
	if st, _ := status.Read(sid); st.State != "working" || st.TurnEnd {
		t.Fatalf("a running turn was settled by elapsed time: %+v", st)
	}
}

// A slow writer of an older turn must not take the newer turn's mark away.
func TestTurnMarkRecordedByASlowOlderWriterGivesWay(t *testing.T) {
	_, sid, _ := polledTurnEndFixture(t, "copilot_ident_slow")
	revA := status.PersistRev(sid, "working")
	revB := status.PersistRev(sid, "working")
	status.RecordTurnMark(sid, revB, "200")
	status.RecordTurnMark(sid, revA, "100")
	if mark, ok := status.TurnMarkOf(sid); !ok || mark != "200" {
		t.Fatalf("the newer turn's mark was overwritten: %q %v", mark, ok)
	}
}

// kiro's source is a pane scrape with no position: it keeps the window and records no mark.
func TestTurnIdentityKiroHasNone(t *testing.T) {
	tr := liveStateTraits(session.KindKiro)
	if !tr.PolledTurnEnd || tr.TurnMark != nil || tr.EndedAfter != nil {
		t.Fatalf("kiro must stay window-only: %+v", tr)
	}
}
