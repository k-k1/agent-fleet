package status

// When a turn ended is recorded in two places on purpose: the status record's TurnEndAt for a
// turn something SETTLED, and the observedEnds store for one a POLL merely saw (docs/log/89
// §89.3, §89.9, §89.10). These tests pin that separation from inside the package — the route
// tests cannot see it, because two writes a few milliseconds apart produce the same RFC3339
// second and a rewrite of the same value cannot be told from no write at all.

import (
	"strings"
	"sync"
	"testing"
	"time"
)

const stampedEarlier = "2020-01-01T00:00:00Z"

// inFlight seeds sid with a turn in flight (what /input's optimistic working leaves) and
// optionally an end a poll already observed. It goes through the real Persist so the record
// carries a Rev — nothing can be observed against a record that has no identity.
func inFlight(t *testing.T, sid string, observed bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	Persist(sid, "working")
	if observed {
		st, _ := Read(sid)
		if err := observedEnds.Write(sid, observedEnd{Rev: st.Rev, At: stampedEarlier}); err != nil {
			t.Fatal(err)
		}
	}
}

// 🔥 The regression this store exists for. The sessions list and the notification route reach
// the end of the SAME turn, and on the hook route the second one is a different process
// (`workspace-agent session-status`), so no in-process lock could help. fstore.Write is a plain
// os.WriteFile, so the only safe write is a blind write of a complete record: recording the
// observation must never be a read-modify-write of the status record. It was one, and it
// destroyed the settle in 143 of 300 runs (measured) — state back to working, TurnEnd gone,
// which stops the Console badge, blocks the docs/log/51 reconciler on marker-working, and lets
// the next poll fire MarkTurnEnd a second time for the same turn.
func TestRecordingAnEndNeverDestroysTheSettle(t *testing.T) {
	const rounds = 300
	lost := 0
	for i := 0; i < rounds; i++ {
		t.Setenv("HOME", t.TempDir())
		const sid = "slot-race"
		Persist(sid, "working")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); RecordTurnEnd(sid) }()          // the sessions list
		go func() { defer wg.Done(); PersistTurnEnd(sid, "idle") }() // the notification route
		wg.Wait()

		if st, _ := Read(sid); st.State != "idle" || !st.TurnEnd {
			lost++
		}
	}
	if lost > 0 {
		t.Fatalf("the settled end of turn was lost %d/%d times to a concurrent recording", lost, rounds)
	}
}

// The core of the split: recording an end leaves the status record untouched. State is what
// sessionx.DriveState gates the completion notification on (== "working"), and TurnEnd + TS are
// what the docs/log/51 report reconciler judges by, so a record that touched any of them would
// silence or misdate a completion report rather than merely time one.
func TestRecordTurnEndLeavesTheStatusRecordAlone(t *testing.T) {
	const sid = "slot-record"
	inFlight(t, sid, false)

	RecordTurnEnd(sid)

	if got := ObservedTurnEnd(sid); got == "" {
		t.Fatal("the end of the turn was not recorded")
	}
	before, _ := Read(sid)
	RecordTurnEnd(sid) // again: whatever it does, it must not be to the status record
	st, _ := Read(sid)
	if st != before {
		t.Fatalf("recording moved the status record: %+v → %+v", before, st)
	}
	if st.State != "working" || st.TurnEnd || st.TurnEndAt != "" {
		t.Fatalf("recording moved the state machine: %+v", st)
	}
}

// A poll must not keep writing. The sessions list runs this for every session in the workspace
// every few seconds, so a stamp per poll is a file write per session per poll — and the value
// would drift forward, which a parent watching lastTurnEndAt reads as another turn finishing.
func TestRecordTurnEndWritesOncePerTurn(t *testing.T) {
	const sid = "slot-once"
	inFlight(t, sid, true)
	before, ok := observedEnds.ModTime(sid)
	if !ok {
		t.Fatal("no observation file")
	}
	time.Sleep(10 * time.Millisecond)

	RecordTurnEnd(sid)

	if got := ObservedTurnEnd(sid); got != stampedEarlier {
		t.Errorf("observation = %q, want the first one %q", got, stampedEarlier)
	}
	// The value above is the real assertion (a rewrite would carry now, not the fixture's
	// time). This one adds the case a rewrite with the SAME value would slip past, and it is
	// best-effort by nature: where mtime is coarse it simply cannot tell, so it is lenient and
	// never falsely red — which is why the production rule does not decide anything this way
	// (ObservedTurnEnd).
	if at, _ := observedEnds.ModTime(sid); !at.Equal(before) {
		t.Errorf("the file was rewritten (%v → %v) for an end already recorded", before, at)
	}
}

// Nothing to end, nothing to record. Without a turn in flight an idle is one nobody can
// explain (a restart, a heal), and a timestamp on it is read as evidence of completion by both
// the report reconciler and a parent polling its child.
func TestRecordTurnEndNeedsATurnInFlight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const idle, unknown = "slot-idle", "slot-unknown"
	Persist(idle, "idle")

	RecordTurnEnd(idle)
	RecordTurnEnd(unknown) // nothing recorded for it at all

	if got := ObservedTurnEnd(idle); got != "" {
		t.Errorf("an idle with no turn in flight was recorded as an end: %q", got)
	}
	if got := ObservedTurnEnd(unknown); got != "" {
		t.Errorf("a session with no status at all was recorded as an end: %q", got)
	}
}

// An observation belongs to the turn it was taken during, and nothing clears the store. What
// makes a stale one inert is that ANY later write to the status record mints a new Rev. Without
// that, a parent would read the previous turn's end as this one's.
//
// 🔥 Back to back, with no sleep and no distinguishable content: the next turn's record here
// says the same thing as the previous one (State working) and lands in the same RFC3339 second
// and, on a coarse filesystem, the same mtime tick. Retiring the observation may therefore not
// depend on ANY of those — the rule that did (whichever file is newer) shipped, and the
// listing's first poll after a turn ended silently stopped recording wherever mtime resolution
// was coarser than the gap between the two writes (a CI failure; reproduced by truncating both
// mtimes to the second).
func TestAnObservationIsRetiredByIdentityNotByTiming(t *testing.T) {
	const sid = "slot-stale"
	inFlight(t, sid, true)
	if ObservedTurnEnd(sid) == "" {
		t.Fatal("the fixture's observation does not count")
	}

	Persist(sid, "working") // the next turn starts, in the same instant

	if got := ObservedTurnEnd(sid); got != "" {
		t.Fatalf("the previous turn's end survived into the next turn: %q", got)
	}
}

// The other half of the same rule, and the one the CI failure was actually about: an
// observation taken in the SAME instant as the record it belongs to has to COUNT. Every real
// one is — the poll reads the record and records the end microseconds later.
func TestAnObservationCountsInTheInstantItWasTaken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "slot-instant"
	Persist(sid, "working")

	RecordTurnEnd(sid) // no sleep anywhere: same second, plausibly the same mtime tick

	if got := ObservedTurnEnd(sid); got == "" {
		t.Fatal("an observation taken in the same instant as its record was discarded; the end of a turn is then reported a poll late, forever, wherever mtime is coarse")
	}
}

// Rev is the identity, so a record without one cannot be observed against: an observation
// stored with an empty Rev would match every other record that has none. Records written
// before Rev existed are exactly that case, and they are on disk during an upgrade.
func TestARecordWithNoIdentityIsNeverObserved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "slot-legacy"
	write(sid, SessionStatus{State: "working", TS: stampedEarlier}) // pre-Rev on-disk shape

	RecordTurnEnd(sid)

	if got := ObservedTurnEnd(sid); got != "" {
		t.Fatalf("an observation was pinned to a record with no identity: %q", got)
	}
	// And nothing was written. An observation that can never be read back would also never
	// satisfy the "already recorded" half of the guard, so the poll would rewrite the file
	// every few seconds for as long as the record stays identity-less — which, during an
	// upgrade, is a whole turn.
	if _, ok := observedEnds.Read(sid); ok {
		t.Fatal("an unreadable observation was written; every later poll rewrites it")
	}
}

// The settle adopts the earlier observation instead of restamping: both describe the same end,
// and a timestamp that moved forward reads as a second turn having finished.
func TestPersistTurnEndAdoptsTheObservedEnd(t *testing.T) {
	const sid = "slot-first"
	inFlight(t, sid, true)

	PersistTurnEnd(sid, "idle")

	st, _ := Read(sid)
	if st.TurnEndAt != stampedEarlier {
		t.Errorf("TurnEndAt = %q, want the observed end %q", st.TurnEndAt, stampedEarlier)
	}
	if st.State != "idle" || !st.TurnEnd {
		t.Errorf("the settled end of turn is wrong: %+v", st)
	}
	// And the settle outlives the observation, so nothing reads it twice.
	if got := ObservedTurnEnd(sid); got != "" {
		t.Errorf("the observation survived the settle: %q", got)
	}
}

// With nothing observed first, the settle is the observation and stamps its own time — the hook
// and managed routes (which never call RecordTurnEnd) reach lastTurnEndAt only through this.
func TestPersistTurnEndStampsWhenNothingObservedItFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "slot-hook"

	PersistTurnEnd(sid, "idle")

	st, _ := Read(sid)
	if st.TurnEndAt == "" || st.TurnEndAt != st.TS {
		t.Fatalf("a hook-route end of turn carries no time: %+v", st)
	}
}

// A new turn clears the settled time with the rest of the record: Persist writes a fresh one,
// so "mid-turn reads empty" and "a restart reads empty" hold for TurnEndAt exactly as they do
// for the TurnEnd bit beside it.
func TestPersistClearsTheSettledEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sid = "slot-next"
	PersistTurnEnd(sid, "idle")

	Persist(sid, "working")

	if st, _ := Read(sid); st.TurnEndAt != "" || st.TurnEnd {
		t.Fatalf("the previous turn's end survived into the next turn: %+v", st)
	}
}

// Remove is the heal path; it has to take the observation with it, or a healed session carries
// the end of a turn whose record is gone.
func TestRemoveDropsTheObservation(t *testing.T) {
	const sid = "slot-heal"
	inFlight(t, sid, true)

	Remove(sid)

	if _, ok := observedEnds.Read(sid); ok {
		t.Fatal("the observation outlived the status record it belongs to")
	}
}

// Both stores are per-sid files under HOME; the tests above depend on that isolation holding.
func TestStatusStoresStayUnderTheTestHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, p := range []string{statusFiles.Path("slot-x"), observedEnds.Path("slot-x")} {
		if !strings.HasPrefix(p, home+"/") {
			t.Fatalf("status path %q escapes the test HOME %q", p, home)
		}
	}
}
