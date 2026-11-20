package status

import (
	"testing"
	"time"
)

// A queue run's hand-back is dropped when a turn of its tree started after the call, so the
// latest start must move on a new turn only, and must survive the stop that deletes the record.
func TestLastTurnStartMovesOnANewTurnOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "ts-turns"
	if got := LastTurnStart(sid); got != "" {
		t.Fatalf("a session with no turn has a start: %q", got)
	}
	Persist(sid, "working")
	first := LastTurnStart(sid)
	if first == "" {
		t.Fatal("the first working write recorded no start")
	}
	time.Sleep(5 * time.Millisecond)
	Persist(sid, "working") // a heartbeat
	Persist(sid, "question")
	Persist(sid, "working") // the answer, the same turn
	if got := LastTurnStart(sid); got != first {
		t.Fatalf("the same turn moved the start: %q -> %q", first, got)
	}
	PersistTurnEnd(sid, "idle")
	Remove(sid) // a stop deletes the status record
	if got := LastTurnStart(sid); got != first {
		t.Fatalf("a stop lost the start: %q", got)
	}
	time.Sleep(5 * time.Millisecond)
	Persist(sid, "working") // the next turn, after a resume
	second := LastTurnStart(sid)
	if second == first || second < first {
		t.Fatalf("a new turn did not move the start: %q -> %q", first, second)
	}
}

func TestSweepOrphanTurnStartsKeepsLiveAndYoung(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Persist("ts-live", "working")
	Persist("ts-orphan", "working")
	if n := SweepOrphanTurnStarts(map[string]bool{"ts-live": true}, time.Hour, time.Now()); n != 0 {
		t.Fatalf("a young orphan was swept: %d", n)
	}
	if n := SweepOrphanTurnStarts(map[string]bool{"ts-live": true}, time.Hour, time.Now().Add(2*time.Hour)); n != 1 {
		t.Fatalf("swept %d, want the old orphan only", n)
	}
	if LastTurnStart("ts-live") == "" || LastTurnStart("ts-orphan") != "" {
		t.Fatal("the sweep removed the wrong start")
	}
}

// Review round 1: a working write under another prompt id is a new turn even when the end of the
// turn before was never written, and a driver's second mark of the same input is not.
func TestLastTurnStartPromptChangeAndRepeatedMark(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "ts-prompt"
	PersistOpen(sid, "working", "P1")
	first := LastTurnStart(sid)
	time.Sleep(5 * time.Millisecond)
	PersistOpen(sid, "working", "P1") // a heartbeat of the same prompt
	if LastTurnStart(sid) != first {
		t.Fatal("a heartbeat moved the start")
	}
	PersistOpen(sid, "working", "P2") // the Stop of P1 was missed
	second := LastTurnStart(sid)
	if second == first {
		t.Fatal("a new prompt id did not move the start")
	}
	time.Sleep(5 * time.Millisecond)
	PersistTurnEnd(sid, "idle")
	PersistTurnStart(sid, "input-1")
	third := LastTurnStart(sid)
	if third == second {
		t.Fatal("a driver's start did not move the start")
	}
	time.Sleep(5 * time.Millisecond)
	PersistTurnStart(sid, "input-1") // the same input marked again on the CLI's own start
	if LastTurnStart(sid) != third {
		t.Fatal("a second mark of the same input moved the start")
	}
	RemoveTurnStart(sid)
	if LastTurnStart(sid) != "" {
		t.Fatal("not removed")
	}
}
