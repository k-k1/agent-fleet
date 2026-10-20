package memreview

// Tests for review round 3 of the part B change.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 3A: a decision removes the run's staged copies too (a run record being rewritten when the process
// stopped leaves a staged copy of the whole record), whatever state the run is in.
func TestADecisionRemovesTheRunsStagedCopies(t *testing.T) {
	for _, ended := range []bool{false, true} {
		name := "open run"
		if ended {
			name = "ended run"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.replies = []func() (string, error){say(sentinelReply("note-a", "note-b"))}
			stage := "enqueued"
			if ended {
				stage = "ended"
			}
			if !h.reviewUntilCrash(h.reviewer(), stage, h.req(TriggerAuto, turns(4))) {
				t.Fatal("no stop")
			}
			run := onlyRun(t)
			if err := os.MkdirAll(runStore.staging(), 0o700); err != nil {
				t.Fatal(err)
			}
			orphan := filepath.Join(runStore.staging(), run.ID+".12345")
			if err := os.WriteFile(orphan, []byte(sentinel+" 0 and the reason text"), 0o600); err != nil {
				t.Fatal(err)
			}
			var synced []string
			old := fsOps
			t.Cleanup(func() { fsOps = old })
			fsOps.syncDir = func(d string) error { synced = append(synced, d); return old.syncDir(d) }
			if err := Decide(Pending()[0].ID, DecisionReject, h.now); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(orphan); !os.IsNotExist(err) {
				t.Fatalf("the run's staged copy survived the decision (%v)", err)
			}
			found := false
			for _, d := range synced {
				found = found || d == runStore.staging()
			}
			if !found {
				t.Errorf("the staging directory was not synced after the removal: %v", synced)
			}
		})
	}
}

// 3B: a sync that failed once is retried: directories that are already visible are not assumed to
// have been made durable.
func TestAFailedDirectorySyncIsRetriedOnTheNextWrite(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "a", "b"))
	old := fsOps
	t.Cleanup(func() { fsOps = old })
	// Fail every directory sync once the chain exists.
	fsOps.syncDir = func(string) error { return errors.New("transient") }
	if err := runStore.Write("r1", Run{ID: "r1"}); err == nil {
		t.Fatal("the write reported success while no directory could be synced")
	}
	var synced []string
	fsOps.syncDir = func(d string) error { synced = append(synced, d); return old.syncDir(d) }
	if err := runStore.Write("r1", Run{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, d := range synced {
		have[d] = true
	}
	for _, d := range []string{base, filepath.Join(base, "a"), filepath.Join(base, "a", "b"), stateRoot(), runStore.Dir(), runStore.staging()} {
		if !have[d] {
			t.Errorf("%s was never synced after the failure (synced: %v)", d, synced)
		}
	}
}

// ...and once per process the whole responsible chain is synced even when nothing was created, since
// an earlier process may have failed there.
func TestTheResponsibleChainIsSyncedOncePerProcess(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if err := os.MkdirAll(runStore.Dir(), 0o700); err != nil { // already visible, sync status unknown
		t.Fatal(err)
	}
	var synced []string
	old := fsOps
	t.Cleanup(func() { fsOps = old })
	fsOps.syncDir = func(d string) error { synced = append(synced, d); return old.syncDir(d) }
	if err := runStore.Write("r1", Run{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, d := range synced {
		have[d] = true
	}
	for _, d := range []string{stateRoot(), filepath.Dir(stateRoot()), runStore.Dir(), runStore.staging()} {
		if !have[d] {
			t.Errorf("%s was not synced (synced: %v)", d, synced)
		}
	}
}

// 3C: a lost attempt whose end is unknown could own any row near it, so the next attempt is not
// linked to a row an earlier attempt has taken, nor to one that earlier attempt may own.
func TestASecondLostAttemptIsNotLinkedToTheFirstOnesRow(t *testing.T) {
	h := newHarness(t)
	h.now = ledgerNow()
	h.ledger = ledgerSpend
	t0 := h.now
	if !h.reviewUntilCrash(h.reviewer(), "dispatched", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no stop")
	}
	appendReviewRow(t, t0.Add(300*time.Millisecond), "call-1", 999) // attempt 1's own, billed before the stop
	// Restart in the same second: attempt 1 is settled (linked), attempt 2 is dispatched, and the
	// process stops again before its call.
	h.now = t0.Add(500 * time.Millisecond)
	if !h.reviewUntilCrash(h.reviewer(), "dispatched", h.req(TriggerManual, turns(4))) {
		t.Fatal("no second stop")
	}
	h.now = t0.Add(700 * time.Millisecond)
	// Both attempts are spent, so the restart settles the second and parks the window.
	if res, err := h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(4))); err != nil || res.State != StateFailed {
		t.Fatalf("%+v %v", res, err)
	}
	as := onlyRun(t).Attempts
	if len(as) != 2 || as[0].Cost != "linked" || as[0].Call != "call-1" || as[0].Spend != 999 {
		t.Fatalf("attempts = %+v", as)
	}
	if as[1].Outcome != OutcomeLost || as[1].Cost != "unknown" || as[1].Spend != 0 || as[1].Call != "" {
		t.Fatalf("the second lost attempt took the first one's row: %+v", as[1])
	}
}

func TestACallLinkedToOneAttemptIsNeverLinkedToAnother(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	start := ledgerNow()
	appendReviewRow(t, start.Add(time.Second), "call-1", 999)
	q := LedgerQuery{Session: "sess", Since: start.Truncate(time.Second)}
	if hit := ledgerSpend(q); !hit.Found || hit.Call != "call-1" {
		t.Fatalf("hit = %+v", hit)
	}
	q.Exclude = map[string]bool{"call-1": true}
	if hit := ledgerSpend(q); hit.Found {
		t.Fatalf("an excluded call was linked: %+v", hit)
	}
	// linkedCalls gathers the calls of the session's attempts in every run.
	queueEnv(t)
	other := Run{ID: "o", Key: "k", Session: "sess", State: StateDone, Attempts: []Attempt{{N: 1, Outcome: OutcomeLost, Call: "call-9"}}}
	_ = saveRun(&other, time.Now())
	cur := &Run{ID: "cur", Session: "sess", Attempts: []Attempt{{N: 1, Outcome: OutcomeLost, Call: "call-8"}}}
	got := linkedCalls(cur)
	if !got["call-9"] || !got["call-8"] || len(got) != 2 {
		t.Fatalf("linked = %v", got)
	}
}

// An earlier lost attempt that was settled as cost-unknown (the ledger had not caught up) may still
// own a row that appears later; the next attempt, close behind it, must not take that row.
func TestALateRowOfAnUnknownCostAttemptIsNotTakenByTheNextOne(t *testing.T) {
	h := newHarness(t)
	h.now = ledgerNow()
	h.ledger = ledgerSpend
	t0 := h.now
	if !h.reviewUntilCrash(h.reviewer(), "dispatched", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no stop")
	}
	h.now = t0.Add(500 * time.Millisecond) // the ledger has nothing yet: attempt 1 is settled as unknown
	if !h.reviewUntilCrash(h.reviewer(), "dispatched", h.req(TriggerManual, turns(4))) {
		t.Fatal("no second stop")
	}
	appendReviewRow(t, t0.Add(300*time.Millisecond), "call-1", 999) // attempt 1's row, flushed late
	h.now = t0.Add(700 * time.Millisecond)
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(4))); err != nil {
		t.Fatal(err)
	}
	as := onlyRun(t).Attempts
	if as[0].Cost != "unknown" || as[1].Cost != "unknown" || as[1].Spend != 0 {
		t.Fatalf("attempts = %+v", as)
	}
}

// The query carries the calls already linked to other attempts (of this run or another).
func TestSettleLostExcludesCallsAlreadyLinked(t *testing.T) {
	queueEnv(t)
	start := ledgerNow()
	prior := Run{ID: "prior", Key: "k", Session: "sess", State: StateDone,
		Attempts: []Attempt{{N: 1, StartedAt: start.Add(-time.Hour), Outcome: OutcomeLost, Cost: "linked", Call: "call-7"}}}
	if err := saveRun(&prior, start); err != nil {
		t.Fatal(err)
	}
	var got map[string]bool
	r := &Reviewer{Ledger: func(q LedgerQuery) LedgerHit { got = q.Exclude; return LedgerHit{} }}
	run := &Run{ID: "cur", Session: "sess", Attempts: []Attempt{{N: 1, StartedAt: start, Outcome: OutcomeDispatched}}}
	r.settleLost(run)
	if !got["call-7"] {
		t.Fatalf("exclude = %v", got)
	}
}

// What a successful sync has made durable is not synced again on every write.
func TestSyncedDirectoriesAreNotResyncedOnEveryWrite(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "a", "b"))
	if err := runStore.Write("r1", Run{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	var synced []string
	old := fsOps
	t.Cleanup(func() { fsOps = old })
	fsOps.syncDir = func(d string) error { synced = append(synced, d); return old.syncDir(d) }
	if err := runStore.Write("r2", Run{ID: "r2"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range synced {
		if d == base || d == filepath.Join(base, "a") {
			t.Errorf("%s was synced again by an ordinary write", d)
		}
	}
}
