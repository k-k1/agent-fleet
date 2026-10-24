package memreview

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// A helper call that never returns must not hold the Agent's one review slot: the call is cut at
// CallTimeout, the attempt is recorded as failed, and the run ends as failed with a visible reason
// once its attempt budget is spent.
func TestReviewCutsAHungHelperCallAndFreesTheSlot(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	r.CallTimeout = 50 * time.Millisecond
	r.Call = func(ctx context.Context, persona, prompt string) (string, error) {
		h.calls++
		<-ctx.Done() // a hung CLI: only the deadline ends it
		return "", ctx.Err()
	}
	done := make(chan struct{})
	var res Result
	var err error
	go func() {
		defer close(done)
		res, err = r.Review(context.Background(), h.req(TriggerManual, turns(4)))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Review is still blocked on the hung call")
	}
	if err != nil || res.State != StateClaimed || res.Failure != FailureCallTimeout {
		t.Fatalf("res %+v err %v, want a retry with the timeout as the reason", res, err)
	}
	if inflight.Load() {
		t.Fatal("the in-flight slot was not released")
	}
	// The slot is free: the next trigger runs, spends the last attempt and parks the run.
	res, err = r.Review(context.Background(), h.req(TriggerManual, turns(4)))
	if err != nil || res.State != StateFailed || res.Failure != FailureCallTimeout {
		t.Fatalf("res %+v err %v, want a failed run that names the timeout", res, err)
	}
	if h.calls != MaxAttempts {
		t.Fatalf("calls = %d, want %d", h.calls, MaxAttempts)
	}
	rs := runs()
	if len(rs) != 1 || rs[0].Failure != FailureCallTimeout || rs[0].Attempts[0].Outcome != OutcomeFailed {
		t.Fatalf("the record does not show the timeout: %+v", rs)
	}
}

// The deadline is per window, not per trigger: a review whose windows each fit the timeout but
// together exceed it is not cut.
func TestReviewTimeoutIsPerWindowNotPerTrigger(t *testing.T) {
	h := newHarness(t)
	var ts []transcript.Turn
	for i := 0; i < 90; i++ {
		ts = append(ts, user(strings.Repeat(fmt.Sprintf("t%02d ", i), 300)))
	}
	r := h.reviewer()
	r.CallTimeout = 400 * time.Millisecond
	var spent time.Duration
	r.Call = func(ctx context.Context, persona, prompt string) (string, error) {
		h.calls++
		select {
		case <-time.After(150 * time.Millisecond):
			spent += 150 * time.Millisecond
			return `{"ops":[]}`, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	for i := 0; i < 20; i++ {
		res, err := r.Review(context.Background(), h.req(TriggerManual, ts))
		if err != nil || res.State != StateDone {
			t.Fatalf("window %d: res %+v err %v", i, res, err)
		}
		if !res.More {
			break
		}
	}
	if h.calls < 3 || spent <= r.CallTimeout {
		t.Fatalf("calls %d, spent %s: the test did not exceed one timeout across windows", h.calls, spent)
	}
}

// The caller's own stop is still a stop, not a timeout: the record stays "dispatched".
func TestReviewStopIsNotMistakenForATimeout(t *testing.T) {
	h := newHarness(t)
	r := h.reviewer()
	ctx, cancel := context.WithCancel(context.Background())
	r.Call = func(c context.Context, persona, prompt string) (string, error) {
		cancel()
		<-c.Done()
		return "", c.Err()
	}
	_, err := r.Review(ctx, h.req(TriggerManual, turns(4)))
	if err == nil {
		t.Fatal("a stopped review reported no error")
	}
	if rs := runs(); len(rs) != 1 || rs[0].State != StateDispatched {
		t.Fatalf("runs = %+v, want one dispatched run", rs)
	}
}
