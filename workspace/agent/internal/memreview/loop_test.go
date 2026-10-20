package memreview

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

type fakeSource struct {
	mu    sync.Mutex
	cands []Candidate
	turns map[string][]transcript.Turn
	reads map[string]int
	err   error
}

func (f *fakeSource) Candidates() []Candidate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Candidate(nil), f.cands...)
}

func (f *fakeSource) Turns(c Candidate) ([]transcript.Turn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads == nil {
		f.reads = map[string]int{}
	}
	f.reads[c.Key]++
	if f.err != nil {
		return nil, f.err
	}
	return f.turns[c.Key], nil
}

func (f *fakeSource) readCount(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[key]
}

func assistantTurns(n int) []transcript.Turn {
	var ts []transcript.Turn
	for i := 1; i <= n; i++ {
		ts = append(ts, transcript.Turn{Role: "assistant", Parts: []transcript.Part{{Kind: "text", Text: fmt.Sprintf("work step %d", i)}}})
	}
	return ts
}

// loopHarness wires a Loop over the harness's Reviewer and a fake Source; the clock is the harness's.
type loopHarness struct {
	*harness
	src  *fakeSource
	loop *Loop
}

func newLoopHarness(t *testing.T) *loopHarness {
	t.Helper()
	h := newHarness(t)
	src := &fakeSource{turns: map[string][]transcript.Turn{}}
	lp := &Loop{Reviewer: h.reviewer(), Source: src, Enabled: func() bool { return h.enabled },
		Now: func() time.Time { return h.now }, Settle: DefaultSettle, Interval: time.Hour, Lookback: DefaultLookback,
		running: map[string]bool{}, wake: make(chan struct{}, 1)}
	// Whatever the test started is stopped and joined before the harness's own cleanups restore
	// HOME and CLAUDE_CONFIG_DIR (cleanups run last-in first-out): a goroutine left over would
	// write into the next test's state directory.
	t.Cleanup(func() { lp.Shutdown(30 * time.Second) })
	return &loopHarness{harness: h, src: src, loop: lp}
}

// add lists a session whose turn ended `ago` before the harness clock.
func (lh *loopHarness) add(name string, ago time.Duration, ts []transcript.Turn) Candidate {
	c := Candidate{Key: name + "-key", Name: name}
	if ago >= 0 {
		c.EndedAt = lh.now.Add(-ago)
	}
	lh.src.cands = append(lh.src.cands, c)
	lh.src.turns[c.Key] = ts
	return c
}

func (lh *loopHarness) tick() { lh.loop.Tick(context.Background()) }

// A turn is a row of the normalized transcript other than a compaction summary (docs/log/134
// section 1.1): a thinking-only row is one, even though the prompt the review builds leaves it out.
func TestCountNewCountsNormalizedRowsExceptCompaction(t *testing.T) {
	ts := append(turns(10), transcript.Turn{Role: "user", Compact: true, Text: "summary", Parts: []transcript.Part{{Kind: "text", Text: "summary"}}},
		transcript.Turn{Role: "assistant", Parts: []transcript.Part{{Kind: "thinking", Text: "hm"}}})
	if n, p := CountNew(ts, 1, 0); n != 11 || p != 5 {
		t.Fatalf("n=%d prompts=%d: want 11 turns (10 plus the thinking row) and 5 prompts", n, p)
	}
	if n, p := CountNew(ts, 1, 6); n != 5 || p != 2 {
		t.Fatalf("after the watermark: n=%d prompts=%d", n, p)
	}
}

// 20 ordinary rows and 10 thinking-only rows are 30 turns: an unattended session with no prompts
// reaches the confirmed threshold with them.
func TestTickCountsThinkingRowsTowardsTheThreshold(t *testing.T) {
	lh := newLoopHarness(t)
	ts := assistantTurns(20)
	for i := 0; i < 10; i++ {
		ts = append(ts, transcript.Turn{Role: "assistant", Parts: []transcript.Part{{Kind: "thinking", Text: "hm"}}})
	}
	// Turn 2 is the evidence the default reply cites; it is an ordinary row.
	lh.add("thinker", 10*time.Minute, ts)
	lh.tick()
	if lh.calls != 1 || Watermark("thinker-key") != 30 {
		t.Fatalf("calls %d watermark %d", lh.calls, Watermark("thinker-key"))
	}
}

// Only the window an open run already claimed is resumed unconditionally; what came after it waits
// for its own turn end, settle time and threshold.
func TestTickResumesOnlyTheClaimedWindow(t *testing.T) {
	lh := newLoopHarness(t)
	c := lh.add("a", 10*time.Minute, turns(2))
	if !lh.reviewUntilCrash(lh.reviewer(), "claimed", Request{Key: c.Key, Session: c.Name, Trigger: TriggerAuto, Turns: turns(2), FirstNumber: 1}) {
		t.Fatal("no stop")
	}
	// The session carried on: 40 more turns, and it is mid-turn again (no evidence).
	lh.src.turns[c.Key] = turns(42)
	lh.src.cands[0].EndedAt = time.Time{}
	lh.loop.Reviewer = lh.reviewer()
	lh.tick()
	if lh.calls != 1 || Watermark(c.Key) != 2 {
		t.Fatalf("calls %d watermark %d: only the claimed window may be resumed", lh.calls, Watermark(c.Key))
	}
	// Once it has settled and has enough new turns, the rest is reviewed on its own terms.
	lh.src.cands[0].EndedAt = lh.now.Add(-10 * time.Minute)
	lh.replies = []func() (string, error){say(`{"ops":[]}`)}
	lh.tick()
	if lh.calls != 2 || Watermark(c.Key) != 42 {
		t.Fatalf("calls %d watermark %d", lh.calls, Watermark(c.Key))
	}
}

func TestTickRunsTheTriggerRules(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("settled-many", 10*time.Minute, assistantTurns(30)) // 30 turns, no prompt at all
	lh.add("settled-prompts", 10*time.Minute, turns(5))        // 3 user prompts
	lh.add("settled-few", 10*time.Minute, turns(4))            // 4 turns, 2 prompts: below both
	lh.add("settled-29", 10*time.Minute, assistantTurns(29))   // one short of the turn count
	lh.add("still-warm", 2*time.Minute, assistantTurns(40))    // inside the settle time
	lh.add("mid-turn", -1, assistantTurns(40))                 // no turn-end evidence
	lh.add("ancient", 72*time.Hour, assistantTurns(40))        // beyond the look-back
	lh.src.turns["settled-prompts-key"] = turns(5)
	lh.tick()
	got := map[string]bool{}
	for _, r := range runs() {
		got[r.Session] = true
	}
	for _, want := range []string{"settled-many", "settled-prompts"} {
		if !got[want] {
			t.Errorf("%s was not reviewed", want)
		}
	}
	for _, not := range []string{"settled-few", "settled-29", "still-warm", "mid-turn", "ancient"} {
		if got[not] {
			t.Errorf("%s must not be reviewed", not)
		}
	}
	// Sessions with no evidence, or inside the settle time, are not even read.
	for _, name := range []string{"still-warm-key", "mid-turn-key", "ancient-key"} {
		if lh.src.readCount(name) != 0 {
			t.Errorf("%s: the transcript was parsed before the cheap gates passed", name)
		}
	}
}

// A turn end that was looked at is not parsed again; a newer one is looked at.
func TestTickDoesNotLookAtTheSameTurnEndTwice(t *testing.T) {
	lh := newLoopHarness(t)
	c := lh.add("quiet", 10*time.Minute, turns(4))
	lh.tick()
	lh.tick()
	if n := lh.src.readCount(c.Key); n != 1 {
		t.Fatalf("the transcript was read %d times for one turn end", n)
	}
	// ...also after a restart (a new Loop over the same state).
	lh2 := &Loop{Reviewer: lh.loop.Reviewer, Source: lh.src, Enabled: lh.loop.Enabled, Now: lh.loop.Now, Settle: DefaultSettle,
		Interval: time.Hour, Lookback: DefaultLookback, running: map[string]bool{}, wake: make(chan struct{}, 1)}
	lh2.Tick(context.Background())
	if n := lh.src.readCount(c.Key); n != 1 {
		t.Fatalf("after a restart the transcript was read %d times", n)
	}
	// A new turn end with enough new turns is reviewed.
	lh.src.cands[0].EndedAt = lh.now.Add(-6 * time.Minute)
	lh.src.turns[c.Key] = assistantTurns(40)
	lh.tick()
	if lh.calls != 1 || Watermark(c.Key) != 40 {
		t.Fatalf("calls %d watermark %d", lh.calls, Watermark(c.Key))
	}
	// A session that vanished loses its record.
	lh.src.cands = nil
	lh.tick()
	if n := len(examinedStore.keys()); n != 0 {
		t.Fatalf("%d examined records outlive their sessions", n)
	}
}

func TestTickNeverRunsWhileSwitchedOff(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("a", 10*time.Minute, assistantTurns(40))
	lh.enabled = false
	lh.tick()
	if lh.calls != 0 || len(runs()) != 0 || lh.src.readCount("a-key") != 0 {
		t.Fatalf("calls %d runs %d reads %d", lh.calls, len(runs()), lh.src.readCount("a-key"))
	}
	// Switched off between two sessions: the second is not touched.
	lh.enabled = true
	lh.add("b", 11*time.Minute, assistantTurns(40))
	n := 0
	lh.loop.Enabled = func() bool { n++; return n <= 2 } // the wake-up's own check and the first session's
	lh.tick()
	if len(runs()) != 1 {
		t.Fatalf("%d runs after the switch went off", len(runs()))
	}
}

// An open run is resumed whatever the thresholds or the evidence say.
func TestTickResumesAnOpenRunWithoutFreshEvidence(t *testing.T) {
	lh := newLoopHarness(t)
	c := lh.add("a", 10*time.Minute, assistantTurns(40))
	if !lh.reviewUntilCrash(lh.reviewer(), "claimed", Request{Key: c.Key, Session: c.Name, Trigger: TriggerAuto, Turns: lh.src.turns[c.Key], FirstNumber: 1}) {
		t.Fatal("no stop")
	}
	// Now mid-turn again, no evidence: the claimed window is still run.
	lh.src.cands[0].EndedAt = time.Time{}
	lh.loop.Reviewer = lh.reviewer()
	lh.tick()
	if lh.calls != 1 || onlyRun(t).State != StateDone {
		t.Fatalf("calls %d run %+v", lh.calls, runs())
	}
}

func TestTickStopsAtAGlobalLimit(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("a", 20*time.Minute, assistantTurns(40))
	lh.add("b", 10*time.Minute, assistantTurns(40))
	lh.replies = []func() (string, error){fail(fmt.Errorf("%w: probe", chatx.ErrDenyAllUnavailable))}
	lh.tick()
	if lh.calls != 1 {
		t.Fatalf("calls = %d: with no usable backend the rest of the wake-up must wait", lh.calls)
	}
	if len(runs()) != 1 {
		t.Fatalf("runs = %d", len(runs()))
	}
}

// A long transcript is worked through window by window for one trigger.
func TestTickWorksThroughSeveralWindows(t *testing.T) {
	lh := newLoopHarness(t)
	var ts []transcript.Turn
	for i := 0; i < 90; i++ {
		ts = append(ts, user(fmt.Sprintf("t%02d ", i)+fmt.Sprint(i)+string(make([]rune, 0))+repeat("x", 1200)))
	}
	c := lh.add("long", 10*time.Minute, ts)
	for i := 0; i < 8; i++ {
		lh.replies = append(lh.replies, say(`{"ops":[]}`))
	}
	lh.tick()
	if lh.calls < 2 || Watermark(c.Key) != 90 {
		t.Fatalf("calls %d watermark %d", lh.calls, Watermark(c.Key))
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func waitIdle(t *testing.T, lp *Loop, name string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for lp.Running(name) {
		if time.Now().After(deadline) {
			t.Fatal("the review did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReviewNowIgnoresTheTriggerRulesAndAnswersAtOnce(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("fresh", 5*time.Second, turns(4)) // inside the settle time, below every threshold
	lh.src.cands = append(lh.src.cands, Candidate{Key: "mid-key", Name: "mid"})
	lh.src.turns["mid-key"] = turns(2)
	if err := lh.loop.ReviewNow("fresh"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, lh.loop, "fresh")
	info := lh.loop.Status("fresh")
	if info.Last == nil || info.Last.State != StateDone || info.Last.Trigger != TriggerManual || info.Last.Queued != 1 || info.Running {
		t.Fatalf("status = %+v", info)
	}
	if len(lh.tags) != 1 || lh.tags[0].Trigger != "manual" || lh.tags[0].Ref != "fresh" {
		t.Fatalf("tags = %+v", lh.tags)
	}
	// A session that is mid-turn can be reviewed on request too.
	if err := lh.loop.ReviewNow("mid"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, lh.loop, "mid")
	if lh.calls != 2 {
		t.Fatalf("calls = %d", lh.calls)
	}
}

func TestReviewNowRefusals(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("a", 5*time.Second, turns(4))
	if err := lh.loop.ReviewNow("nobody"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v", err)
	}
	lh.enabled = false
	if err := lh.loop.ReviewNow("a"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	if lh.calls != 0 || len(runs()) != 0 {
		t.Fatal("a refused request did something")
	}
	// Busy: one review in flight in the Agent; the same session twice.
	lh.enabled = true
	release := make(chan struct{})
	started := make(chan struct{})
	lh.loop.Reviewer.Call = func(ctx context.Context, persona, prompt string) (string, error) {
		close(started)
		<-release
		return `{"ops":[]}`, nil
	}
	if err := lh.loop.ReviewNow("a"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := lh.loop.ReviewNow("a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("the same session twice: %v", err)
	}
	lh.add("b", 5*time.Second, turns(4))
	if err := lh.loop.ReviewNow("b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("another session meanwhile: %v", err)
	}
	if !lh.loop.Status("a").Running {
		t.Fatal("status does not show the review running")
	}
	close(release)
	waitIdle(t, lh.loop, "a")
}

// A manual review that cannot go on says why in Status instead of vanishing.
func TestReviewNowReportsWhyItStopped(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("a", 5*time.Second, turns(4))
	lh.replies = []func() (string, error){fail(fmt.Errorf("%w: probe", chatx.ErrDenyAllUnavailable))}
	if err := lh.loop.ReviewNow("a"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, lh.loop, "a")
	if m := lh.loop.Status("a").Manual; m == nil || m.Outcome != "no_backend" {
		t.Fatalf("manual = %+v", m)
	}
	// ...and a later request clears it.
	h := lh
	h.now = h.now.Add(time.Hour)
	if err := lh.loop.ReviewNow("a"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, lh.loop, "a")
	if m := lh.loop.Status("a").Manual; m == nil || m.Outcome != "done" {
		t.Fatalf("manual = %+v after a success", m)
	}
}

func TestStartTicksAtOnceAndStopsWithItsContext(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("a", 10*time.Minute, assistantTurns(40))
	ctx, cancel := context.WithCancel(context.Background())
	lh.loop.Interval = 20 * time.Millisecond
	lh.loop.Start(ctx)
	deadline := time.Now().Add(20 * time.Second)
	for len(runs()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the loop never ran a wake-up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	// The loop's goroutine is joined, not waited for by the clock: Shutdown returns when it is done.
	lh.loop.Shutdown(30 * time.Second)
	if lh.loop.Running("a") {
		t.Fatal("a review is still running after Shutdown")
	}
	// Wake after the loop has ended must not block.
	lh.loop.Wake()
	lh.loop.Wake()
}

// "Review now" on a session with nothing new still answers: the outcome is something to wait for.
func TestReviewNowSaysWhenThereIsNothingNew(t *testing.T) {
	lh := newLoopHarness(t)
	lh.add("a", 5*time.Second, turns(4))
	if err := lh.loop.ReviewNow("a"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, lh.loop, "a")
	first := lh.loop.Status("a").Manual
	if first == nil || first.Outcome != "done" {
		t.Fatalf("first = %+v", first)
	}
	lh.now = lh.now.Add(time.Minute)
	if err := lh.loop.ReviewNow("a"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, lh.loop, "a")
	second := lh.loop.Status("a").Manual
	if second == nil || second.Outcome != "empty" || !second.At.After(first.At) {
		t.Fatalf("second = %+v (first %+v)", second, first)
	}
	if lh.calls != 1 {
		t.Fatalf("calls = %d", lh.calls)
	}
	// An unreadable transcript answers too.
	lh.src.err = errors.New("gone")
	lh.now = lh.now.Add(time.Minute)
	if err := lh.loop.ReviewNow("a"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, lh.loop, "a")
	if m := lh.loop.Status("a").Manual; m == nil || m.Outcome != "unreadable" {
		t.Fatalf("manual = %+v", m)
	}
}

// Shutdown reaches the helper call, whichever way the review started: the CLI is killed rather
// than left running when the Agent exits, nothing new starts, and the interrupted attempt stays
// `dispatched` for the next start to settle.
func TestShutdownCancelsTheHelperCallOfBothPaths(t *testing.T) {
	for _, path := range []string{"manual", "auto"} {
		t.Run(path, func(t *testing.T) {
			lh := newLoopHarness(t)
			c := lh.add("a", 10*time.Minute, assistantTurns(40))
			started := make(chan struct{})
			cancelled := make(chan error, 1)
			lh.loop.Reviewer.Call = func(ctx context.Context, persona, prompt string) (string, error) {
				close(started)
				<-ctx.Done()
				cancelled <- ctx.Err()
				return "", ctx.Err()
			}
			if path == "manual" {
				if err := lh.loop.ReviewNow("a"); err != nil {
					t.Fatal(err)
				}
			} else {
				lh.loop.Interval = time.Hour
				lh.loop.Start(context.Background())
			}
			<-started
			lh.loop.Shutdown(5 * time.Second)
			select {
			case err := <-cancelled:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("the call ended with %v", err)
				}
			default:
				t.Fatal("Shutdown returned before the helper call was cancelled")
			}
			if lh.loop.Running("a") {
				t.Fatal("a review is still marked running after Shutdown")
			}
			if run := onlyRun(t); run.State != StateDispatched || run.Attempts[0].Outcome != OutcomeDispatched {
				t.Fatalf("run = %+v", run)
			}
			// Nothing new starts.
			if err := lh.loop.ReviewNow("a"); !errors.Is(err, ErrShuttingDown) {
				t.Fatalf("ReviewNow after Shutdown: %v", err)
			}
			before := len(runs())
			lh.loop.Tick(context.Background())
			if len(runs()) != before {
				t.Fatal("a wake-up after Shutdown started work")
			}
			_ = c
		})
	}
}

// A manual review over several windows reports the total, not the last run's: a first window that
// queued a proposal followed by an empty one is not "nothing worth keeping", and a refusal part way
// keeps what was already queued.
func TestManualReviewTotalsEveryWindow(t *testing.T) {
	long := func() []transcript.Turn {
		var ts []transcript.Turn
		for i := 0; i < 50; i++ {
			ts = append(ts, user(fmt.Sprintf("t%02d ", i)+repeat("x", 1200)))
		}
		return ts
	}
	late := `{"ops":[{"op":"create","scope":"project","name":"late-note","type":"user","description":"d","body":"b","reason":"r","evidence":[50]}]}`
	for name, tc := range map[string]struct {
		replies []func() (string, error)
		want    ManualInfo
	}{
		"first window queues, second is empty": {
			[]func() (string, error){say(goodReply("early-note")), say(`{"ops":[]}`)},
			ManualInfo{Outcome: "done", Windows: 2, Queued: 1},
		},
		"only the second window queues": {
			[]func() (string, error){say(`{"ops":[]}`), say(late)},
			ManualInfo{Outcome: "done", Windows: 2, Queued: 1},
		},
		"a refusal in the second window keeps the first's proposal": {
			[]func() (string, error){say(goodReply("early-note")), fail(fmt.Errorf("%w: probe", chatx.ErrDenyAllUnavailable))},
			ManualInfo{Outcome: "no_backend", Windows: 1, Queued: 1},
		},
		"a window that gets nothing usable is counted": {
			[]func() (string, error){say(goodReply("early-note")), say("not json")},
			ManualInfo{Outcome: "done", Windows: 2, Queued: 1, Failed: 1},
		},
	} {
		t.Run(name, func(t *testing.T) {
			lh := newLoopHarness(t)
			lh.add("long", 5*time.Second, long())
			lh.replies = tc.replies
			if err := lh.loop.ReviewNow("long"); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, lh.loop, "long")
			got := lh.loop.Status("long").Manual
			if got == nil {
				t.Fatal("no manual outcome")
			}
			got.At = time.Time{}
			if *got != tc.want {
				t.Fatalf("manual = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

// Shutdown returns only once the loop's goroutine is done with its wake-up, so a test (or the
// Agent) never has work running past it.
type slowSource struct {
	fakeSource
	entered chan struct{}
	release chan struct{}
	done    atomic.Bool
}

func (s *slowSource) Candidates() []Candidate {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	s.done.Store(true)
	return nil
}

func TestShutdownWaitsForTheWakeUpInFlight(t *testing.T) {
	lh := newLoopHarness(t)
	src := &slowSource{entered: make(chan struct{}, 1), release: make(chan struct{})}
	lh.loop.Source = src
	lh.loop.Start(context.Background())
	<-src.entered
	// With a budget too short for the stuck wake-up, Shutdown gives up and says nothing is joined.
	lh.loop.Shutdown(20 * time.Millisecond)
	if src.done.Load() {
		t.Fatal("the wake-up finished before it was released")
	}
	// With room, it waits for the wake-up to finish.
	go func() { time.Sleep(50 * time.Millisecond); close(src.release) }()
	lh.loop.Shutdown(10 * time.Second)
	if !src.done.Load() {
		t.Fatal("Shutdown returned while the wake-up was still running")
	}
}
