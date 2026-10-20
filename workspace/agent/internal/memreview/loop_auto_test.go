package memreview

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// autoSwitch makes the member's automatic-review switch a test can flip, shared by the loop and
// its Reviewer the way the real prefs are.
func autoSwitch(lh *loopHarness, on bool) *atomic.Bool {
	var sw atomic.Bool
	sw.Store(on)
	lh.loop.AutoEnabled = sw.Load
	lh.loop.Reviewer.AutoEnabled = sw.Load
	return &sw
}

func longTranscript() []transcript.Turn {
	var ts []transcript.Turn
	for i := 0; i < 90; i++ {
		ts = append(ts, user(fmt.Sprintf("t%02d ", i)+repeat("x", 1200)))
	}
	return ts
}

// With automatic review off the loop claims nothing, reads no transcript and leaves no run, while
// "Review now" on the same session still works; turning it on lets the next wake-up claim it.
func TestTickClaimsNothingWhileAutomaticReviewIsOff(t *testing.T) {
	lh := newLoopHarness(t)
	sw := autoSwitch(lh, false)
	c := lh.add("a", 10*time.Minute, assistantTurns(40))
	lh.tick()
	if lh.calls != 0 || len(runs()) != 0 || lh.src.readCount(c.Key) != 0 {
		t.Fatalf("calls %d runs %d reads %d: the loop must not touch a session while it is off", lh.calls, len(runs()), lh.src.readCount(c.Key))
	}
	if err := lh.loop.ReviewNow("a"); err != nil {
		t.Fatalf("manual review must not need the automatic switch: %v", err)
	}
	waitIdle(t, lh.loop, "a")
	if lh.calls != 1 || onlyRun(t).Trigger != TriggerManual {
		t.Fatalf("calls %d runs %+v", lh.calls, runs())
	}
	sw.Store(true)
	lh.add("b", 10*time.Minute, assistantTurns(40))
	lh.tick()
	if lh.calls != 2 {
		t.Fatalf("calls %d: switching it on should let the loop claim", lh.calls)
	}
}

// The review switch itself still governs the manual path.
func TestManualReviewStillNeedsTheReviewSwitch(t *testing.T) {
	lh := newLoopHarness(t)
	autoSwitch(lh, true)
	lh.add("a", 10*time.Minute, assistantTurns(40))
	lh.enabled = false
	if err := lh.loop.ReviewNow("a"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
}

// A run the loop claimed earlier is not resumed while automatic review is off.
func TestTickDoesNotResumeAnOpenRunWhileAutomaticReviewIsOff(t *testing.T) {
	lh := newLoopHarness(t)
	c := lh.add("a", 10*time.Minute, assistantTurns(40))
	if !lh.reviewUntilCrash(lh.reviewer(), "claimed", Request{Key: c.Key, Session: c.Name, Trigger: TriggerAuto, Turns: lh.src.turns[c.Key], FirstNumber: 1}) {
		t.Fatal("no stop")
	}
	lh.loop.Reviewer = lh.reviewer()
	sw := autoSwitch(lh, false)
	lh.tick()
	if lh.calls != 0 || onlyRun(t).State != StateClaimed {
		t.Fatalf("calls %d run %+v", lh.calls, runs())
	}
	sw.Store(true)
	lh.tick()
	if lh.calls != 1 || onlyRun(t).State != StateDone {
		t.Fatalf("calls %d run %+v", lh.calls, runs())
	}
}

// In-flight behaviour: a helper call already running when automatic review goes off is allowed to
// finish and its answer is queued, but the next window of the same trigger is not claimed.
func TestSwitchingAutomaticReviewOffLetsTheCallInFlightFinishAndClaimsNoMore(t *testing.T) {
	lh := newLoopHarness(t)
	sw := autoSwitch(lh, true)
	c := lh.add("long", 10*time.Minute, longTranscript())
	lh.replies = []func() (string, error){func() (string, error) {
		sw.Store(false) // the member turns it off while the first call runs
		return goodReply("kept"), nil
	}}
	lh.tick()
	if lh.calls != 1 {
		t.Fatalf("calls %d: no window after the switch went off", lh.calls)
	}
	w := Watermark(c.Key)
	if w == 0 || w >= 90 {
		t.Fatalf("watermark %d: the finished window counts, the rest stays unreviewed", w)
	}
	if got := pendingCount(); got != 1 {
		t.Fatalf("pending %d: the in-flight answer is queued like any other", got)
	}
	lh.tick()
	if lh.calls != 1 {
		t.Fatalf("calls %d: still off", lh.calls)
	}
}

// A call in flight that fails after the switch went off is not retried: the run waits as claimed.
func TestAFailureAfterTheSwitchWentOffIsNotRetried(t *testing.T) {
	lh := newLoopHarness(t)
	sw := autoSwitch(lh, true)
	lh.add("a", 10*time.Minute, assistantTurns(40))
	lh.replies = []func() (string, error){func() (string, error) {
		sw.Store(false)
		return "", errors.New("helper exited")
	}}
	lh.tick()
	lh.now = lh.now.Add(time.Hour) // past any backoff
	lh.tick()
	if lh.calls != 1 || onlyRun(t).State != StateClaimed {
		t.Fatalf("calls %d run %+v", lh.calls, runs())
	}
}

// The Reviewer asks the switch itself, before claiming, for an automatic review only.
func TestReviewerRefusesAnAutomaticReviewWhileTheSwitchIsOff(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	r.AutoEnabled = func() bool { return false }
	req := Request{Key: "k", Session: "s", Trigger: TriggerAuto, Turns: turns(4), FirstNumber: 1}
	if _, err := r.Review(t.Context(), req); !errors.Is(err, ErrDisabled) || h.calls != 0 || len(runs()) != 0 {
		t.Fatalf("auto: err %v calls %d runs %d", err, h.calls, len(runs()))
	}
	req.Trigger = TriggerManual
	if _, err := r.Review(t.Context(), req); err != nil || h.calls != 1 {
		t.Fatalf("manual: err %v calls %d", err, h.calls)
	}
}

// The switch going off while the memory is read (after the first check, before the call is
// recorded) stops the call: nothing is sent and the run stays claimed.
func TestSwitchGoingOffWhileTheSnapshotIsReadStartsNoCall(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	var on atomic.Bool
	on.Store(true)
	r.AutoEnabled = on.Load
	inner := r.Snapshot
	r.Snapshot = func(s string) (memoryx.ReviewSnapshot, error) {
		on.Store(false)
		return inner(s)
	}
	_, err := r.Review(t.Context(), Request{Key: "k", Session: "s", Trigger: TriggerAuto, Turns: turns(4), FirstNumber: 1})
	if !errors.Is(err, ErrDisabled) || h.calls != 0 || onlyRun(t).State != StateClaimed || len(onlyRun(t).Attempts) != 0 {
		t.Fatalf("err %v calls %d runs %+v", err, h.calls, runs())
	}
}

// The switch going off between the first check and the claim creates no run.
func TestSwitchGoingOffBeforeTheClaimCreatesNoRun(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	n := 0
	r.AutoEnabled = func() bool { n++; return n == 1 } // true only for the check at the top
	_, err := r.Review(t.Context(), Request{Key: "k", Session: "s", Trigger: TriggerAuto, Turns: turns(4), FirstNumber: 1})
	if !errors.Is(err, ErrDisabled) || h.calls != 0 || len(runs()) != 0 {
		t.Fatalf("err %v calls %d runs %d", err, h.calls, len(runs()))
	}
}

// A write of "off" that races a review at its dispatch boundary is ordered with it: either the
// dispatch passed first (the call is in flight and finishes) or the write did (nothing starts).
// The order is fixed by the Snapshot seam: the claim has passed, then the writer takes the gate and
// says so, and only then does the review move on to the dispatch boundary, where it must wait for
// the writer and see "off".
func TestSwitchWriteOrdersWithTheDispatchBoundary(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	var on atomic.Bool
	on.Store(true)
	var armed atomic.Bool // the writer holds the gate: the next check is the dispatch boundary's
	asked := make(chan struct{}, 1)
	r.AutoEnabled = func() bool {
		if armed.Load() {
			select {
			case asked <- struct{}{}:
			default:
			}
		}
		return on.Load()
	}
	held := make(chan struct{})
	wrote := make(chan struct{})
	inner := r.Snapshot
	r.Snapshot = func(s string) (memoryx.ReviewSnapshot, error) {
		go func() {
			defer close(wrote)
			_ = WithSwitchWrite(func() error {
				armed.Store(true)
				close(held) // the gate is ours; the review cannot pass its dispatch boundary yet
				// With the gate the review waits and never asks; the timeout only bounds that wait.
				// Without it the review asks at once and has read "on" before the write lands.
				select {
				case <-asked:
				case <-time.After(200 * time.Millisecond):
				}
				on.Store(false)
				return nil
			})
		}()
		<-held
		return inner(s)
	}
	_, err := r.Review(t.Context(), Request{Key: "k", Session: "s", Trigger: TriggerAuto, Turns: turns(4), FirstNumber: 1})
	<-wrote
	if !errors.Is(err, ErrDisabled) || h.calls != 0 {
		t.Fatalf("err %v calls %d: a dispatch must not pass a switch write that holds the gate", err, h.calls)
	}
}

// Once the dispatch passed the boundary the call is in flight and finishes, whatever the switch does.
func TestACallThatPassedTheDispatchBoundaryFinishes(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	var on atomic.Bool
	on.Store(true)
	r.AutoEnabled = on.Load
	r.hook = func(stage string) {
		if stage == "dispatched" {
			_ = WithSwitchWrite(func() error { on.Store(false); return nil })
		}
	}
	res, err := r.Review(t.Context(), Request{Key: "k", Session: "s", Trigger: TriggerAuto, Turns: turns(4), FirstNumber: 1})
	if err != nil || h.calls != 1 || res.State != StateDone {
		t.Fatalf("res %+v err %v calls %d", res, err, h.calls)
	}
}
