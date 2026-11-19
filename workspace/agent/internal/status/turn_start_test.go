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
