package watch

import (
	"context"
	"encoding/json"
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
func (e *Engine) wakesOwner(s Subscription, now time.Time) bool {
	if s.Notify == NotifyMember {
		return false
	}
	if s.Done || e.inflight[s.ID] > 0 {
		return true
	}
	if s.Builtin != "" {
		return !(s.Max > 0 && s.Sent >= s.Max && len(s.Pending) == 0)
	}
	return now.Before(s.Expires)
}

// holdsOwner is wakesOwner narrowed to what a session that has gone idle can still be waiting
// for. A built-in is the PR feedback feature's standing offer to tell a session about its PR;
// with nothing pending and no check of the PR still running it can only fire after somebody
// pushes, reviews or merges, and the session does not wait for that. Counting it kept a finished
// memo-queue run open for good, with no watch to be seen in the member's list. EndOwner keeps
// the wider rule: a retire still ends every built-in.
func (e *Engine) holdsOwner(s Subscription, now time.Time) bool {
	if !e.wakesOwner(s, now) {
		return false
	}
	if s.Builtin == "" || s.Done || e.inflight[s.ID] > 0 || len(s.Pending) > 0 {
		return true
	}
	return e.pendingChecksOf(s)
}

// pendingChecksOf reports whether s tells about a CI result of a PR whose checks the last
// observation saw still running: that result is on its way and the session is waiting for it.
func (e *Engine) pendingChecksOf(s Subscription) bool {
	if !slices.ContainsFunc(s.Types, func(t string) bool { return t == typeChecksFailed || t == typeChecksFailing }) {
		return false
	}
	var cur PRCursor
	return json.Unmarshal(e.state.Cursors[s.Subject], &cur) == nil && cur.ChecksPending &&
		e.now().Sub(cur.ChecksAt) < checksPendingTTL
}

// Wakers counts owner's subscriptions that can still start it: timers (the ones with a due time)
// and watches (the rest; a built-in only while holdsOwner says the session waits for it). ok is false when the engine could not read its
// state, and the counts mean nothing then.
func (e *Engine) Wakers(owner string) (watches, timers int, ok bool) {
	watches, timers, _, ok = e.WakersListed(owner)
	return watches, timers, ok
}

// WakersListed is Wakers plus listed: how many subscriptions of owner List shows, which is what
// the member sees as the session's watches. It differs from watches+timers because Wakers counts
// built-in subscriptions and notices being delivered, which List leaves out, and List shows an
// expired one that is not yet pruned, which Wakers does not.
func (e *Engine) WakersListed(owner string) (watches, timers, listed int, ok bool) {
	snap, ok := e.WakersSnapshot(owner)
	return snap.Watches, snap.Timers, snap.Listed, ok
}

// Holder names one subscription that Wakers counts, so a person can tell whether the watch that
// holds a session is real.
type Holder struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Builtin bool   `json:"builtin,omitempty"`
	Timer   bool   `json:"timer,omitempty"`
}

// WakersSnapshot is the counts of WakersListed and the holders behind them, read under one lock
// so they never disagree.
type WakersSnapshot struct {
	Watches, Timers, Listed int
	Holders                 []Holder
}

// WakersSnapshot reads WakersListed's counts together with who holds them. ok is false when the
// engine could not read its state.
func (e *Engine) WakersSnapshot(owner string) (snap WakersSnapshot, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	if e.loadFailed {
		return WakersSnapshot{}, false
	}
	now := e.now()
	for _, s := range e.state.Subs {
		if s.Owner != owner {
			continue
		}
		if isListed(s) {
			snap.Listed++
		}
		if !e.holdsOwner(s, now) {
			continue
		}
		if !s.Due.IsZero() {
			snap.Timers++
		} else {
			snap.Watches++
		}
		snap.Holders = append(snap.Holders, Holder{ID: s.ID, Source: s.Source, Builtin: s.Builtin != "", Timer: !s.Due.IsZero()})
	}
	return snap, true
}

// Ended is what EndOwner removed.
type Ended struct {
	Watches  int `json:"watches"`
	Timers   int `json:"timers"`
	Builtins int `json:"builtins"`
}

// EndOwner ends every subscription owner holds that can start it (wakesOwner's rule, wider than the holdsOwner one Wakers counts by), pending notices
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
		if s.Owner != owner || !e.wakesOwner(s, now) {
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

// markInflightLocked adds d to the in-flight count of c's subscription and its twins'.
func (e *Engine) markInflightLocked(c claimed, d int) {
	if e.inflight == nil {
		e.inflight = map[string]int{}
	}
	for _, x := range append([]claimed{c}, c.twins...) {
		if e.inflight[x.sub.ID] += d; e.inflight[x.sub.ID] <= 0 {
			delete(e.inflight, x.sub.ID)
		}
	}
}
