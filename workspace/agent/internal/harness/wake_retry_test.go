package harness

// Reproduces the 2026-09-21 live finding: a session's first turn against a stopped engine
// aborted after ~45 seconds (control-plane's own plain-path hold, engineManagedPlainHoldSeconds)
// instead of waiting through ADR 0093's measured 3.5-7 minute real cold start. A live cold engine
// is hard to arrange on demand, so this pins the fix against a fake Client that reproduces both
// refusal shapes actually seen: the exact engine_unavailable body /props answered (a
// Service-Connect DNS lookup failure — control-plane/engine_gateway.go's props()) and the
// ordinary engine_waking retry the plain/streamed request paths answer with.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// wakingClient answers InputTokens with the scripted failures in order, then succeeds; Send
// always succeeds with turn. Concurrency-safe because retryEngineWake calls fn synchronously but
// the counter is still read by the test's own assertions afterward.
type wakingClient struct {
	mu       sync.Mutex
	fails    []error
	attempts int
	turn     Turn
}

func (c *wakingClient) Send(context.Context, []Message, []ToolDef) (Turn, error) {
	return c.turn, nil
}

func (c *wakingClient) InputTokens(context.Context, []Message, []ToolDef) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.attempts
	c.attempts++
	if i < len(c.fails) {
		return 0, c.fails[i]
	}
	return 10, nil
}

// withShortWakeRetryInterval shrinks the pause between wake retries for the duration of a test,
// so a test exercising several retries does not actually wait engineWakeRetryInterval's real
// value between each.
func withShortWakeRetryInterval(t *testing.T) {
	t.Helper()
	origInterval, origBudget := engineWakeRetryInterval, engineWakeRetryBudget
	engineWakeRetryInterval = time.Millisecond
	t.Cleanup(func() { engineWakeRetryInterval, engineWakeRetryBudget = origInterval, origBudget })
}

func TestRunWaitsThroughEngineUnavailableThenEngineWakingInsteadOfAborting(t *testing.T) {
	withShortWakeRetryInterval(t)

	client := &wakingClient{
		fails: []error{
			// The exact body observed live from GET /engine/llm/props against a stopped box —
			// see engineWakeRetryable's own doc comment for why this, and not just
			// engine_waking, has to be treated as a wake in progress.
			&EngineError{Kind: EngineUnavailable, Message: `the engine did not answer /props: Get "http://llm.af.internal:8080/props": dial tcp: lookup llm.af.internal on 10.20.0.2:53: no such host`},
			&EngineError{Kind: EngineWaking, Message: "the fleet's own inference engine is starting; retry"},
		},
		turn: Turn{Content: "hello"},
	}
	reg := NewRegistry()
	rt := &Runtime{Cwd: t.TempDir()}

	res, err := Run(context.Background(), client, reg, rt, nil)
	if err != nil {
		t.Fatalf("Run aborted instead of waiting through the wake: %v", err)
	}
	if res.Final.Content != "hello" {
		t.Fatalf("Final.Content = %q, want %q", res.Final.Content, "hello")
	}
	if client.attempts != 3 {
		t.Fatalf("InputTokens called %d times, want 3 (2 failures + 1 success)", client.attempts)
	}
}

// A genuine refusal — "no enabled model" is EngineUnavailable with no network-reachability
// phrasing in it — must still fail on the first attempt. Retrying it would turn a clear,
// administrative refusal into an effectively infinite wait (types.go's own Retryable doc
// comment), which is exactly the failure mode engineWakeRetryable's message match is written to
// avoid falling into.
func TestRunFailsFastOnGenuineEngineUnavailable(t *testing.T) {
	withShortWakeRetryInterval(t)

	client := &wakingClient{
		fails: []error{
			&EngineError{Kind: EngineUnavailable, Message: "this engine has no enabled model — an administrator has to select one"},
		},
		turn: Turn{Content: "hello"},
	}
	reg := NewRegistry()
	rt := &Runtime{Cwd: t.TempDir()}

	_, err := Run(context.Background(), client, reg, rt, nil)
	var ee *EngineError
	if !errors.As(err, &ee) || ee.Kind != EngineUnavailable {
		t.Fatalf("err = %v, want an un-retried EngineUnavailable", err)
	}
	if client.attempts != 1 {
		t.Fatalf("InputTokens called %d times, want 1 (no retry)", client.attempts)
	}
}

func TestEngineWakeRetryableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not an EngineError", errors.New("boom"), false},
		{"engine_waking", &EngineError{Kind: EngineWaking, Message: "starting"}, true},
		{"engine_off", &EngineError{Kind: EngineOff, Message: "switched off"}, false},
		{"engine_unavailable no model", &EngineError{Kind: EngineUnavailable,
			Message: "this engine has no enabled model — an administrator has to select one"}, false},
		{"engine_unavailable dns lookup", &EngineError{Kind: EngineUnavailable,
			Message: `the engine did not answer /props: Get "http://x:8080/props": dial tcp: lookup x on 10.0.0.2:53: no such host`}, true},
		{"engine_unavailable connection refused", &EngineError{Kind: EngineUnavailable,
			Message: "the fleet's own inference engine did not come up in time: connection refused"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := engineWakeRetryable(tc.err); got != tc.want {
				t.Errorf("engineWakeRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// retryEngineWake must give up once its budget is spent rather than retrying forever — a
// permanently unreachable engine (a real outage, not a wake) still has to surface as a failure
// eventually.
func TestRetryEngineWakeGivesUpAfterBudget(t *testing.T) {
	withShortWakeRetryInterval(t)
	engineWakeRetryBudget = 5 * time.Millisecond

	calls := 0
	err := retryEngineWake(context.Background(), func() error {
		calls++
		return &EngineError{Kind: EngineWaking, Message: "still starting"}
	})
	var ee *EngineError
	if !errors.As(err, &ee) || ee.Kind != EngineWaking {
		t.Fatalf("err = %v, want an EngineWaking error once the budget is spent", err)
	}
	if calls < 2 {
		t.Fatalf("fn called %d times, want at least 2 (budget must allow more than one attempt)", calls)
	}
}

// An interrupt (the turn's own ctx cancelled) must stop the wait immediately rather than
// spinning to the budget — Interrupt() cancels this same context for every other blocking point
// in this package (waitInteraction, a running tool), and the wake wait is no exception.
func TestRetryEngineWakeStopsOnContextCancellation(t *testing.T) {
	withShortWakeRetryInterval(t)
	engineWakeRetryInterval = time.Hour // would hang the test if cancellation did not cut it short

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- retryEngineWake(ctx, func() error {
			calls++
			return &EngineError{Kind: EngineWaking, Message: "still starting"}
		})
	}()
	// Let the first attempt happen and enter its retry sleep before cancelling.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		var ee *EngineError
		if !errors.As(err, &ee) {
			t.Fatalf("err = %v, want the last EngineError returned promptly on cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retryEngineWake did not return after its context was cancelled")
	}
}
