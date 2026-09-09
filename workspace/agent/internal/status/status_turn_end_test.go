package status

// TurnEndAt is the timestamp behind the sessions list's lastTurnEndAt, and it exists as a
// field of its own so that a POLL can record "the turn ended here" without settling the state
// machine the completion notification is gated on (docs/log/89 §89.3, turn_end_poll.go).
// Everything below is that separation, pinned from inside the package: these invariants are
// invisible from the route tests, where two writes a few milliseconds apart produce the same
// RFC3339 second and a rewrite of the same value cannot be told from no write at all.

import (
	"strings"
	"testing"
	"time"
)

const stampedEarlier = "2020-01-01T00:00:00Z"

// inFlight seeds sid with a turn in flight (what /input's optimistic working leaves), stamped
// or not. It writes the record directly so the stamp is a value the test can recognise.
func inFlight(t *testing.T, sid, stamp string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	write(sid, SessionStatus{State: "working", TS: stampedEarlier, TurnEndAt: stamp})
}

// The core of the split: recording an end leaves State, TurnEnd and TS exactly as they were.
// State is what sessionx.DriveState gates the completion notification on (== "working") and
// TurnEnd + TS are what the docs/log/51 report reconciler judges by, so a record that touched
// any of them would silence or misdate a completion report rather than merely time one.
func TestRecordTurnEndStampsTheTimeAndNothingElse(t *testing.T) {
	const sid = "slot-record"
	inFlight(t, sid, "")

	RecordTurnEnd(sid)

	st, ok := Read(sid)
	if !ok || st.TurnEndAt == "" {
		t.Fatalf("the end of the turn was not stamped: %+v", st)
	}
	if st.State != "working" || st.TurnEnd || st.TS != stampedEarlier {
		t.Fatalf("recording moved the state machine: %+v", st)
	}
}

// A poll must not keep writing. The sessions list runs this for every session in the workspace
// every few seconds, so a stamp per poll is a file write per session per poll — and the value
// would drift forward, which a parent watching lastTurnEndAt reads as another turn finishing.
func TestRecordTurnEndWritesOncePerTurn(t *testing.T) {
	const sid = "slot-once"
	inFlight(t, sid, stampedEarlier)
	before, ok := statusFiles.ModTime(sid)
	if !ok {
		t.Fatal("no status file")
	}
	time.Sleep(10 * time.Millisecond)

	RecordTurnEnd(sid)

	if st, _ := Read(sid); st.TurnEndAt != stampedEarlier {
		t.Errorf("TurnEndAt = %q, want the first observation %q", st.TurnEndAt, stampedEarlier)
	}
	if at, _ := statusFiles.ModTime(sid); !at.Equal(before) {
		t.Errorf("the file was rewritten (%v → %v) for an end already recorded", before, at)
	}
}

// Nothing to end, nothing to record. Without a turn in flight an idle is one nobody can
// explain (a restart, a heal), and a timestamp on it is read as evidence of completion by both
// the report reconciler and a parent polling its child.
func TestRecordTurnEndNeedsATurnInFlight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const idle, unknown = "slot-idle", "slot-unknown"
	write(idle, SessionStatus{State: "idle"})

	RecordTurnEnd(idle)
	RecordTurnEnd(unknown) // nothing recorded for it at all

	if st, _ := Read(idle); st.TurnEndAt != "" {
		t.Errorf("an idle with no turn in flight was stamped: %+v", st)
	}
	if _, ok := Read(unknown); ok {
		t.Error("a session with no status at all had one created")
	}
}

// The first observation of a turn's end wins. Both writers describe the SAME end — the poll
// that saw it first, and the notification route that settles it afterwards — so overwriting
// would move lastTurnEndAt forward with no second turn behind it.
func TestPersistTurnEndKeepsTheFirstObservation(t *testing.T) {
	const sid = "slot-first"
	inFlight(t, sid, stampedEarlier)

	PersistTurnEnd(sid, "idle")

	st, _ := Read(sid)
	if st.TurnEndAt != stampedEarlier {
		t.Errorf("TurnEndAt = %q, want the earlier observation %q", st.TurnEndAt, stampedEarlier)
	}
	if st.State != "idle" || !st.TurnEnd || st.TS == stampedEarlier {
		t.Errorf("the settled end of turn is wrong: %+v", st)
	}
}

// With nothing recorded before it, PersistTurnEnd is the observation, so it stamps its own
// time — the hook and managed routes (which never call RecordTurnEnd) reach lastTurnEndAt only
// through this.
func TestPersistTurnEndStampsWhenNothingObservedItFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "slot-hook"

	PersistTurnEnd(sid, "idle")

	st, _ := Read(sid)
	if st.TurnEndAt == "" || st.TurnEndAt != st.TS {
		t.Fatalf("a hook-route end of turn carries no time: %+v", st)
	}
}

// A new turn clears the stamp with everything else: Persist writes a fresh record, so
// "mid-turn reads empty" and "a restart reads empty" hold for TurnEndAt exactly as they do for
// the TurnEnd bit it was split off from.
func TestPersistClearsTheRecordedEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "slot-next"
	PersistTurnEnd(sid, "idle")

	Persist(sid, "working")

	if st, _ := Read(sid); st.TurnEndAt != "" || st.TurnEnd {
		t.Fatalf("the previous turn's end survived into the next turn: %+v", st)
	}
}

// The store is per-sid files under HOME; the tests above depend on that isolation holding.
func TestStatusStoreStaysUnderTheTestHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if p := statusFiles.Path("slot-x"); !strings.HasPrefix(p, home+"/") {
		t.Fatalf("status path %q escapes the test HOME %q", p, home)
	}
}
