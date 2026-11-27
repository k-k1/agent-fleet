package status

import (
	"testing"
	"time"
)

func armed(t *testing.T, sid string, noReason bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ArmFirstTurn(sid, noReason)
	MarkFirstTurnDelivering(sid, "task")
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

// The first end was missed and a second prompt's turn follows on a record that still says
// working: a different prompt id is a new turn, not a heartbeat.
func TestFirstTurnDifferentPromptWhileWorkingVoids(t *testing.T) {
	const sid = "ft-prompt"
	armed(t, sid, false)
	PersistOpen(sid, "working", "first")
	PersistOpen(sid, "working", "first")
	PersistOpen(sid, "working", "second")
	PersistTurnEndFor(sid, "idle", "", "second")
	if got, ok := ReadFirstTurn(sid); ok {
		t.Fatalf("the second prompt's end settled the first turn: %+v", got)
	}
}

// Deleting the status record (a stop, a heal) with the turn open voids the ledger; a settled one
// is kept.
func TestFirstTurnRemoveVoidsAnOpenLedgerAndKeepsASettledOne(t *testing.T) {
	armed(t, "ft-rm-open", false)
	Persist("ft-rm-open", "working")
	Remove("ft-rm-open")
	Persist("ft-rm-open", "working")
	PersistTurnEnd("ft-rm-open", "idle")
	if got, ok := ReadFirstTurn("ft-rm-open"); ok {
		t.Fatalf("an end after a removal settled the ledger: %+v", got)
	}
	armed(t, "ft-rm-done", false)
	Persist("ft-rm-done", "working")
	PersistTurnEnd("ft-rm-done", "idle")
	Remove("ft-rm-done")
	if _, ok := ReadFirstTurn("ft-rm-done"); !ok {
		t.Fatal("a stop deleted a settled first turn")
	}
}

// A person typing before the delivery began owns that turn: it is not the prompt's.
func TestFirstTurnBeforeDeliveryIsNotThePromptsTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "ft-early"
	ArmFirstTurn(sid, false)
	Persist(sid, "working") // a person's input, the prompt not yet sent
	PersistTurnEnd(sid, "idle")
	MarkFirstTurnDelivering(sid, "task")
	Persist(sid, "working")
	PersistTurnEnd(sid, "idle")
	if got, ok := ReadFirstTurn(sid); ok {
		t.Fatalf("a turn before the delivery settled the ledger: %+v", got)
	}
	// And a start seen before the mark voids it even when nothing follows.
	ArmFirstTurn("ft-early2", false)
	Persist("ft-early2", "working")
	MarkFirstTurnDelivering("ft-early2", "task")
	Persist("ft-early2", "idle")
	Persist("ft-early2", "working")
	PersistTurnEnd("ft-early2", "idle")
	if _, ok := ReadFirstTurn("ft-early2"); ok {
		t.Fatal("a ledger whose turn began before the delivery was kept")
	}
}

func TestArmReplacesALedgerOfAReusedName(t *testing.T) {
	armed(t, "ft-reuse", false)
	Persist("ft-reuse", "working")
	PersistTurnEnd("ft-reuse", "idle")
	if _, ok := ReadFirstTurn("ft-reuse"); !ok {
		t.Fatal("setup")
	}
	ArmFirstTurn("ft-reuse", false)
	if _, ok := ReadFirstTurn("ft-reuse"); ok {
		t.Fatal("a new session of the same name kept the old session's first turn")
	}
	RemoveFirstTurn("ft-reuse")
}

func TestSweepOrphanFirstTurnsKeepsALedgerItCannotLock(t *testing.T) {
	const sid = "ft-sweep-locked"
	t.Setenv("HOME", t.TempDir())
	ArmFirstTurn(sid, false)
	prev := lockWait
	lockWait = 30 * time.Millisecond
	t.Cleanup(func() { lockWait = prev })
	unlock, ok := lockSid(sid)
	if !ok {
		t.Fatal("could not take the lock")
	}
	n := SweepOrphanFirstTurns(map[string]bool{}, time.Minute, time.Now().Add(time.Hour))
	unlock()
	if n != 0 {
		t.Errorf("removed %d while the lock was held", n)
	}
	if _, ok := firstTurns.Read(sid); !ok {
		t.Error("the ledger was removed without the lock")
	}
	if n := SweepOrphanFirstTurns(map[string]bool{}, time.Minute, time.Now().Add(time.Hour)); n != 1 {
		t.Errorf("unlocked sweep removed %d, want 1", n)
	}
}

// A verdict that took time to reach belongs to the record it started from: once a newer working
// record was written, PersistTurnEndIfRev refuses, and the record stays working.
func TestPersistTurnEndIfRevRefusesANewerRecord(t *testing.T) {
	isolate(t)
	sid := "ifrev-sid"
	Persist(sid, "working")
	old, _ := Read(sid)
	if !PersistTurnEndIfRev(sid, "idle", "", "", old.Rev) {
		t.Fatal("the current record was refused")
	}
	Persist(sid, "working") // the next turn
	cur, _ := Read(sid)
	if PersistTurnEndIfRev(sid, "idle", TurnEndReasonFailed, "", old.Rev) {
		t.Fatal("an end bound to an older record was written")
	}
	if now, _ := Read(sid); now.State != "working" || now.Rev != cur.Rev || now.TurnEnd {
		t.Fatalf("the newer record was changed: %+v", now)
	}
}
