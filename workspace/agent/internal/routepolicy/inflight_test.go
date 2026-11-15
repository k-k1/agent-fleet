package routepolicy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// holdPolicy makes Active a store holding p, settling the listeners first and on restore.
func holdPolicy(t *testing.T, p Policy) *Store {
	t.Helper()
	s := Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	WaitListeners()
	old := Active
	Active = func() *Store { return s }
	t.Cleanup(func() {
		WaitListeners()
		Active = old
	})
	return s
}

// The listener cancels a tracked call, with ErrCancelled as the cause, exactly when the policy held
// restricts and does not allow its route, an unnamed route counting as not allowed. A policy that
// restricts nothing cancels nothing, and a call that is done is no longer tracked.
func TestTrackedCallsUnderAPolicyChange(t *testing.T) {
	holdPolicy(t, Policy{Revision: 1, Allowed: []string{}})
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
	s := holdPolicy(t, Policy{Revision: 1, Allowed: []string{}})
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

// A tighter policy taken between a caller's own check and Track is not missed: the listener's pass
// may be over, so Track judges the call against the policy held now. SetRoute does the same for a
// route named later. An unnamed route is judged once it is named.
func TestTrackAfterATighteningIsCancelled(t *testing.T) {
	s := holdPolicy(t, Policy{Revision: 1, Restricted: true, Allowed: []string{"anthropic-api", "openai-api"}})
	if _, err := s.Accept(Policy{Revision: 2, Restricted: true, Allowed: []string{"openai-api"}}); err != nil {
		t.Fatal(err)
	}
	WaitListeners()
	ctx, c := Track(context.Background(), "anthropic-api")
	defer c.Done()
	if !Cancelled(ctx) {
		t.Error("a call registered after the tightening ran on a forbidden route")
	}
	ok, oc := Track(context.Background(), "openai-api")
	defer oc.Done()
	later, lc := Track(context.Background(), "")
	defer lc.Done()
	if ok.Err() != nil || later.Err() != nil {
		t.Fatal("an allowed or unnamed call was cancelled at registration")
	}
	lc.SetRoute("anthropic-api")
	if !Cancelled(later) {
		t.Error("a route named forbidden after registration was not cancelled")
	}
}

// Listeners run on their own goroutines, so a tightening's call can run after a later loosening:
// each reads the policy its store holds when it runs, and so never cancels a call the loosening
// allows, one registered after it included.
func TestADelayedTighteningDoesNotUndoALoosening(t *testing.T) {
	s := holdPolicy(t, Policy{Revision: 1, Restricted: true, Allowed: []string{"anthropic-api", "openai-api"}})
	gate := make(chan struct{})
	beforeListener = func() { <-gate }
	t.Cleanup(func() { WaitListeners(); beforeListener = nil })
	if _, err := s.Accept(Policy{Revision: 2, Restricted: true, Allowed: []string{"openai-api"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(Policy{Revision: 3, Restricted: true, Allowed: []string{"anthropic-api", "openai-api"}}); err != nil {
		t.Fatal(err)
	}
	ctx, c := Track(context.Background(), "anthropic-api") // a new call after the loosening
	defer c.Done()
	close(gate) // both changes are delivered only now, the tightening's included
	WaitListeners()
	if ctx.Err() != nil {
		t.Error("a delayed tightening cancelled a call the held policy allows")
	}
}
