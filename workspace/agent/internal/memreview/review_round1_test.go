package memreview

// Tests for review round 1 of the part B change: what the state keeps after a decision, replays
// after a stop, durability, bounds and the ledger link.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const sentinel = "SENTINEL-PROPOSAL-BODY-TEXT"

func sentinelReply(names ...string) string {
	var ops []string
	for i, n := range names {
		ops = append(ops, fmt.Sprintf(`{"op":"create","scope":"project","name":%q,"type":"user","description":"d","body":"%s %d","reason":"the reason text","evidence":[2]}`, n, sentinel, i))
	}
	return `{"ops":[` + strings.Join(ops, ",") + `]}`
}

func filesUnder(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func anyContains(files map[string]string, needle string) (string, bool) {
	for p, c := range files {
		if strings.Contains(c, needle) {
			return p, true
		}
	}
	return "", false
}

// R1: proposal text lives in the queue alone. The ended run carries counts, and after a decision
// the text is nowhere under the state directory; a rejection leaves only a hash.
func TestEndedRunsHoldNoProposalTextAndADecisionLeavesNone(t *testing.T) {
	for _, decision := range []string{DecisionReject, DecisionApprove} {
		t.Run(decision, func(t *testing.T) {
			h := newHarness(t)
			h.replies = []func() (string, error){say(sentinelReply("note-a", "note-b"))}
			res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
			if err != nil || res.Queued != 2 {
				t.Fatalf("%+v %v", res, err)
			}
			run := onlyRun(t)
			if run.State != StateDone || run.Ops != nil || run.Queued != 2 {
				t.Fatalf("run = %+v", run)
			}
			files := filesUnder(t, stateRoot())
			for p, c := range files {
				if strings.Contains(c, sentinel) && !strings.Contains(p, "/pending/") {
					t.Errorf("text outside the queue: %s", p)
				}
			}
			for _, p := range Pending() {
				if err := Decide(p.ID, decision, h.now); err != nil {
					t.Fatal(err)
				}
			}
			if p, bad := anyContains(filesUnder(t, stateRoot()), sentinel); bad {
				t.Fatalf("text left after the decision: %s", p)
			}
			if p, bad := anyContains(filesUnder(t, stateRoot()), "the reason text"); bad {
				t.Fatalf("reason left after the decision: %s", p)
			}
		})
	}
}

// A failed run keeps no reply text either.
func TestFailedRunsHoldNoReplyText(t *testing.T) {
	h := newHarness(t)
	leak := strings.Replace(sentinelReply("x"), `"body":"`, `"body":"`+fakeKey()+` `, 1)
	h.replies = []func() (string, error){say(leak)}
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil {
		t.Fatal(err)
	}
	if p, bad := anyContains(filesUnder(t, stateRoot()), sentinel); bad {
		t.Fatalf("text kept: %s", p)
	}
}

// R2: a proposal decided between queueing and the end of the run does not come back when the run
// is resumed.
func TestADecidedProposalIsNotQueuedAgainWhenTheRunReplays(t *testing.T) {
	for _, decision := range []string{DecisionApprove, DecisionReject} {
		t.Run(decision, func(t *testing.T) {
			h := newHarness(t)
			h.replies = []func() (string, error){say(sentinelReply("note-a", "note-b"))}
			if !h.reviewUntilCrash(h.reviewer(), "enqueued", h.req(TriggerAuto, turns(4))) {
				t.Fatal("no stop")
			}
			ps := Pending()
			if len(ps) != 2 {
				t.Fatalf("pending = %d", len(ps))
			}
			if err := Decide(ps[0].ID, decision, h.now); err != nil {
				t.Fatal(err)
			}
			res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
			if err != nil || res.State != StateDone || h.calls != 1 {
				t.Fatalf("%+v %v calls %d", res, err, h.calls)
			}
			left := Pending()
			if len(left) != 1 || left[0].ID != ps[1].ID {
				t.Fatalf("pending after the replay = %+v", left)
			}
			// The marker has done its job once the run has ended.
			if n := len(decidedStore.keys()); n != 0 {
				t.Errorf("%d decided markers outlive the run", n)
			}
			if p, bad := anyContains(filesUnder(t, stateRoot()), sentinel+" 0"); bad && !strings.Contains(p, "/pending/") {
				t.Errorf("text outside the queue: %s", p)
			}
		})
	}
}

// R3: the ending of a failed run is saved before anything else, so a stop right after it cannot
// make the next start take the refused attempt for a lost one and call again.
func TestARefusedReplyIsNotCalledAgainAfterAStopAtTheEnd(t *testing.T) {
	h := newHarness(t)
	leak := fmt.Sprintf(`{"ops":[{"op":"create","scope":"project","name":"x","type":"user","description":"d","body":"use %s","reason":"r","evidence":[2]}]}`, fakeKey())
	h.replies = []func() (string, error){say(leak)}
	if !h.reviewUntilCrash(h.reviewer(), "ended", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no stop")
	}
	if Watermark("sess-key") != 0 {
		t.Fatal("the watermark moved before the stop point")
	}
	if run := onlyRun(t); run.State != StateFailed || run.Attempts[0].Outcome != OutcomeSecret {
		t.Fatalf("run = %+v", run)
	}
	// More turns have arrived: the parked window must not be reviewed again or shifted.
	h.replies = []func() (string, error){say(`{"ops":[]}`)}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(8)))
	if err != nil || h.calls != 2 || res.State != StateDone || Watermark("sess-key") != 8 {
		t.Fatalf("%+v %v calls %d", res, err, h.calls)
	}
	if strings.Contains(h.prompts[1], "[1] user") || !strings.Contains(h.prompts[1], "[5] user") {
		t.Error("the second call reviewed the parked window again")
	}
}

func TestAFinishedRunWhoseWatermarkWasNotMovedDoesNotShiftTheNextWindow(t *testing.T) {
	h := newHarness(t)
	if !h.reviewUntilCrash(h.reviewer(), "ended", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no stop")
	}
	h.replies = []func() (string, error){say(`{"ops":[]}`)}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(8)))
	if err != nil || res.State != StateDone || h.calls != 2 {
		t.Fatalf("%+v %v calls %d", res, err, h.calls)
	}
	if strings.Contains(h.prompts[1], "[1] user") {
		t.Error("turns of the finished window were reviewed again")
	}
}

// R4: a record is synced before it is renamed into place, and the directory after; a record that
// cannot be made durable stops the call that depends on it.
func TestWritesAreSyncedInOrderAndAFailedSyncStopsTheCall(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var events []string
	old := fsOps
	t.Cleanup(func() { fsOps = old })
	fsOps.syncFile = func(f *os.File) error { events = append(events, "file"); return f.Sync() }
	fsOps.syncDir = func(d string) error { events = append(events, "dir:"+filepath.Base(d)); return old.syncDir(d) }
	fsOps.rename = func(a, b string) error { events = append(events, "rename"); return os.Rename(a, b) }
	if err := runStore.Write("r1", Run{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	// The directories created on the way are synced first; then the order is file, rename, dirs.
	at := -1
	for i, e := range events {
		if e == "file" {
			at = i
			break
		}
	}
	if at < 0 || len(events) < at+3 || events[at+1] != "rename" || !strings.HasPrefix(events[at+2], "dir:") {
		t.Fatalf("events = %v", events)
	}
	if _, err := os.Stat(filepath.Join(runStore.Dir(), "r1.json")); err != nil {
		t.Fatal(err)
	}

	// A sync that fails: the record is not in place, and nothing dependent on it runs.
	h := newHarness(t)
	fsOps.syncFile = func(*os.File) error { return errors.New("disk on fire") }
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); err == nil {
		t.Fatal("a review went on without a durable claim")
	}
	if h.calls != 0 || len(runs()) != 0 {
		t.Fatalf("calls %d runs %d", h.calls, len(runs()))
	}
	if entries, _ := os.ReadDir(runStore.staging()); len(entries) != 0 {
		t.Errorf("a failed write left %d staged files", len(entries))
	}
}

func TestADailyCountThatCannotBeSavedStopsTheCall(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	old := fsOps
	t.Cleanup(func() { fsOps = old })
	// Let the claim through, fail the next write (the daily count).
	n := 0
	fsOps.syncFile = func(f *os.File) error {
		if n++; n > 1 {
			return errors.New("disk on fire")
		}
		return f.Sync()
	}
	if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); err == nil || h.calls != 0 {
		t.Fatalf("err = %v calls %d", err, h.calls)
	}
}

// R4: a body staged by a writer that was killed before the rename is removed with the proposal, and
// swept when it is old.
func TestStagedTextIsCollected(t *testing.T) {
	queueEnv(t)
	p := prop("abc", "n1", sentinel)
	if _, err := enqueue(p); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(pendingStore.staging(), "abc.12345")
	if err := os.WriteFile(orphan, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Decide("abc", DecisionApprove, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("the staged copy of a decided proposal survived (%v)", err)
	}
	// An unrelated old staged file goes with the sweep; a fresh one stays.
	old, fresh := filepath.Join(pendingStore.staging(), "zzz.1"), filepath.Join(pendingStore.staging(), "zzz.2")
	_ = os.WriteFile(old, []byte(sentinel), 0o600)
	_ = os.WriteFile(fresh, []byte("x"), 0o600)
	_ = os.Chtimes(old, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	pruneRuns(time.Now())
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an old staged file survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh staged file was swept")
	}
}

// R5: a removal that fails is an error, not a success with the text still there.
func TestDecideAndForgetReportARemovalThatFailed(t *testing.T) {
	queueEnv(t)
	if _, err := enqueue(prop("a", "n1", sentinel)); err != nil {
		t.Fatal(err)
	}
	dir := pendingStore.Dir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := Decide("a", DecisionApprove, time.Now()); err == nil {
		t.Fatal("Decide reported success while the text is still there")
	}
	if len(Pending()) != 1 {
		t.Fatal("the proposal disappeared without being removed")
	}
	_ = os.Chmod(dir, 0o700)
	if err := Decide("a", DecisionApprove, time.Now()); err != nil || len(Pending()) != 0 {
		t.Fatalf("the retry: %v", err)
	}

	// ForgetMemory: the hash that could not be removed is not counted.
	if _, err := enqueue(prop("b", "n2", "t")); err != nil {
		t.Fatal(err)
	}
	if err := Decide("b", DecisionReject, time.Now()); err != nil {
		t.Fatal(err)
	}
	rdir := rejectedStore.Dir()
	_ = os.Chmod(rdir, 0o500)
	t.Cleanup(func() { _ = os.Chmod(rdir, 0o700) })
	if n, err := ForgetMemory("project", "n2", "p"); n != 0 || err == nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	_ = os.Chmod(rdir, 0o700)
	if n, err := ForgetMemory("project", "n2", "p"); n != 1 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// R6: the bound holds inside the insert. A reply that does not fit is kept whole and queued as room
// appears; skips need no room.
func TestTheQueueBoundHoldsInsideTheInsertAndAReplyIsNotLost(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < MaxPending-1; i++ {
		if _, err := enqueue(Proposal{ID: fmt.Sprintf("p%03d", i), Session: "s", Hash: fmt.Sprintf("h%03d", i), Op: Op{Op: OpCreate, Scope: "user", Name: "n", Body: "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	h.replies = []func() (string, error){say(sentinelReply("note-a", "note-b", "note-c"))}
	_, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v", err)
	}
	if n := len(Pending()); n != MaxPending {
		t.Fatalf("pending = %d, the bound is %d", n, MaxPending)
	}
	if run := onlyRun(t); run.State != StateAnswered || len(run.Ops) != 3 {
		t.Fatalf("the reply was not kept: %+v", run)
	}
	// Still full: nothing happens, and the helper is not asked again.
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); !errors.Is(err, ErrQueueFull) || h.calls != 1 {
		t.Fatalf("err = %v calls %d", err, h.calls)
	}
	// Room appears: the rest is queued, still without a call.
	for _, p := range Pending()[:2] {
		if p.RunID == "" {
			if err := Decide(p.ID, DecisionApprove, h.now); err != nil {
				t.Fatal(err)
			}
		}
	}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.State != StateDone || h.calls != 1 {
		t.Fatalf("%+v %v calls %d", res, err, h.calls)
	}
	names := map[string]bool{}
	for _, p := range Pending() {
		names[p.Op.Name] = true
	}
	for _, n := range []string{"note-a", "note-b", "note-c"} {
		if !names[n] {
			t.Errorf("%s was lost", n)
		}
	}
	if len(Pending()) > MaxPending {
		t.Fatalf("pending = %d", len(Pending()))
	}
}

func TestSkipsNeedNoRoomInAFullQueue(t *testing.T) {
	queueEnv(t)
	for i := 0; i < MaxPending; i++ {
		if _, err := enqueue(Proposal{ID: fmt.Sprintf("p%03d", i), Session: "s", Hash: fmt.Sprintf("h%03d", i), Op: Op{Op: OpCreate, Scope: "user", Name: "n", Body: "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	dup := Proposal{ID: "other", Session: "s", Hash: "h007", Op: Op{Op: OpCreate, Scope: "user", Name: "n", Body: "b"}}
	if how, err := enqueue(dup); err != nil || how != SkippedDuplicate {
		t.Fatalf("duplicate: %q %v", how, err)
	}
	if err := rejectedStore.Write("hrej", rejection{Hash: "hrej"}); err != nil {
		t.Fatal(err)
	}
	if how, err := enqueue(Proposal{ID: "o2", Session: "s", Hash: "hrej", Op: Op{Op: OpCreate, Scope: "user", Name: "n2", Body: "b"}}); err != nil || how != SkippedRejected {
		t.Fatalf("rejected: %q %v", how, err)
	}
	if _, err := enqueue(Proposal{ID: "o3", Session: "s", Hash: "hnew", Op: Op{Op: OpCreate, Scope: "user", Name: "n3", Body: "b"}}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("a new proposal in a full queue: %v", err)
	}
}

// R7: the ledger's second-precision stamp is matched against the attempt's start by second, a call
// that used several models is summed, and the attempt before it is not taken for it.
func TestLedgerLinkHandlesSecondPrecisionAndSplitCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	// AppendRows stamps its file with the real day, so the fixture is built around now.
	start := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Second).Add(500 * time.Millisecond)
	row := func(ts time.Time, call string, spend int) usagex.Record {
		return usagex.Record{TS: ts.UTC().Format(time.RFC3339), Call: call, Feature: usagex.FeatureMemoryReview, Ref: "sess", Spend: spend, Kind: "claude"}
	}
	if err := usagex.AppendRows([]usagex.Record{
		row(start.Add(-time.Hour), "old", 999),                         // an earlier call of the same session
		row(start.Truncate(time.Second), "mine", 100),                  // same second as the start, before the start instant
		row(start.Truncate(time.Second), "mine", 20),                   // the same call's second model
		row(start.Add(ledgerAttemptWindow+time.Minute), "later", 7000), // a later call, outside the window
		{TS: start.Format(time.RFC3339), Call: "x", Feature: usagex.FeatureTitleSession, Ref: "sess", Spend: 5},
	}); err != nil {
		t.Fatal(err)
	}
	hit := ledgerSpend(LedgerQuery{Session: "sess", Since: start.Truncate(time.Second)})
	if !hit.Found || hit.Spend != 120 || hit.Call != "mine" {
		t.Fatalf("hit = %+v: want 120 (both rows of the call, none of the others)", hit)
	}
	if ledgerSpend(LedgerQuery{Session: "other-session", Since: start.Truncate(time.Second)}).Found {
		t.Error("another session's row matched")
	}
	// Two calls in the window: which one is the attempt's cannot be told, so none is linked.
	if err := usagex.AppendRows([]usagex.Record{row(start.Add(time.Minute), "second-call", 50)}); err != nil {
		t.Fatal(err)
	}
	if ledgerSpend(LedgerQuery{Session: "sess", Since: start.Truncate(time.Second)}).Found {
		t.Error("an ambiguous window was linked")
	}

	// settleLost starts the search at the attempt's second, or one later when the attempt before
	// it ended within that second.
	r := &Reviewer{Ledger: func(q LedgerQuery) LedgerHit {
		return LedgerHit{Spend: int(q.Since.Unix() % 1000), Found: true}
	}}
	run := &Run{Session: "sess", Attempts: []Attempt{
		{N: 1, StartedAt: start.Add(-time.Minute), EndedAt: start.Add(-100 * time.Millisecond), Outcome: OutcomeMalformed},
		{N: 2, StartedAt: start, Outcome: OutcomeDispatched},
	}}
	r.settleLost(run)
	if got, want := run.Attempts[1].Spend, int(start.Truncate(time.Second).Add(time.Second).Unix()%1000); got != want {
		t.Fatalf("since second: %d, want %d (the previous attempt ended in the same second)", got, want)
	}
	run = &Run{Session: "sess", Attempts: []Attempt{
		{N: 1, StartedAt: start.Add(-time.Minute), EndedAt: start.Add(-30 * time.Second), Outcome: OutcomeMalformed},
		{N: 2, StartedAt: start, Outcome: OutcomeDispatched},
	}}
	r.settleLost(run)
	if got, want := run.Attempts[1].Spend, int(start.Truncate(time.Second).Unix()%1000); got != want {
		t.Fatalf("since second: %d, want %d", got, want)
	}
}

// R8: a pin on a backend that cannot run deny-all, left alone, must not grow the record.
func TestAnUnusableBackendDoesNotGrowTheRunRecord(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 30; i++ {
		h.replies = append(h.replies, fail(fmt.Errorf("%w: probe", chatx.ErrDenyAllUnavailable)))
	}
	r := h.reviewer()
	for i := 0; i < 30; i++ {
		if _, err := r.Review(context.Background(), h.req(TriggerManual, turns(4))); !errors.Is(err, ErrNoBackend) {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	run := onlyRun(t)
	if len(run.Attempts) != 0 || run.Unsent != 30 || run.State != StateClaimed {
		t.Fatalf("attempts %d unsent %d state %s", len(run.Attempts), run.Unsent, run.State)
	}
	if CallsToday(h.now) != 0 {
		t.Fatalf("daily calls = %d", CallsToday(h.now))
	}
	// The budget is whole when a backend appears.
	h.replies = []func() (string, error){say(`{"ops":[]}`)}
	if res, err := r.Review(context.Background(), h.req(TriggerManual, turns(4))); err != nil || res.State != StateDone {
		t.Fatalf("%+v %v", res, err)
	}
}

// A decided marker is only needed while its run can replay; the sweep that runs at each claim drops
// the ones whose run has ended or is gone, and keeps those of an open run.
func TestDecidedMarkersOfEndedRunsAreCollected(t *testing.T) {
	queueEnv(t)
	now := time.Now()
	ended := Run{ID: "ended", Key: "k1", State: StateDone}
	open := Run{ID: "open", Key: "k2", State: StateAnswered}
	_ = saveRun(&ended, now)
	_ = saveRun(&open, now)
	for id, run := range map[string]string{"m-ended": "ended", "m-open": "open", "m-gone": "no-such-run"} {
		if err := decidedStore.Write(id, run); err != nil {
			t.Fatal(err)
		}
	}
	pruneRuns(now)
	left := map[string]bool{}
	for _, k := range decidedStore.keys() {
		left[k] = true
	}
	if len(left) != 1 || !left["m-open"] {
		t.Fatalf("markers left = %v", left)
	}
}
