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

// orderTail is the last delivery enqueued for a session; its entry is dropped when its slot is
// released with nobody queued behind it.
type orderTail struct {
	// done closes when the slot's delivery returned. A skipped slot never closes it.
	done chan struct{}
	// dead closes when the slot is skipped, so later inputs skip at once instead of
	// waiting out their own timeout.
	dead chan struct{}
	// ahead is the live slot a skipped slot was waiting behind; set before dead closes.
	ahead *orderTail
}

// initialInputOrderWait is longer than a delivery's own worst case (about 30 s for tmux, 30 s
// for the composer, then delivery confirmation).
const initialInputOrderWait = 5 * time.Minute

var initialInputOrder = &inputOrder{tails: map[string]*orderTail{}, wait: initialInputOrderWait}

// enqueue takes name's next slot now, so call it from the request goroutine: slot order is the
// order enqueue calls were linearised in. The returned func waits for the previous slot, then
// runs fn. When the predecessor has not finished within o.wait it runs skip instead and never
// fn: failing the later input keeps the order, whereas letting it type ahead of a head that is
// still alive would break it. A skipped slot returns at once (no goroutine or WaitGroup entry
// outlives the timeout) and hands the chain's tail back to the live slot it waited behind, so
// an input enqueued after the timeout is skipped as well, and the one remaining entry goes
// when that head returns.
func (o *inputOrder) enqueue(name string, fn, skip func()) (run func()) {
	o.mu.Lock()
	prev := o.tails[name]
	cur := &orderTail{done: make(chan struct{}), dead: make(chan struct{})}
	o.tails[name] = cur
	o.mu.Unlock()
	release := func() {
		o.mu.Lock()
		if o.tails[name] == cur {
			delete(o.tails, name)
		}
		o.mu.Unlock()
		close(cur.done)
	}
	skipSlot := func(ahead *orderTail) {
		o.mu.Lock()
		cur.ahead = ahead
		if o.tails[name] == cur {
			o.tails[name] = ahead
		}
		o.mu.Unlock()
		close(cur.dead)
		skip()
	}
	return func() {
		if prev == nil {
			defer release()
			fn()
			return
		}
		t := time.NewTimer(o.wait)
		defer t.Stop()
		select {
		case <-prev.done:
		case <-prev.dead:
			// Everything between is skipped; only the live slot behind it matters.
			if !isClosed(prev.ahead.done) {
				skipSlot(prev.ahead)
				return
			}
		case <-t.C:
			skipSlot(prev)
			return
		}
		defer release()
		fn()
	}
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
