package routepolicy

import (
	"context"
	"errors"
	"sync"
)

// Calls in flight under a tightening policy (ADR 0138 D12, "A chat call and an image call in flight
// when A is tightened are cancelled"): a utility call (the assistant chat, a one-shot, an image
// generation) registers the route it runs on for as long as it runs, and when the held policy
// becomes one that restricts and does not allow that route, its context is cancelled with
// ErrCancelled. The call's own context then kills its CLI or aborts its request; the caller turns
// it into its policy refusal and writes no ledger row. A policy that restricts nothing cancels
// nothing.

// ErrCancelled is the cause of a call's context cancelled by the credential policy.
var ErrCancelled = errors.New("the credential policy no longer allows the route this call was running on, so it was stopped")

// Call is one tracked call.
type Call struct {
	mu     sync.Mutex
	route  string
	cancel context.CancelCauseFunc
}

var (
	inflightMu sync.Mutex
	inflight   = map[*Call]struct{}{}
)

// Track registers a call on route for as long as it runs: the returned context is cancelled with
// ErrCancelled when a restricting policy that does not allow route is taken. A route not known yet
// ("") counts as not allowed: SetRoute names it once it is. Done ends the tracking.
func Track(ctx context.Context, route string) (context.Context, *Call) {
	ctx, cancel := context.WithCancelCause(ctx)
	c := &Call{route: route, cancel: cancel}
	inflightMu.Lock()
	inflight[c] = struct{}{}
	inflightMu.Unlock()
	return context.WithValue(ctx, callKey{}, c), c
}

type callKey struct{}

// CallFrom is the call ctx tracks, nil when it tracks none: for code below the one that started
// tracking which resolves the route (SetRoute).
func CallFrom(ctx context.Context) *Call {
	c, _ := ctx.Value(callKey{}).(*Call)
	return c
}

// SetRoute names the route a call resolved after it was tracked.
func (c *Call) SetRoute(route string) {
	c.mu.Lock()
	c.route = route
	c.mu.Unlock()
}

// Done ends the tracking. The context is released too; a call cancelled by the policy keeps
// ErrCancelled as its cause.
func (c *Call) Done() {
	inflightMu.Lock()
	delete(inflight, c)
	inflightMu.Unlock()
	c.cancel(context.Canceled)
}

// Cancelled reports whether ctx was cancelled by the credential policy.
func Cancelled(ctx context.Context) bool { return errors.Is(context.Cause(ctx), ErrCancelled) }

// cancelForbidden is the tracked calls' policy listener.
func cancelForbidden(p Policy) {
	if !p.Restricted {
		return
	}
	inflightMu.Lock()
	calls := make([]*Call, 0, len(inflight))
	for c := range inflight {
		calls = append(calls, c)
	}
	inflightMu.Unlock()
	for _, c := range calls {
		c.mu.Lock()
		route := c.route
		c.mu.Unlock()
		if route == "" || !p.Allows(route) {
			c.cancel(ErrCancelled)
		}
	}
}

func init() { OnChange(cancelForbidden) }
