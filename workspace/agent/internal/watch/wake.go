package watch

import (
	"context"
	"fmt"
	"log"
	"slices"
	"time"
)

// Waker is the Control Plane's wake-only schedule target (ADR 0133 §7): a booking that starts
// the workspace if it is stopped and does nothing else. The engine books one for a time watch
// asked for with wake, and keeps it only until the watch has fired or ended.
type Waker interface {
	// Book books a wake for at and returns its id. A deployment whose scheduler is not running
	// answers with an *ErrRefused, so no watch is accepted that would never fire.
	Book(ctx context.Context, at time.Time) (string, error)
	// Cancel removes a booking. nil means the Control Plane no longer holds it, whether it
	// deleted it now or had already (it deletes a booking itself after it fires). Any other
	// answer may have left it in place, and the engine asks again.
	Cancel(ctx context.Context, id string) error
}

// SetWaker connects the Control Plane's wake target. Without one, watch refuses wake.
func (e *Engine) SetWaker(w Waker) {
	e.mu.Lock()
	e.waker = w
	e.mu.Unlock()
}

// timeGrace is how long a time watch outlives its instant by default, so one that fell due while
// the workspace was stopped and without wake is still delivered, late, at the next start.
const timeGrace = 24 * time.Hour

// timeTerms sets the deadline of a time watch and refuses what a timer cannot do. The default
// lifetime is not the 7 days of other watches: a timer 20 days out would end before it fired.
func (e *Engine) timeTerms(sub *Subscription, at time.Time, r Request) error {
	if r.Until == UntilDeadline {
		return &ErrRefused{"a time watch fires once; until deadline does not apply"}
	}
	sub.Due = at.UTC()
	if r.Lifetime == 0 {
		sub.Expires = at.Add(timeGrace).UTC()
		if limit := sub.Created.Add(MaxLifetime); sub.Expires.After(limit) {
			sub.Expires = limit
		}
		return nil
	}
	if sub.Expires.Before(at) {
		return &ErrRefused{"the watch's lifetime ends before the time it waits for"}
	}
	return nil
}

// bookWake books the Control Plane wake for a time watch and records it as outstanding. It is
// recorded before the subscription exists and marked as being booked, so reconcileWakes does not
// cancel it in the meantime; if the watch is then refused, the entry is what gets it cancelled.
func (e *Engine) bookWake(ctx context.Context, at time.Time) (string, error) {
	e.mu.Lock()
	w := e.waker
	e.mu.Unlock()
	if w == nil {
		return "", &ErrRefused{"wake is not available here: this Workspace has no Control Plane scheduler to book with"}
	}
	id, err := w.Book(ctx, at)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.load()
	e.state.Wakes = append(e.state.Wakes, id)
	if e.booking == nil {
		e.booking = map[string]bool{}
	}
	e.booking[id] = true
	return id, nil
}

func (e *Engine) endBooking(id string) {
	e.mu.Lock()
	delete(e.booking, id)
	e.mu.Unlock()
}

// wakeNeeded reports whether a time watch still depends on its booking: until it has fired or
// matched its event. After that the booking only costs a start of the workspace.
func (s Subscription) wakeNeeded() bool {
	return s.WakeID != "" && !s.Done && len(s.Told) == 0 && len(s.Pending) == 0
}

// reconcileWakes cancels every outstanding booking no live watch needs: the watch fired, was
// ended with unwatch, expired, lost its owner or was never saved. Working from the set of
// outstanding bookings, not from the places a watch can end, is what makes the clean-up
// complete and safe to repeat; a cancel the Control Plane does not confirm is asked again after
// a minute.
func (e *Engine) reconcileWakes(ctx context.Context) {
	e.mu.Lock()
	e.load()
	w, now := e.waker, e.now()
	if w == nil || len(e.state.Wakes) == 0 || now.Before(e.wakeRetryAt) {
		e.mu.Unlock()
		return
	}
	needed := map[string]bool{}
	for _, s := range e.state.Subs {
		if s.wakeNeeded() {
			needed[s.WakeID] = true
		}
	}
	var drop []string
	for _, id := range e.state.Wakes {
		if !needed[id] && !e.booking[id] {
			drop = append(drop, id)
		}
	}
	e.mu.Unlock()
	if len(drop) == 0 {
		return
	}

	var gone []string
	var failed error
	for _, id := range drop {
		if ctx.Err() != nil {
			break
		}
		if err := w.Cancel(ctx, id); err != nil {
			failed = fmt.Errorf("%s: %w", id, err)
			continue
		}
		gone = append(gone, id)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if failed != nil {
		e.wakeRetryAt = e.now().Add(time.Minute)
		log.Printf("watch: could not cancel a wake booking, will retry: %v", failed)
	}
	if len(gone) == 0 {
		return
	}
	e.state.Wakes = slices.DeleteFunc(e.state.Wakes, func(id string) bool { return slices.Contains(gone, id) })
	e.save()
}
