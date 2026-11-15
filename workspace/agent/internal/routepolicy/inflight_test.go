package routepolicy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// A tracked call is cancelled, with ErrCancelled as the cause, exactly when a restricting policy
// that does not allow its route is taken; a route not named yet counts as not allowed. A policy
// that restricts nothing, or allows the route, cancels nothing, and a call that is done is no
// longer tracked.
func TestTrackedCallsUnderAPolicyChange(t *testing.T) {
	allowed, _ := Track(context.Background(), "anthropic-api")
	forbidden, _ := Track(context.Background(), "anthropic-subscription")
	unnamed, _ := Track(context.Background(), "")
	named, nc := Track(context.Background(), "")
	nc.SetRoute("anthropic-api")
	finished, fc := Track(context.Background(), "anthropic-subscription")
	fc.Done()
	if CallFrom(named) != nc {
		t.Error("CallFrom does not find the tracked call")
	}

	cancelForbidden(Policy{Revision: 1, Allowed: []string{}})
	for name, ctx := range map[string]context.Context{"allowed": allowed, "forbidden": forbidden, "unnamed": unnamed} {
		if ctx.Err() != nil {
			t.Errorf("%s: cancelled by a policy that restricts nothing", name)
		}
	}

	cancelForbidden(Policy{Revision: 2, Restricted: true, Allowed: []string{"anthropic-api"}})
	for name, ctx := range map[string]context.Context{"forbidden": forbidden, "unnamed": unnamed} {
		if !Cancelled(ctx) || !errors.Is(context.Cause(ctx), ErrCancelled) {
			t.Errorf("%s: not cancelled by the policy (%v)", name, context.Cause(ctx))
		}
	}
	for name, ctx := range map[string]context.Context{"allowed": allowed, "named on an allowed route": named} {
		if ctx.Err() != nil {
			t.Errorf("%s: cancelled", name)
		}
	}
	if Cancelled(finished) {
		t.Error("a call that was done was cancelled by the policy")
	}
}

// The listener is wired: a store taking a restricting policy cancels a tracked call on a route it
// forbids, and WaitListeners returns only once that is done.
func TestTakingAPolicyCancelsTrackedCalls(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "rp.json"), "")
	ctx, c := Track(context.Background(), "chatgpt-subscription")
	defer c.Done()
	if _, err := s.Accept(Policy{Revision: 5, Restricted: true, Allowed: []string{"openai-api"}}); err != nil {
		t.Fatal(err)
	}
	WaitListeners()
	if !Cancelled(ctx) {
		t.Error("the tracked call on a forbidden route kept running")
	}
}
