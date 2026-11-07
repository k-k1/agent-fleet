package routegw

import (
	"log"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
)

// SettingsSource is the Settings list a call is checked against. The Control Plane pushes no
// signal when a profile changes, so the source pulls: Get, the per-call check, at most every
// MinInterval (a streaming harness makes many calls per turn, and a pull per call would hammer
// the CP); Fresh, for a create, a start and a credential resolution, always from a pull that
// began after it was asked. It answers "cannot say" once no pull has succeeded for MaxAge: a
// gateway that cannot see Settings refuses rather than trust a copy it cannot date.
//
// A failed pull never replaces the copy, so it can never turn a refused profile into an allowed
// one; Fresh after a failed or timed-out pull answers from the last copy within MaxAge, as Get
// does.
type SettingsSource struct {
	// Pull fetches the list from the Control Plane; ok is false when it could not.
	Pull        func() (map[string]awsx.Profile, bool)
	Now         func() time.Time
	MinInterval time.Duration
	MaxAge      time.Duration
	// Wait bounds how long a caller waits for a pull (0: defaultSettingsWait).
	Wait time.Duration

	mu      sync.Mutex
	last    map[string]awsx.Profile
	lastTry time.Time
	lastOK  time.Time
	// started and finished count pulls; one runs at a time, so finished is started, or one less
	// while flight is open. flight is closed when the running pull ends.
	started, finished uint64
	flight            chan struct{}
}

func (s *SettingsSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Get is the Deps.Settings function: the per-call check, pulled at most every MinInterval. A
// pull already running is joined rather than doubled.
func (s *SettingsSource) Get() (map[string]awsx.Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastTry.IsZero() || s.now().Sub(s.lastTry) >= s.MinInterval {
		s.pullAfterLocked(s.finished)
	}
	return s.currentLocked()
}

// Fresh is the Deps.FreshSettings function: the list from a pull that started after this call
// did, so an edit the member saved before a create or start reached the Agent is seen by it.
// A pull already running may have read Settings before the edit, so it is waited out and the
// next one is taken; callers arriving together share that next pull, so a burst of starts costs
// at most two pulls.
func (s *SettingsSource) Fresh() (map[string]awsx.Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pullAfterLocked(s.started)
	return s.currentLocked()
}

// pullAfterLocked waits until a pull numbered above n has finished, starting one when none is
// running, or until Wait has passed. s.mu is held on entry and on return, and released while
// waiting. The pull runs on its own goroutine: the real one takes the ~/.aws/config lock before
// its HTTP fetch, and a lock another process holds would otherwise hang every create and start.
// A caller that gives up answers from the last copy, as after a failed pull; a pull that never
// returns keeps later callers on that copy until MaxAge, and then they refuse.
func (s *SettingsSource) pullAfterLocked(n uint64) {
	var timeout <-chan time.Time
	for s.finished <= n {
		if s.flight == nil {
			s.started++
			ch := make(chan struct{})
			s.flight = ch
			t := s.now()
			s.lastTry = t
			go s.runPull(ch, t)
		}
		ch := s.flight
		if timeout == nil {
			tm := time.NewTimer(s.wait())
			defer tm.Stop()
			timeout = tm.C
		}
		s.mu.Unlock()
		select {
		case <-ch:
			s.mu.Lock()
		case <-timeout:
			s.mu.Lock()
			return
		}
	}
}

// runPull runs one pull and publishes it. A panic in Pull is logged and counts as a failed pull:
// it runs off the request's goroutine, where nothing else would recover it.
func (s *SettingsSource) runPull(ch chan struct{}, t time.Time) {
	var m map[string]awsx.Profile
	ok := false
	defer func() {
		if r := recover(); r != nil {
			log.Printf("route gateway: settings pull panicked: %v", r)
			ok = false
		}
		s.mu.Lock()
		// Dated by when the pull began: the CP read it no earlier than that.
		if ok {
			s.last, s.lastOK = m, t
		}
		s.finished = s.started
		s.flight = nil
		close(ch)
		s.mu.Unlock()
	}()
	m, ok = s.Pull()
}

func (s *SettingsSource) wait() time.Duration {
	if s.Wait > 0 {
		return s.Wait
	}
	return defaultSettingsWait
}

// defaultSettingsWait is longer than the bridge's own 10 s HTTP timeout, so a pull that is only
// slow is still waited for.
const defaultSettingsWait = 15 * time.Second

func (s *SettingsSource) currentLocked() (map[string]awsx.Profile, bool) {
	if s.last == nil || s.now().Sub(s.lastOK) > s.MaxAge {
		return nil, false
	}
	return s.last, true
}
