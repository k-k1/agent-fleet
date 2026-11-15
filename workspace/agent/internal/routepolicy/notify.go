package routepolicy

import "sync"

// Listeners of a change of the held policy: what must act when the policy tightens rather than
// only judge what is asked of it next (ADR 0138 D12: a login flow already running is cancelled).

var (
	listenersMu sync.Mutex
	listeners   []func(Policy)
	// running counts the listener calls in flight, for WaitListeners.
	running sync.WaitGroup
)

// OnChange registers f to be called, on its own goroutine, with the held policy each time a store
// takes a different one (a higher revision, a placeholder, a start marker). Register at init.
func OnChange(f func(Policy)) {
	listenersMu.Lock()
	defer listenersMu.Unlock()
	listeners = append(listeners, f)
}

// notify tells the listeners the policy s now holds. Called after s's lock is released.
func (s *Store) notify() {
	p := s.State().Policy
	listenersMu.Lock()
	fs := append([]func(Policy){}, listeners...)
	listenersMu.Unlock()
	for _, f := range fs {
		running.Add(1)
		go func() {
			defer running.Done()
			f(p)
		}()
	}
}

// WaitListeners waits for the listener calls in flight to return. A test that swaps a package var
// a listener reads (Active among them) calls it first: the listeners a store's Accept started run
// on their own goroutines and outlive it. Production never waits.
func WaitListeners() { running.Wait() }
