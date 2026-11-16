package agents

import (
	"sync"
	"time"
)

// SWRProbe is a stale-while-revalidate cache around a slow status probe (a CLI launch).
// Get answers from the last result and refreshes it in the background, so a GET /connections
// poll never pays the CLI's latency except on the very first answer, and never runs more than
// one probe at a time per instance however often it is polled.
//
// A failed probe never replaces a good answer: "unknown" must not read as "signed out", which
// the launch pickers act on by hiding the agent.
type SWRProbe[T any] struct {
	// TTL is how long an answer is served without a refresh.
	TTL time.Duration
	// ColdBudget bounds the wait when there is no answer at all yet.
	ColdBudget time.Duration
	// Probe runs the CLI. ok=false means it failed (keep the previous answer).
	Probe func() (val T, ok bool)
	// StoreColdFailure records the zero value when the first probe fails, so a CLI that
	// signals "signed out" by a non-zero exit is not re-run on every poll.
	StoreColdFailure bool

	mu      sync.Mutex
	val     T
	have    bool
	at      time.Time
	gen     uint64        // bumped by Invalidate; a probe from an older generation is discarded
	running bool          // a probe of the current generation is out
	done    chan struct{} // closed when the current probe finishes
}

// Get returns the cached answer, starting a background refresh when it is older than TTL.
// With no answer yet it waits for the (single) probe, at most ColdBudget.
func (s *SWRProbe[T]) Get() T {
	s.mu.Lock()
	if s.have && time.Since(s.at) < s.TTL {
		v := s.val
		s.mu.Unlock()
		return v
	}
	done := s.startLocked()
	if s.have {
		v := s.val
		s.mu.Unlock()
		return v // stale: the refresh lands for the next poll
	}
	s.mu.Unlock()

	t := time.NewTimer(s.ColdBudget)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.val
}

// Peek returns the cached answer, however old, without ever waiting: ok=false when there is
// none yet. A missing or stale answer starts a background refresh, as Get does. It is for paths
// that must not pay the CLI's latency at all (a create's launch record).
func (s *SWRProbe[T]) Peek() (val T, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.have || time.Since(s.at) >= s.TTL {
		s.startLocked()
	}
	return s.val, s.have
}

// Warm starts a refresh if the answer is missing or stale, without waiting for it.
func (s *SWRProbe[T]) Warm() {
	s.mu.Lock()
	if !s.have || time.Since(s.at) >= s.TTL {
		s.startLocked()
	}
	s.mu.Unlock()
}

// Invalidate forgets the answer, so the next Get waits for a fresh probe: a login or logout
// must show at once. A probe already out was started before the change and is discarded.
func (s *SWRProbe[T]) Invalidate() {
	s.mu.Lock()
	s.gen++
	s.have = false
	var zero T
	s.val = zero
	s.running = false
	s.mu.Unlock()
}

// probesOut counts the probes of every SWRProbe still running. A probe outlives the Get that
// started it (its CLI keeps running after the cold budget or while a stale answer is served), and
// the CLI writes its own state under HOME: a test that ends first loses its temporary directory
// to it. Production never waits; AwaitProbes is for tests.
var probesOut struct {
	mu   sync.Mutex
	n    int
	idle *sync.Cond
}

func probeStarted() {
	probesOut.mu.Lock()
	probesOut.n++
	probesOut.mu.Unlock()
}

func probeEnded() {
	probesOut.mu.Lock()
	probesOut.n--
	if probesOut.n == 0 && probesOut.idle != nil {
		probesOut.idle.Broadcast()
	}
	probesOut.mu.Unlock()
}

// AwaitProbes blocks until no SWRProbe has a probe running. It has no bound on purpose: giving up
// would bring back the write into a removed directory it exists to prevent. So every Probe
// function must terminate, and one that runs a CLI needs a context timeout.
func AwaitProbes() {
	probesOut.mu.Lock()
	defer probesOut.mu.Unlock()
	if probesOut.idle == nil {
		probesOut.idle = sync.NewCond(&probesOut.mu)
	}
	for probesOut.n > 0 {
		probesOut.idle.Wait()
	}
}

// startLocked launches the probe unless one is already out, returning the channel that
// closes when the current one finishes. s.mu must be held.
func (s *SWRProbe[T]) startLocked() chan struct{} {
	if s.running {
		return s.done
	}
	s.running = true
	done := make(chan struct{})
	s.done = done
	gen := s.gen
	probeStarted()
	go func() {
		defer probeEnded()
		v, ok := s.Probe()
		s.mu.Lock()
		if gen == s.gen {
			switch {
			case ok:
				s.val, s.have, s.at = v, true, time.Now()
			case !s.have && s.StoreColdFailure:
				var zero T
				s.val, s.have, s.at = zero, true, time.Now()
			}
			s.running = false
		}
		s.mu.Unlock()
		close(done)
	}()
	return done
}
