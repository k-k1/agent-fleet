package watch

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// What an owner holds that can still start it or put input into it (ADR 0137 §6), and the way to
// end all of it at once (retire). Both read the engine's own state, which a session's list row
// cannot get from Engine.List: that one leaves out built-in subscriptions and ones whose notice
// is being delivered, and both can still start the session.

// StateUnreadable is a Store that can tell "nothing is stored" from "something is stored and
// could not be read". The engine answers Wakers with ok=false in the second case: an empty state
// it fell back to is not evidence that the owner holds nothing.
type StateUnreadable interface {
	Unreadable() bool
}

// wakesOwner reports whether s can still start owner's session or put input into it. A
// built-in delivered to the member puts nothing into the session. One whose deliveries are
// spent and that holds nothing pending cannot fire again; everything else counts, a claimed
// notice (Done) and an ended notice waiting for delivery included.
func (s Subscription) wakesOwner(now time.Time) bool {
	if s.Notify == NotifyMember {
		return false
	}
	if s.Done {
		return true
	}
	if s.Builtin != "" {
		return !(s.Max > 0 && s.Sent >= s.Max && len(s.Pending) == 0)
	}
	return now.Before(s.Expires)
}

// Wakers counts owner's subscriptions that can still start it: timers (the ones with a due time)
// and watches (the rest, built-in ones included). ok is false when the engine could not read its
// state, and the counts mean nothing then.
func (e *Engine) Wakers(owner string) (watches, timers int, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	if e.loadFailed {
		return 0, 0, false
	}
	now := e.now()
	for _, s := range e.state.Subs {
		if s.Owner != owner || !s.wakesOwner(now) {
			continue
		}
		if !s.Due.IsZero() {
			timers++
		} else {
			watches++
		}
	}
	return watches, timers, true
}

// Ended is what EndOwner removed.
type Ended struct {
	Watches  int `json:"watches"`
	Timers   int `json:"timers"`
	Builtins int `json:"builtins"`
}

// EndOwner ends every subscription owner holds that can start it (Wakers' rule), pending notices
// and claimed ones included, and cancels the wake bookings they held. unended names what could
// not be ended: a booking the Control Plane did not confirm gone (it is asked again by every
// tick), which would still start the workspace but find no session to tell. err is set only when
// the state could not be read or saved, and then nothing was ended.
//
// A subscription made for the owner afterwards is the caller's to refuse (Engine.Barred), and a
// built-in made again by its feature likewise: this ends what exists now.
func (e *Engine) EndOwner(ctx context.Context, owner string) (ended Ended, unended []string, err error) {
	e.mu.Lock()
	e.load()
	if e.loadFailed {
		e.mu.Unlock()
		return Ended{}, nil, fmt.Errorf("the watch state could not be read")
	}
	now := e.now()
	before := slices.Clone(e.state.Subs)
	var keep []Subscription
	var bookings []string
	for _, s := range e.state.Subs {
		if s.Owner != owner || !s.wakesOwner(now) {
			keep = append(keep, s)
			continue
		}
		switch {
		case s.Builtin != "":
			ended.Builtins++
		case !s.Due.IsZero():
			ended.Timers++
		default:
			ended.Watches++
		}
		if s.WakeID != "" {
			bookings = append(bookings, s.WakeID)
		}
	}
	if len(keep) == len(e.state.Subs) {
		e.mu.Unlock()
		return ended, e.cancelBookings(ctx, bookings), nil
	}
	e.state.Subs = keep
	e.dropUnwatchedLocked()
	if !e.save() {
		e.state.Subs = before
		e.mu.Unlock()
		return Ended{}, nil, fmt.Errorf("the watch state could not be saved")
	}
	e.mu.Unlock()
	e.sink.Forget(owner)
	return ended, e.cancelBookings(ctx, bookings), nil
}

// cancelBookings runs the booking reconcile now (a failed cancel would otherwise wait a minute)
// and reports which of the given bookings the Control Plane still holds.
func (e *Engine) cancelBookings(ctx context.Context, ids []string) []string {
	e.mu.Lock()
	e.wakeRetryAt = time.Time{}
	e.mu.Unlock()
	e.reconcileWakes(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	var left []string
	for _, id := range ids {
		if slices.Contains(e.state.Wakes, id) {
			left = append(left, "wake booking "+id)
		}
	}
	return left
}
