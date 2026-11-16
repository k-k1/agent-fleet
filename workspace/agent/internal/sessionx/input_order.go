package sessionx

import (
	"sync"
	"time"
)

// inputOrder runs the detached when_ready deliveries of one session in the order they were
// enqueued. Each delivery polls for its composer (500 ms ticks) before typing, so goroutines
// with no ordering between them type in whatever order their polls finish: a prompt sent
// first can land after a later one. The chain only orders deliveries against each other; a
// stop, a keys/seq send or a synchronous prompt never waits on it (#3290).
type inputOrder struct {
	mu    sync.Mutex
	tails map[string]*orderTail
	// wait bounds how long a delivery waits for its predecessor. The predecessor's external
	// commands carry no deadline, so a hung one would otherwise hold every later input of the
	// session forever.
	wait time.Duration
}

// orderTail is one delivery's slot. Every field below done and dead is guarded by inputOrder.mu.
type orderTail struct {
	// done closes when the slot's delivery returned; dead closes when the slot was skipped.
	done chan struct{}
	dead chan struct{}
	// finished and skipped mirror the two channels under the mutex. ahead is the live slot a
	// skipped slot was waiting behind (nil: none).
	finished, skipped bool
	ahead             *orderTail
}

// initialInputOrderWait is longer than a delivery's own worst case (about 30 s for tmux, 30 s
// for the composer, then delivery confirmation).
const initialInputOrderWait = 5 * time.Minute

var initialInputOrder = &inputOrder{tails: map[string]*orderTail{}, wait: initialInputOrderWait}

// live returns the nearest slot at or ahead of x that has neither finished nor been skipped,
// or nil when nothing is ahead. Call with o.mu held. It is what a waiter decides on, and what
// the chain's tail is restored to, so a finished or skipped slot is never the tail.
func live(x *orderTail) *orderTail {
	for x != nil && x.skipped {
		x = x.ahead
	}
	if x != nil && x.finished {
		return nil
	}
	return x
}

// enqueue takes name's next slot now, so call it from the request goroutine: slot order is the
// order enqueue calls were linearised in. The returned func waits for the previous slot, then
// runs fn. Whatever woke it (the predecessor finishing or being skipped, or o.wait expiring),
// it decides under the mutex: nothing live ahead means it runs fn, otherwise it runs skip and
// never fn, because typing ahead of a live head would break the order. A skipped slot returns
// at once and gives the tail back to the live slot it waited behind, so an input enqueued after
// the timeout is skipped as well and no follower waits on a hung head.
//
// A head that never returns is bounded by the deadline on every tmux call a delivery makes
// (tmuxx.OneShotTimeout); forget drops the chain's entry when the session is deleted.
func (o *inputOrder) enqueue(name string, fn, skip func()) (run func()) {
	o.mu.Lock()
	prev := o.tails[name]
	cur := &orderTail{done: make(chan struct{}), dead: make(chan struct{})}
	o.tails[name] = cur
	o.mu.Unlock()
	return func() {
		if prev != nil {
			t := time.NewTimer(o.wait)
			defer t.Stop()
			select {
			case <-prev.done:
			case <-prev.dead:
			case <-t.C:
			}
			o.mu.Lock()
			if ahead := live(prev); ahead != nil {
				cur.skipped, cur.ahead = true, ahead
				if o.tails[name] == cur {
					o.tails[name] = ahead
				}
				o.mu.Unlock()
				close(cur.dead)
				skip()
				return
			}
			o.mu.Unlock()
		}
		defer func() {
			o.mu.Lock()
			cur.finished = true
			if o.tails[name] == cur {
				delete(o.tails, name)
			}
			o.mu.Unlock()
			close(cur.done)
		}()
		fn()
	}
}

// forget drops name's chain entry once the session is gone, so a head still running (or a
// follower queued behind it) does not keep a tails entry for a session that no longer exists.
// Running slots are untouched: a head's deferred cleanup compares against the map entry and
// finds it gone, and a follower waits on the predecessor channels it already holds, so a late
// return neither panics nor double-closes anything. An input enqueued for the name afterwards
// starts a fresh chain.
func (o *inputOrder) forget(name string) {
	o.mu.Lock()
	delete(o.tails, name)
	o.mu.Unlock()
}

// forgetInputState releases the per-name input state of a session that is being deleted: the
// when_ready chain and the terminal lock. A holder of the lock keeps the mutex it already
// has and unlocks that one, so removing the map entry cannot cause a double unlock; at worst a
// late arrival for the deleted name gets a fresh mutex.
func forgetInputState(name string) {
	initialInputOrder.forget(name)
	terminalLocks.Delete(name)
}
