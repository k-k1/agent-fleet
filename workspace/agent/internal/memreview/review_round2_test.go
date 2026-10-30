package memreview

// Tests for review round 2 of the part B change.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// 2A: a decision that reports success leaves no copy of the decided text anywhere, including in a
// run that has not ended (it stopped after queueing, or waits for room for the rest of its reply).
func TestADecisionScrubsTheCopyInARunThatHasNotEnded(t *testing.T) {
	for _, decision := range []string{DecisionApprove, DecisionReject} {
		t.Run(decision, func(t *testing.T) {
			h := newHarness(t)
			h.replies = []func() (string, error){say(sentinelReply("note-a", "note-b"))}
			if !h.reviewUntilCrash(h.reviewer(), "enqueued", h.req(TriggerAuto, turns(4))) {
				t.Fatal("no stop")
			}
			ps := Pending()
			if err := Decide(ps[0].ID, decision, h.now); err != nil {
				t.Fatal(err)
			}
			files := filesUnder(t, stateRoot())
			if p, bad := anyContains(files, sentinel+" 0"); bad {
				t.Fatalf("decided text left in %s", p)
			}
			// The other operation is undecided and still there (queue and run).
			if _, ok := anyContains(files, sentinel+" 1"); !ok {
				t.Fatal("the undecided operation vanished")
			}
			// Replaying the run neither revives the decided one nor loses the other.
			res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
			if err != nil || res.State != StateDone || h.calls != 1 {
				t.Fatalf("%+v %v calls %d", res, err, h.calls)
			}
			left := Pending()
			if len(left) != 1 || left[0].Op.Name != "note-b" || left[0].Op.Body == "" {
				t.Fatalf("pending = %+v", left)
			}
		})
	}
}

// ...and when the run waits for room, deciding another proposal does not strand a copy either.
func TestADecisionWhileARunWaitsForRoomLeavesNoCopy(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < MaxPending-1; i++ {
		if _, err := enqueue(Proposal{ID: fmt.Sprintf("p%03d", i), Session: "s", Hash: fmt.Sprintf("h%03d", i), Op: Op{Op: OpCreate, Scope: "user", Name: "n", Body: "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	h.replies = []func() (string, error){say(sentinelReply("note-a", "note-b"))}
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); err == nil {
		t.Fatal("expected ErrQueueFull")
	}
	var first Proposal
	for _, p := range Pending() {
		if p.RunID != "" {
			first = p
		}
	}
	if first.ID == "" {
		t.Fatal("no proposal of the waiting run was queued")
	}
	if err := Decide(first.ID, DecisionApprove, h.now); err != nil {
		t.Fatal(err)
	}
	if p, bad := anyContains(filesUnder(t, stateRoot()), sentinel+" 0"); bad {
		t.Fatalf("decided text left in %s", p)
	}
}

// 2B: creating the state root makes every new directory entry durable, up to the existing parent.
func TestCreatingTheStateRootSyncsTheWholeNewChain(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "a", "b"))
	var synced []string
	old := fsOps
	t.Cleanup(func() { fsOps = old })
	fsOps.syncDir = func(d string) error { synced = append(synced, d); return old.syncDir(d) }
	if err := runStore.Write("r1", Run{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		base, filepath.Join(base, "a"), filepath.Join(base, "a", "b"), // the config dir chain and its parent
		stateRoot(), runStore.Dir(), runStore.staging(),
	}
	have := map[string]bool{}
	for _, d := range synced {
		have[d] = true
	}
	for _, d := range want {
		if !have[d] {
			t.Errorf("%s was never synced (synced: %v)", d, synced)
		}
	}
}

func TestRemovingAStagedFileSyncsTheStagingDirectory(t *testing.T) {
	queueEnv(t)
	if err := pendingStore.Write("k", Proposal{ID: "k"}); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(pendingStore.staging(), "k.999")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var synced []string
	old := fsOps
	t.Cleanup(func() { fsOps = old })
	fsOps.syncDir = func(d string) error { synced = append(synced, d); return old.syncDir(d) }
	if err := pendingStore.Remove("k"); err != nil {
		t.Fatal(err)
	}
	has := func(d string) bool {
		for _, s := range synced {
			if s == d {
				return true
			}
		}
		return false
	}
	if !has(pendingStore.staging()) || !has(pendingStore.Dir()) {
		t.Fatalf("synced = %v", synced)
	}
	// The sweep makes its deletions durable too.
	stale := filepath.Join(pendingStore.staging(), "zz.1")
	_ = os.WriteFile(stale, []byte("x"), 0o600)
	_ = os.Chtimes(stale, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	synced = nil
	pendingStore.sweepStaging(time.Hour, time.Now())
	if !has(pendingStore.staging()) {
		t.Fatalf("the sweep did not sync the staging directory: %v", synced)
	}
}

// 2C: an ended run is the evidence for repairing a watermark and is not pruned until the watermark
// has reached its window, however many other runs there are.
func TestPruneKeepsEndedRunsWhoseWatermarkWasNotReached(t *testing.T) {
	h := newHarness(t)
	if !h.reviewUntilCrash(h.reviewer(), "ended", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no stop")
	}
	if Watermark("sess-key") != 0 {
		t.Fatal("the watermark moved before the stop point")
	}
	base := h.now.Add(time.Hour)
	for i := 0; i < keepTerminalRuns+20; i++ {
		other := Run{ID: fmt.Sprintf("other%03d", i), Key: fmt.Sprintf("k%03d", i), Session: "x", State: StateDone, Last: 3}
		_ = moveWatermark(other.Key, 3, base)
		_ = saveRun(&other, base.Add(time.Duration(i)*time.Second))
	}
	pruneRuns(base)
	h.replies = []func() (string, error){say(`{"ops":[]}`)}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.State != "empty" || h.calls != 1 || Watermark("sess-key") != 4 {
		t.Fatalf("%+v %v calls %d wm %d: the window was reviewed again", res, err, h.calls, Watermark("sess-key"))
	}
	// Once the watermark has reached it, the old run can go.
	pruneRuns(base)
	n := 0
	for _, r := range runs() {
		if r.Terminal() {
			n++
		}
	}
	if n > keepTerminalRuns {
		t.Fatalf("terminal runs = %d, want at most %d once every watermark is reached", n, keepTerminalRuns)
	}
}

// 2D: an attempt ends when its call returns, not when it started.
func TestAnAttemptEndsWhenItsCallReturns(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	h.replies = []func() (string, error){func() (string, error) { h.now = h.now.Add(10 * time.Second); return "not json", nil }}
	if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil {
		t.Fatal(err)
	}
	at := onlyRun(t).Attempts[0]
	if !at.EndedAt.Equal(at.StartedAt.Add(10 * time.Second)) {
		t.Fatalf("started %v ended %v", at.StartedAt, at.EndedAt)
	}
}

func ledgerNow() time.Time {
	return time.Now().UTC().Add(-time.Minute).Truncate(time.Second).Add(200 * time.Millisecond)
}

func appendReviewRow(t *testing.T, ts time.Time, call string, spend int) {
	t.Helper()
	if err := usagex.AppendRows([]usagex.Record{{TS: ts.UTC().Format(time.RFC3339), Call: call, Feature: usagex.FeatureMemoryReview, Ref: "sess", Spend: spend, Kind: "claude"}}); err != nil {
		t.Fatal(err)
	}
}

// A lost attempt is not linked to the row of the call before it, in the same run or the run before,
// even when that call ended in the second this attempt started.
func TestALostAttemptIsNotLinkedToAnEarlierCallsRow(t *testing.T) {
	for name, sameRun := range map[string]bool{"the attempt before it": true, "the run before it": false} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.now = ledgerNow()
			h.ledger = ledgerSpend
			first := h.now
			// Call 1 ends 0.5 s after it started (still inside the second); its ledger row is stamped
			// with that second.
			h.replies = []func() (string, error){func() (string, error) {
				h.now = h.now.Add(500 * time.Millisecond)
				appendReviewRow(t, h.now, "call-1", 999)
				if sameRun {
					return "not json", nil
				}
				return `{"ops":[]}`, nil
			}}
			r := h.reviewer()
			if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil {
				t.Fatal(err)
			}
			// The next dispatch starts 100 ms later, in the same second, and the process stops before
			// the call is made: this attempt has no row at all.
			h.now = first.Add(600 * time.Millisecond)
			var req Request
			if sameRun {
				req = h.req(TriggerManual, turns(4))
			} else {
				req = h.req(TriggerAuto, turns(8))
			}
			if !h.reviewUntilCrash(h.reviewer(), "dispatched", req) {
				t.Fatal("no stop")
			}
			h.replies = []func() (string, error){say(`{"ops":[]}`)}
			h.now = h.now.Add(time.Hour)
			if _, err := h.reviewer().Review(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			var lost *Attempt
			for _, run := range runs() {
				for i := range run.Attempts {
					if run.Attempts[i].Outcome == OutcomeLost {
						lost = &run.Attempts[i]
					}
				}
			}
			if lost == nil || lost.Cost != "unknown" || lost.Spend != 0 {
				t.Fatalf("lost attempt = %+v: it must not take the earlier call's 999 tokens", lost)
			}
		})
	}
}

// ...but an attempt whose own call did bill is linked to exactly that call.
func TestALostAttemptIsLinkedToItsOwnRow(t *testing.T) {
	h := newHarness(t)
	h.now = ledgerNow()
	h.ledger = ledgerSpend
	if !h.reviewUntilCrash(h.reviewer(), "dispatched", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no stop")
	}
	// The call was made and billed before the process ended.
	appendReviewRow(t, h.now.Add(3*time.Second), "own-call", 321)
	h.now = h.now.Add(time.Hour)
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil {
		t.Fatal(err)
	}
	if at := onlyRun(t).Attempts[0]; at.Cost != "linked" || at.Spend != 321 {
		t.Fatalf("attempt = %+v", at)
	}
}

func TestSettleLostUsesTheCallsEndOfAnEarlierAttemptOfTheSession(t *testing.T) {
	queueEnv(t)
	start := ledgerNow()
	prior := Run{ID: "prior", Key: "k", Session: "sess", State: StateDone, Attempts: []Attempt{{N: 1, StartedAt: start.Add(-time.Minute), EndedAt: start.Add(-100 * time.Millisecond), Outcome: OutcomeAnswered}}}
	if err := saveRun(&prior, start); err != nil {
		t.Fatal(err)
	}
	var since time.Time
	r := &Reviewer{Ledger: func(q LedgerQuery) LedgerHit { since = q.Since; return LedgerHit{} }}
	run := &Run{ID: "cur", Session: "sess", Attempts: []Attempt{{N: 1, StartedAt: start, Outcome: OutcomeDispatched}}}
	r.settleLost(run)
	if want := start.Truncate(time.Second).Add(time.Second); !since.Equal(want) {
		t.Fatalf("since = %v, want %v (the earlier run's call ended in this attempt's second)", since, want)
	}
	if !strings.Contains(run.Attempts[0].Cost, "unknown") {
		t.Fatalf("cost = %q", run.Attempts[0].Cost)
	}
}
