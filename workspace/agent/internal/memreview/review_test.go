package memreview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// harness is a Reviewer with a scripted helper, a movable clock and a state directory of its own.
type harness struct {
	t     *testing.T
	now   time.Time
	calls int
	// reply is what the next call returns; replies is consulted first when non-empty.
	replies []func() (string, error)
	prompts []string
	tags    []usagex.Tag
	enabled bool
	ledger  func(LedgerQuery) LedgerHit
	project string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	return &harness{t: t, now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.Local), enabled: true, project: "proj-1"}
}

// reviewer builds a fresh Reviewer over the harness (a new one models a restart: the state is the
// directory, nothing else survives).
func (h *harness) reviewer() *Reviewer {
	return &Reviewer{
		Now:     func() time.Time { return h.now },
		Enabled: func() bool { return h.enabled },
		Snapshot: func(session string) (memoryx.ReviewSnapshot, error) {
			return memoryx.ReviewSnapshot{Session: session, Kind: "claude", HasProject: true, ProjectID: h.project,
				Entries: []memoryx.ReviewEntry{{Scope: "project", Name: "old-note", Description: "d", Type: "project", Revision: 2, Body: "old body"}}}, nil
		},
		Call: func(ctx context.Context, persona, prompt string) (string, error) {
			h.calls++
			h.prompts = append(h.prompts, prompt)
			if tag, ok := usagex.TagOf(ctx); ok {
				h.tags = append(h.tags, tag)
			}
			if len(h.replies) > 0 {
				f := h.replies[0]
				h.replies = h.replies[1:]
				return f()
			}
			return goodReply("fresh-note"), nil
		},
		Ledger:  h.ledger,
		Backoff: time.Minute,
	}
}

func goodReply(name string) string {
	return fmt.Sprintf(`{"ops":[{"op":"create","scope":"project","name":%q,"type":"feedback","description":"one line","body":"the text","reason":"why","evidence":[2]}]}`, name)
}

func say(s string) func() (string, error)   { return func() (string, error) { return s, nil } }
func fail(err error) func() (string, error) { return func() (string, error) { return "", err } }

func turns(n int) []transcript.Turn {
	var ts []transcript.Turn
	for i := 1; i <= n; i++ {
		if i%2 == 1 {
			ts = append(ts, user(fmt.Sprintf("question number %d", i)))
		} else {
			ts = append(ts, transcript.Turn{Role: "assistant", Parts: []transcript.Part{{Kind: "text", Text: fmt.Sprintf("answer number %d", i)}}})
		}
	}
	return ts
}

func (h *harness) req(trigger string, ts []transcript.Turn) Request {
	return Request{Key: "sess-key", Session: "sess", Trigger: trigger, Turns: ts, FirstNumber: 1}
}

// crash is what a hook panics with to model the process ending at a stage.
type crash struct{ stage string }

func (h *harness) reviewUntilCrash(r *Reviewer, at string, req Request) (crashed bool) {
	h.t.Helper()
	r.hook = func(stage string) {
		if stage == at {
			panic(crash{stage})
		}
	}
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(crash); !ok {
				panic(v)
			}
			crashed = true
		}
	}()
	_, _ = r.Review(context.Background(), req)
	return false
}

func onlyRun(t *testing.T) Run {
	t.Helper()
	rs := runs()
	if len(rs) != 1 {
		t.Fatalf("runs = %d, want 1", len(rs))
	}
	return rs[0]
}

func TestReviewQueuesTheProposalsAndMovesTheWatermark(t *testing.T) {
	h := newHarness(t)
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(6)))
	if err != nil {
		t.Fatal(err)
	}
	if res.State != StateDone || res.Ops != 1 || res.Queued != 1 || res.Watermark != 6 || res.More {
		t.Fatalf("result = %+v", res)
	}
	ps := Pending()
	if len(ps) != 1 || ps[0].Op.Name != "fresh-note" || ps[0].ProjectID != "proj-1" || ps[0].Session != "sess" || ps[0].RunID != res.RunID {
		t.Fatalf("pending = %+v", ps)
	}
	run := onlyRun(t)
	if run.State != StateDone || len(run.Attempts) != 1 || run.Attempts[0].Outcome != OutcomeAnswered || run.First != 1 || run.Last != 6 {
		t.Fatalf("run = %+v", run)
	}
	// One ledger-tagged call: the feature, the trigger and the reviewed session as ref.
	if h.calls != 1 || len(h.tags) != 1 || h.tags[0] != (usagex.Tag{Feature: "memory.review", Trigger: "auto", Ref: "sess"}) {
		t.Fatalf("calls %d tags %+v", h.calls, h.tags)
	}
	if CallsToday(h.now) != 1 {
		t.Fatalf("calls today = %d", CallsToday(h.now))
	}
	// The same turns again have nothing left to review and cost no call.
	res, err = h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(6)))
	if err != nil || res.State != "empty" || h.calls != 1 {
		t.Fatalf("second review: %+v, %v, calls %d", res, err, h.calls)
	}
}

func TestReviewWithNoProposalsStillMovesTheWatermark(t *testing.T) {
	h := newHarness(t)
	h.replies = []func() (string, error){say(`{"ops":[]}`)}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.State != StateDone || res.Queued != 0 || Watermark("sess-key") != 4 || len(Pending()) != 0 {
		t.Fatalf("%+v %v wm %d", res, err, Watermark("sess-key"))
	}
}

// A long transcript is reviewed one window per call, oldest first, and the watermark follows.
func TestReviewWorksThroughALongTranscriptWindowByWindow(t *testing.T) {
	h := newHarness(t)
	var ts []transcript.Turn
	for i := 0; i < 90; i++ {
		ts = append(ts, user(strings.Repeat(fmt.Sprintf("t%02d ", i), 300)))
	}
	h.replies = []func() (string, error){say(`{"ops":[]}`), say(`{"ops":[]}`), say(`{"ops":[]}`), say(`{"ops":[]}`)}
	r := h.reviewer()
	prev := 0
	for i := 0; i < 20; i++ {
		res, err := r.Review(context.Background(), h.req(TriggerAuto, ts))
		if err != nil {
			t.Fatal(err)
		}
		if res.State == "empty" {
			break
		}
		if res.Watermark <= prev {
			t.Fatalf("round %d: the watermark did not advance (%d -> %d)", i, prev, res.Watermark)
		}
		prev = res.Watermark
		if !res.More {
			break
		}
	}
	if prev != 90 || h.calls < 2 {
		t.Fatalf("watermark %d after %d calls", prev, h.calls)
	}
	if strings.Index(h.prompts[0], "t00 ") < 0 || strings.Contains(h.prompts[0], "t89 ") {
		t.Error("the first call must carry the oldest turns")
	}
}

func TestReviewNeverRunsWhileSwitchedOff(t *testing.T) {
	h := newHarness(t)
	h.enabled = false
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(4))); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	if h.calls != 0 || len(runs()) != 0 {
		t.Fatal("a switched-off review claimed or called")
	}
	// Switched off after the claim and before the call: the run stays open, nothing is sent.
	h.enabled = true
	r := h.reviewer()
	n := 0
	r.Enabled = func() bool { n++; return n <= 2 } // the top check and the one before the claim
	if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	if h.calls != 0 || len(runs()) != 1 || Watermark("sess-key") != 0 {
		t.Fatalf("calls %d runs %d wm %d", h.calls, len(runs()), Watermark("sess-key"))
	}
}

// Manual and automatic triggers racing on one window make one call; any other session waits.
func TestReviewAllowsOneInFlightAndOneCallPerWindow(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var mu sync.Mutex
	calls := 0
	r := h.reviewer()
	r.Call = func(ctx context.Context, persona, prompt string) (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		started <- struct{}{}
		<-release
		return `{"ops":[]}`, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4)))
		errs <- err
	}()
	<-started
	// Another trigger on the same session, and another session altogether: both are refused.
	other := h.req(TriggerManual, turns(4))
	other.Key, other.Session = "other-key", "other"
	for _, req := range []Request{h.req(TriggerManual, turns(4)), other} {
		if _, err := r.Review(context.Background(), req); !errors.Is(err, ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
	}
	close(release)
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("helper calls = %d, want 1", calls)
	}
}

func TestReviewRedactsTheTranscriptBeforeTheCall(t *testing.T) {
	h := newHarness(t)
	ts := turns(4)
	ts[0] = user("please use " + fakeKey() + " for the deploy")
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, ts)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.prompts[0], "AKIA") || !strings.Contains(h.prompts[0], omittedMark) {
		t.Fatal("the key reached the helper")
	}
	// ...and nowhere in what the review keeps.
	assertNoneContains(t, stateRoot(), "AKIA")
}

func assertNoneContains(t *testing.T, dir, needle string) {
	t.Helper()
	seen := 0
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		seen++
		if b, _ := os.ReadFile(p); strings.Contains(string(b), needle) {
			t.Errorf("%s contains %q", p, needle)
		}
		return nil
	})
	if seen == 0 {
		t.Errorf("no file under %s: the scan looked at nothing", dir)
	}
}

// One call and at most one retry; a reply that cannot be used spends the budget, and then the
// window is parked and never offered again.
func TestReviewRetriesOnceThenParksTheWindow(t *testing.T) {
	h := newHarness(t)
	h.replies = []func() (string, error){say("not json"), say("still not json")}
	r := h.reviewer()
	res, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.State != StateClaimed || res.Failure == "" || Watermark("sess-key") != 0 {
		t.Fatalf("first attempt: %+v %v wm %d", res, err, Watermark("sess-key"))
	}
	// Inside the backoff an automatic trigger waits; "Review now" does not.
	if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); !errors.Is(err, ErrBackoff) {
		t.Fatalf("err = %v, want ErrBackoff", err)
	}
	if h.calls != 1 {
		t.Fatalf("calls = %d during the backoff", h.calls)
	}
	res, err = r.Review(context.Background(), h.req(TriggerManual, turns(4)))
	if err != nil || res.State != StateFailed || Watermark("sess-key") != 4 {
		t.Fatalf("second attempt: %+v %v wm %d", res, err, Watermark("sess-key"))
	}
	run := onlyRun(t)
	if run.State != StateFailed || len(run.Attempts) != 2 || run.Attempts[0].Outcome != OutcomeMalformed || run.Failure == "" {
		t.Fatalf("run = %+v", run)
	}
	// Parked: more turns arrive, the parked window is not offered again, the new ones are.
	h.replies = []func() (string, error){say(`{"ops":[]}`)}
	res, err = r.Review(context.Background(), h.req(TriggerAuto, turns(8)))
	if err != nil || res.State != StateDone || h.calls != 3 {
		t.Fatalf("after parking: %+v %v calls %d", res, err, h.calls)
	}
	if !strings.Contains(h.prompts[2], "[5] user") || strings.Contains(h.prompts[2], "[1] user") {
		t.Error("the new window must start after the parked one")
	}
}

func TestReviewDoesNotRetryAReplyThatEchoedASecret(t *testing.T) {
	h := newHarness(t)
	leak := fmt.Sprintf(`{"ops":[{"op":"create","scope":"project","name":"x","type":"user","description":"d","body":"use %s","reason":"r","evidence":[2]}]}`, fakeKey())
	h.replies = []func() (string, error){say(leak)}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.State != StateFailed || h.calls != 1 || Watermark("sess-key") != 4 || len(Pending()) != 0 {
		t.Fatalf("%+v %v calls %d", res, err, h.calls)
	}
	assertNoneContains(t, stateRoot(), "AKIA")
	if strings.Contains(res.Failure, "AKIA") {
		t.Error("the failure text carries the value")
	}
}

func TestReviewCallErrorsSpendTheBudgetWithoutQuotingThem(t *testing.T) {
	h := newHarness(t)
	h.replies = []func() (string, error){fail(errors.New("boom PRIVATE-DETAIL")), fail(errors.New("boom PRIVATE-DETAIL"))}
	r := h.reviewer()
	if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil {
		t.Fatal(err)
	}
	res, err := r.Review(context.Background(), h.req(TriggerManual, turns(4)))
	if err != nil || res.State != StateFailed || h.calls != 2 {
		t.Fatalf("%+v %v calls %d", res, err, h.calls)
	}
	assertNoneContains(t, stateRoot(), "PRIVATE-DETAIL")
}

// Nothing was sent, so no attempt and no call are spent, and the window stays where it was.
func TestReviewWithoutADenyAllBackendSpendsNothing(t *testing.T) {
	h := newHarness(t)
	h.replies = []func() (string, error){fail(fmt.Errorf("%w: probe", chatx.ErrDenyAllUnavailable))}
	r := h.reviewer()
	if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("err = %v", err)
	}
	run := onlyRun(t)
	if run.State != StateClaimed || run.used() != 0 || run.Unsent != 1 || CallsToday(h.now) != 0 || Watermark("sess-key") != 0 {
		t.Fatalf("run %+v calls %d", run, CallsToday(h.now))
	}
	h.now = h.now.Add(2 * time.Minute)
	if res, err := r.Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil || res.State != StateDone {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestReviewDailyCapStopsTheCallsAndResetsWithTheDay(t *testing.T) {
	h := newHarness(t)
	if err := dailyStore.Write("daily", daily{Day: h.now.Format("2006-01-02"), Calls: DailyCallCap}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(4))); !errors.Is(err, ErrDailyCap) {
		t.Fatalf("err = %v", err)
	}
	if h.calls != 0 {
		t.Fatal("a call was made past the cap")
	}
	h.now = h.now.Add(24 * time.Hour)
	if res, err := h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(4))); err != nil || res.State != StateDone {
		t.Fatalf("next day: %+v %v", res, err)
	}
	if CallsToday(h.now) != 1 {
		t.Fatalf("calls today = %d", CallsToday(h.now))
	}
}

func TestReviewStopsAtAFullQueue(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < MaxPending; i++ {
		p := Proposal{ID: fmt.Sprintf("p%03d", i), Session: "s", Hash: fmt.Sprintf("h%03d", i), Op: Op{Op: OpCreate, Scope: "user", Name: "n", Body: "b"}}
		if _, err := enqueue(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v", err)
	}
	if h.calls != 0 || len(runs()) != 0 {
		t.Fatal("a full queue still claimed or called")
	}
}

// ---- stopping at every stage ----

func TestReviewResumesAClaimThatNeverCalled(t *testing.T) {
	h := newHarness(t)
	if !h.reviewUntilCrash(h.reviewer(), "claimed", h.req(TriggerAuto, turns(6))) {
		t.Fatal("did not stop at the claim")
	}
	if h.calls != 0 || onlyRun(t).State != StateClaimed {
		t.Fatalf("calls %d run %+v", h.calls, runs())
	}
	// A manual trigger after the restart resumes the SAME run; it does not claim another window.
	before := onlyRun(t).ID
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(6)))
	if err != nil || res.RunID != before || res.State != StateDone || h.calls != 1 || len(runs()) != 1 {
		t.Fatalf("%+v %v calls %d runs %d", res, err, h.calls, len(runs()))
	}
}

func TestReviewCountsADispatchedAttemptThatNeverAnswered(t *testing.T) {
	for name, tc := range map[string]struct {
		ledger func(LedgerQuery) LedgerHit
		cost   string
		spend  int
	}{
		"no usage row":  {nil, "unknown", 0},
		"a usage row":   {func(LedgerQuery) LedgerHit { return LedgerHit{Spend: 321, Call: "c1", Found: true} }, "linked", 321},
		"row not found": {func(LedgerQuery) LedgerHit { return LedgerHit{} }, "unknown", 0},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.ledger = tc.ledger
			if !h.reviewUntilCrash(h.reviewer(), "dispatched", h.req(TriggerAuto, turns(6))) {
				t.Fatal("did not stop at the dispatch")
			}
			if run := onlyRun(t); run.State != StateDispatched || run.Attempts[0].Outcome != OutcomeDispatched {
				t.Fatalf("run = %+v", run)
			}
			h.now = h.now.Add(10 * time.Minute)
			res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(6)))
			if err != nil || res.State != StateDone {
				t.Fatalf("%+v %v", res, err)
			}
			run := onlyRun(t)
			if len(run.Attempts) != 2 || run.Attempts[0].Outcome != OutcomeLost || run.Attempts[0].Cost != tc.cost || run.Attempts[0].Spend != tc.spend || run.Attempts[1].Outcome != OutcomeAnswered {
				t.Fatalf("attempts = %+v", run.Attempts)
			}
		})
	}
}

// The lost attempt used the retry: a second stop at the next dispatch leaves no budget, so the
// window is parked and the helper is not asked a third time.
func TestReviewDoesNotCallPastTheBudgetAfterARestart(t *testing.T) {
	h := newHarness(t)
	h.replies = []func() (string, error){say("not json")}
	r := h.reviewer()
	if _, err := r.Review(context.Background(), h.req(TriggerAuto, turns(6))); err != nil {
		t.Fatal(err)
	}
	if !h.reviewUntilCrash(h.reviewer(), "dispatched", h.req(TriggerManual, turns(6))) {
		t.Fatal("did not stop at the second dispatch")
	}
	calls := h.calls
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerManual, turns(6)))
	if err != nil || res.State != StateFailed || h.calls != calls || Watermark("sess-key") != 6 {
		t.Fatalf("%+v %v calls %d->%d", res, err, calls, h.calls)
	}
	if got := onlyRun(t).Attempts; len(got) != 2 || got[1].Outcome != OutcomeLost {
		t.Fatalf("attempts = %+v", got)
	}
}

// An answer that was validated and kept is never asked for again, and is queued exactly once even
// when the stop comes after some of the queueing.
func TestReviewQueuesAKeptAnswerWithoutAskingAgain(t *testing.T) {
	for _, stage := range []string{"answered", "enqueued"} {
		t.Run(stage, func(t *testing.T) {
			h := newHarness(t)
			h.replies = []func() (string, error){say(`{"ops":[` +
				`{"op":"create","scope":"project","name":"first-note","type":"user","description":"d","body":"b1","reason":"r","evidence":[2]},` +
				`{"op":"create","scope":"user","name":"second-note","type":"user","description":"d","body":"b2","reason":"r","evidence":[3]}]}`)}
			if !h.reviewUntilCrash(h.reviewer(), stage, h.req(TriggerAuto, turns(6))) {
				t.Fatalf("did not stop at %s", stage)
			}
			if h.calls != 1 {
				t.Fatalf("calls = %d", h.calls)
			}
			res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(6)))
			if err != nil || res.State != StateDone || h.calls != 1 || res.Queued != 2 {
				t.Fatalf("%+v %v calls %d", res, err, h.calls)
			}
			if ps := Pending(); len(ps) != 2 {
				t.Fatalf("pending = %d", len(ps))
			}
			if Watermark("sess-key") != 6 || onlyRun(t).State != StateDone {
				t.Fatal("the run did not end")
			}
			// A project-scoped proposal carries its project, a user-scoped one none.
			for _, p := range Pending() {
				if (p.Op.Scope == "project") != (p.ProjectID == "proj-1") {
					t.Errorf("proposal %s: scope %s project %q", p.Op.Name, p.Op.Scope, p.ProjectID)
				}
			}
		})
	}
}

// The transcript changed under an open run: its window cannot be rebuilt, so it ends rather than
// reviewing something else.
func TestReviewParksARunWhoseWindowIsGone(t *testing.T) {
	h := newHarness(t)
	if !h.reviewUntilCrash(h.reviewer(), "claimed", h.req(TriggerAuto, turns(6))) {
		t.Fatal("no stop")
	}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(3)))
	if err != nil || res.State != StateFailed || h.calls != 0 || Watermark("sess-key") != 6 {
		t.Fatalf("%+v %v calls %d wm %d", res, err, h.calls, Watermark("sess-key"))
	}
}

func TestReviewRequestsAreChecked(t *testing.T) {
	h := newHarness(t)
	for _, req := range []Request{
		{Session: "s", Trigger: TriggerAuto, Turns: turns(2), FirstNumber: 1},
		{Key: "k", Trigger: TriggerAuto, Turns: turns(2), FirstNumber: 1},
		{Key: "k", Session: "s", Trigger: "schedule", Turns: turns(2), FirstNumber: 1},
	} {
		if _, err := h.reviewer().Review(context.Background(), req); err == nil {
			t.Errorf("%+v was accepted", req)
		}
	}
	if h.calls != 0 {
		t.Fatal("a bad request made a call")
	}
}

// A queue that fills while a run is open stops the call, not just the claim.
func TestReviewStopsAnOpenRunWhenTheQueueFillsMeanwhile(t *testing.T) {
	h := newHarness(t)
	if !h.reviewUntilCrash(h.reviewer(), "claimed", h.req(TriggerAuto, turns(4))) {
		t.Fatal("no stop")
	}
	for i := 0; i < MaxPending; i++ {
		if _, err := enqueue(Proposal{ID: fmt.Sprintf("p%03d", i), Session: "s", Hash: fmt.Sprintf("h%03d", i), Op: Op{Op: OpCreate, Scope: "user", Name: "n", Body: "b"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); !errors.Is(err, ErrQueueFull) || h.calls != 0 {
		t.Fatalf("err = %v calls %d", err, h.calls)
	}
}

// The Agent stopping mid-call (the context ends) leaves the attempt recorded as dispatched, so
// the next start counts it; it is not recorded as a failure of the helper.
func TestReviewLeavesAnInterruptedCallForRecovery(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.replies = []func() (string, error){func() (string, error) { cancel(); return "", ctx.Err() }}
	if _, err := h.reviewer().Review(ctx, h.req(TriggerAuto, turns(4))); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if run := onlyRun(t); run.State != StateDispatched || run.Attempts[0].Outcome != OutcomeDispatched || Watermark("sess-key") != 0 {
		t.Fatalf("run = %+v", run)
	}
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || res.State != StateDone || onlyRun(t).Attempts[0].Outcome != OutcomeLost {
		t.Fatalf("%+v %v %+v", res, err, onlyRun(t).Attempts)
	}
}

// A window that was claimed and finished is not run again, even if the watermark write was lost.
func TestReviewDoesNotRerunAFinishedWindowWhoseWatermarkIsLost(t *testing.T) {
	h := newHarness(t)
	if _, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil {
		t.Fatal(err)
	}
	watermarkStore.Remove(keyHash("sess-key"))
	// Same turns again: the window is the same, its run is finished.
	res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4)))
	if err != nil || h.calls != 1 || Watermark("sess-key") != 4 || res.State != "empty" {
		t.Fatalf("%+v %v calls %d wm %d", res, err, h.calls, Watermark("sess-key"))
	}
}

// A proposal's identity includes its project: the same project-scoped idea in two projects is two
// proposals, while a user-scoped one is one idea wherever it comes from.
func TestQueueTreatsProjectScopedIdeasPerProject(t *testing.T) {
	h := newHarness(t)
	both := `{"ops":[` +
		`{"op":"create","scope":"project","name":"p-note","type":"user","description":"d","body":"same","reason":"r","evidence":[2]},` +
		`{"op":"create","scope":"user","name":"u-note","type":"user","description":"d","body":"same","reason":"r","evidence":[2]}]}`
	h.replies = []func() (string, error){say(both), say(both)}
	h.project = "proj-1"
	if res, err := h.reviewer().Review(context.Background(), h.req(TriggerAuto, turns(4))); err != nil || res.Queued != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	h.project = "proj-2"
	req := h.req(TriggerAuto, turns(4))
	req.Key, req.Session = "other-key", "other"
	res, err := h.reviewer().Review(context.Background(), req)
	if err != nil || res.Queued != 1 || res.Skipped != 1 {
		t.Fatalf("second project: %+v %v", res, err)
	}
}
