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
	// judgeMu makes a judgement atomic against a registration: the listener holds it from reading
	// the policy to the last cancel, Track and SetRoute from registering or naming a route to
	// judging it. A call registered after the listener read a policy therefore waits for that pass
	// to end and is judged against the policy held by then, so a pass reading a tightening that a
	// later loosening has since replaced never reaches a call the loosening admitted.
	judgeMu sync.Mutex
)

// Track registers a call on route for as long as it runs: the returned context is cancelled with
// ErrCancelled when a restricting policy that does not allow route is taken. A route not known yet
// ("") counts as not allowed by a policy taken meanwhile: SetRoute names it once it is. Done ends
// the tracking.
//
// The caller checked the policy before; a tighter one taken between that check and this
// registration would be missed by the listener, whose pass may already be over. So the call is
// judged once more against the policy held now, after it is registered: a store holds its new
// policy before it notifies, so either the listener's pass sees the call or this check sees the
// policy (TestTrackAfterATighteningIsCancelled).
func Track(ctx context.Context, route string) (context.Context, *Call) {
	ctx, cancel := context.WithCancelCause(ctx)
	c := &Call{route: route, cancel: cancel}
	if beforeTrack != nil {
		beforeTrack()
	}
	judgeMu.Lock()
	inflightMu.Lock()
	inflight[c] = struct{}{}
	inflightMu.Unlock()
	if route != "" {
		c.judgeHeld()
	}
	judgeMu.Unlock()
	return context.WithValue(ctx, callKey{}, c), c
}

// beforeTrack runs at the start of Track, between a caller's own check and the registration. A
// seam for tests (SetBeforeTrackForTest).
var beforeTrack func()

// SetBeforeTrackForTest makes Track call f before it registers a call, to take a policy in the
// window a caller's check leaves; it returns the restore.
func SetBeforeTrackForTest(f func()) (restore func()) {
	old := beforeTrack
	beforeTrack = f
	return func() { beforeTrack = old }
}

// judgeHeld cancels c when the policy held now restricts and does not allow its route. judgeMu is
// held.
func (c *Call) judgeHeld() {
	if p, restricted := Hiding(); restricted {
		c.judge(p)
	}
}

// judge cancels c when p does not allow its route; an unnamed route is not allowed.
func (c *Call) judge(p Policy) {
	c.mu.Lock()
	route := c.route
	c.mu.Unlock()
	if route == "" || !p.Allows(route) {
		c.cancel(ErrCancelled)
	}
}

type callKey struct{}

// CallFrom is the call ctx tracks, nil when it tracks none: for code below the one that started
// tracking which resolves the route (SetRoute).
func CallFrom(ctx context.Context) *Call {
	c, _ := ctx.Value(callKey{}).(*Call)
	return c
}

// SetRoute names the route a call resolved after it was tracked, and judges it against the policy
// held now, for the same window as Track's.
func (c *Call) SetRoute(route string) {
	judgeMu.Lock()
	defer judgeMu.Unlock()
	c.mu.Lock()
	c.route = route
	c.mu.Unlock()
	c.judgeHeld()
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

// cancelForbiddenIn is the tracked calls' policy listener for store s (notify): it reads the policy
// s holds and judges every tracked call by it, as one step against registrations (judgeMu).
func cancelForbiddenIn(s *Store) {
	judgeMu.Lock()
	defer judgeMu.Unlock()
	p := s.State().Policy
	if afterJudgeRead != nil {
		afterJudgeRead()
	}
	judgeAllLocked(p)
}

// afterJudgeRead runs in the listener between reading the policy and judging the calls: a seam for
// a test that changes the policy in that interval.
var afterJudgeRead func()

// judgeAllLocked cancels every tracked call p does not allow; nothing while p restricts nothing.
// judgeMu is held.
func judgeAllLocked(p Policy) {
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
		c.judge(p)
	}
}
