package sessionx

// Input that never reaches a hook-less kind's CLI (#4047): the stored "working" ends on positive
// evidence (no new user line in the source after the window), with a reason that is not a turn
// end, and a late input is still counted as an ordinary turn.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/copilot"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// pastWindow moves the poll clock beyond copilot's input window.
func pastWindow(t *testing.T) {
	t.Helper()
	now := time.Now().Add(liveStateTraits("copilot").InputWindow + time.Minute)
	polledEndNow = func() time.Time { return now }
	t.Cleanup(func() { polledEndNow = time.Now })
}

func TestInputNeverAppearsEndsWorkingWithoutATurnEnd(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_notdelivered")
	var notified int
	agents.SetStateNotifier(func(_, _, _, _ string) { notified++ })
	t.Cleanup(func() { agents.SetStateNotifier(nil) })
	turnEnded()
	startTurn(m) // the input is "delivered" but the CLI never writes a user line

	// Inside the window nothing changes: a slow write is not a verdict.
	if got := wireSession(m, true); got.InputNotDelivered {
		t.Fatal("flagged inside the window")
	}
	pastWindow(t)
	got := wireSession(m, true)
	if got.State != "idle" || !got.InputNotDelivered || got.LastTurnEndAt != "" {
		t.Fatalf("state=%q notDelivered=%v lastTurnEndAt=%q; want idle, flagged, no turn end", got.State, got.InputNotDelivered, got.LastTurnEndAt)
	}
	if st, _ := status.Read(sid); st.State != "idle" || st.TurnEnd || st.TurnEndAt != "" {
		t.Fatalf("record = %+v, want idle with no turn end", st)
	}
	if st := DriveState(m, true, false); st != "idle" {
		t.Fatalf("DriveState = %q", st)
	}
	if status.ObservedTurnEnd(sid) != "" || notified != 0 {
		t.Fatalf("a turn end was stamped (%q) or notified (%d)", status.ObservedTurnEnd(sid), notified)
	}
	// The idle watch's observation: idle but no turn-end version, so it cannot fire.
	if obs, _ := (watchFleet{}).Observe(m.Name); obs.LastTurnEndAt != "" {
		t.Fatalf("watch saw a turn end version %q", obs.LastTurnEndAt)
	}
}

func TestLateInputAfterTheWindowIsACountedTurn(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_lateinput")
	fired := make(chan [2]string, 8)
	agents.SetStateNotifier(func(_, previous, state, _ string) { fired <- [2]string{previous, state} })
	t.Cleanup(func() { agents.SetStateNotifier(nil) })
	turnEnded()
	startTurn(m)
	pastWindow(t)
	if got := wireSession(m, true); !got.InputNotDelivered {
		t.Fatal("control: not flagged")
	}

	// The input lands late and is still running: the session is working again, not ended.
	appendEvents(t, m, `{"type":"user.message"}`)
	got := wireSession(m, true)
	if got.InputNotDelivered || got.State != "working" {
		t.Fatalf("late input: state=%q notDelivered=%v, want working", got.State, got.InputNotDelivered)
	}
	if st := DriveState(m, true, false); st != "working" {
		t.Fatalf("DriveState = %q, want working", st)
	}

	appendEvents(t, m, `{"type":"assistant.turn_end"}`)
	if st := DriveState(m, true, false); st != "idle" {
		t.Fatalf("DriveState after the end = %q", st)
	}
	select {
	case g := <-fired:
		if g != [2]string{"working", "idle"} {
			t.Fatalf("transition %v", g)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the late turn's completion was never notified")
	}
	if st, _ := status.Read(sid); st.State != "idle" || !st.TurnEnd || st.InputNotDelivered {
		t.Fatalf("record = %+v, want a normal turn end", st)
	}
}

// A turn whose input is in the source is never ended, however long it runs.
func TestRunningTurnIsNeverEndedAsNotDelivered(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_running")
	turnEnded()
	startTurn(m)
	appendEvents(t, m, `{"type":"user.message"}`, `{"type":"assistant.turn_start"}`)
	pastWindow(t)
	for i := 0; i < 3; i++ {
		if got := wireSession(m, true); got.InputNotDelivered || got.State != "working" {
			t.Fatalf("poll %d: state=%q notDelivered=%v", i, got.State, got.InputNotDelivered)
		}
		DriveState(m, true, false)
	}
	if st, _ := status.Read(sid); st.State != "working" || st.InputNotDelivered {
		t.Fatalf("record = %+v", st)
	}
}

// A source that cannot say (rotated below the mark) decides nothing.
func TestUnreadableSourceDoesNotFlagNotDelivered(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_unknown")
	turnEnded()
	startTurn(m)
	if err := truncateEvents(m); err != nil {
		t.Fatal(err)
	}
	pastWindow(t)
	wireSession(m, true)
	if st, _ := status.Read(sid); st.InputNotDelivered {
		t.Fatalf("flagged on an unreadable source: %+v", st)
	}
}

func truncateEvents(m session.Meta) error {
	return os.WriteFile(copilot.EventsPath(copilot.SessionID(m)), []byte("\n"), 0o600)
}

// The source was replaced (a resumed CLI) and regrew past the mark's size: the new file's user
// line lies BEFORE the old offset, which a size compare alone would read as "input not there".
func TestReplacedSourceRegrownPastTheMarkIsNotFlagged(t *testing.T) {
	m, sid, turnEnded := polledTurnEndFixture(t, "copilot_replaced")
	turnEnded()
	startTurn(m)
	big := `{"type":"user.message","data":{"content":"` + strings.Repeat("y", 600) + `"}}`
	if err := os.WriteFile(copilot.EventsPath(copilot.SessionID(m)), []byte(big+"\n"+`{"type":"assistant.turn_start"}`+"\n"+strings.Repeat(`{"type":"tool.x"}`+"\n", 50)), 0o600); err != nil {
		t.Fatal(err)
	}
	pastWindow(t)
	wireSession(m, true)
	DriveState(m, true, false)
	if st, _ := status.Read(sid); st.InputNotDelivered || st.State != "working" {
		t.Fatalf("a running turn on a replaced source was flagged: %+v", st)
	}
}
