package status

import (
	"testing"
	"time"
)

func armed(t *testing.T, sid string, noReason bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ArmFirstTurn(sid, noReason)
}

func TestFirstTurnSettlesOnceWhateverFollows(t *testing.T) {
	const sid = "ft-once"
	armed(t, sid, false)
	if _, ok := ReadFirstTurn(sid); ok {
		t.Fatal("a first turn that has not ended was reported")
	}
	Persist(sid, "working")
	Persist(sid, "working") // a heartbeat is not a second start
	PersistOpen(sid, "working", "p1")
	time.Sleep(20 * time.Millisecond)
	PersistTurnEndReason(sid, "idle", TurnEndReasonFailed)
	got, ok := ReadFirstTurn(sid)
	if !ok || got.Outcome != TurnEndReasonFailed || got.EndedAt == "" {
		t.Fatalf("first turn = %+v ok=%v", got, ok)
	}

	// A second turn, a stop (which deletes the record) and a third turn change nothing.
	Persist(sid, "working")
	PersistTurnEnd(sid, "idle")
	Remove(sid)
	Persist(sid, "working")
	PersistTurnEndReason(sid, "idle", TurnEndReasonAborted)
	if again, _ := ReadFirstTurn(sid); again != got {
		t.Fatalf("a later turn changed the first one: %+v -> %+v", got, again)
	}
}

func TestFirstTurnQuestionInsideTheTurnIsNotASecondStart(t *testing.T) {
	const sid = "ft-question"
	armed(t, sid, false)
	Persist(sid, "working")
	Persist(sid, "question")
	Persist(sid, "working") // the answer
	Persist(sid, "permission")
	Persist(sid, "working")
	PersistTurnEnd(sid, "idle")
	got, ok := ReadFirstTurn(sid)
	if !ok || got.Outcome != TurnOutcomeClean {
		t.Fatalf("first turn = %+v ok=%v", got, ok)
	}
}

// The first end was missed (a stop deleted the record before anything saw it), so the end that
// comes next belongs to a later turn and must not be put on the ledger.
func TestFirstTurnMissedEndLeavesItAbsentForGood(t *testing.T) {
	const sid = "ft-missed"
	armed(t, sid, false)
	Persist(sid, "working")
	Remove(sid) // stopped mid-turn
	Persist(sid, "working")
	PersistTurnEnd(sid, "idle")
	if got, ok := ReadFirstTurn(sid); ok {
		t.Fatalf("a later turn's end was taken for the first: %+v", got)
	}
	Persist(sid, "working")
	PersistTurnEnd(sid, "idle")
	if got, ok := ReadFirstTurn(sid); ok {
		t.Fatalf("still absent expected, got %+v", got)
	}
}

func TestFirstTurnEndWithoutAStartIsNotTheFirstTurn(t *testing.T) {
	const sid = "ft-nostart"
	armed(t, sid, false)
	PersistTurnEnd(sid, "idle")
	if got, ok := ReadFirstTurn(sid); ok {
		t.Fatalf("an end with no start was taken for the first turn: %+v", got)
	}
}

func TestFirstTurnWithoutALedgerRecordsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Persist("ft-none", "working")
	PersistTurnEnd("ft-none", "idle")
	if _, ok := ReadFirstTurn("ft-none"); ok {
		t.Fatal("a session that is not a delivery's has a first turn")
	}
}

// A kind without hooks records no reason: the outcome is empty, the times are set, and whichever
// of the two ends is seen first settles it (the list's observation, or DriveState's settle).
func TestFirstTurnOfAKindWithoutReasonsKeepsTheOutcomeEmpty(t *testing.T) {
	for _, observedFirst := range []bool{true, false} {
		sid := "ft-noreason"
		if observedFirst {
			sid += "-obs"
		}
		armed(t, sid, true)
		Persist(sid, "working")
		if observedFirst {
			RecordTurnEnd(sid)
		}
		PersistTurnEndReason(sid, "idle", TurnEndReasonFailed)
		got, ok := ReadFirstTurn(sid)
		if !ok || got.Outcome != "" || got.EndedAt == "" {
			t.Fatalf("observedFirst=%v: %+v ok=%v", observedFirst, got, ok)
		}
	}
}

func TestFirstTurnReopenedInputIsTheSameTurn(t *testing.T) {
	const sid = "ft-reopen"
	armed(t, sid, true)
	rev := PersistRev(sid, "working")
	PersistInputNotDelivered(sid, rev, "m")
	st, _ := Read(sid)
	ReopenDelivered(sid, st.Rev, "m")
	RecordTurnEnd(sid)
	if _, ok := ReadFirstTurn(sid); !ok {
		t.Fatal("the reopened input voided the first turn")
	}
}
