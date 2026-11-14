package sessionx

import "sync"

// inputOrder makes the detached when_ready deliveries of one session run in the order their
// requests were answered. Each delivery waits for its own boot/readiness polling (PaneMode,
// 500 ms ticks) and then types, so two goroutines started 5 s apart with no ordering between
// them reach the pane in whatever order their polls happen to finish; measured live, a 2000-byte
// prompt landed after the 2100-byte one sent 5 s later (#4178). The chain is keyed by session
// and only orders deliveries against each other: a stop, a keys/seq send or a synchronous
// prompt never waits on it, so "stop, then a new prompt" is not held up (#3290).
type inputOrder struct {
	mu    sync.Mutex
	tails map[string]*orderTail
}

// orderTail is the last delivery enqueued for a session; its entry is dropped when that
// delivery finishes with nobody queued behind it.
type orderTail struct {
	done chan struct{}
}

var initialInputOrder = &inputOrder{tails: map[string]*orderTail{}}

// enqueue registers a delivery for name NOW (call it from the request goroutine, so the
// registration order is the request order) and returns the function that runs it: it waits
// for the previous delivery of the same session to finish, runs fn, then releases the next.
func (o *inputOrder) enqueue(name string, fn func()) (run func()) {
	o.mu.Lock()
	prev := o.tails[name]
	cur := &orderTail{done: make(chan struct{})}
	var prevDone chan struct{}
	if prev != nil {
		prevDone = prev.done
	}
	o.tails[name] = cur
	o.mu.Unlock()
	return func() {
		if prevDone != nil {
			<-prevDone
		}
		defer func() {
			o.mu.Lock()
			if o.tails[name] == cur {
				delete(o.tails, name)
			}
			o.mu.Unlock()
			close(cur.done)
		}()
		fn()
	}
}
