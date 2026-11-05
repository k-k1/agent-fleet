package routegw

import (
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/awsx"
)

// SettingsSource is the Settings list a call is checked against. It pulls from the Control Plane
// at most every MinInterval, so a profile that is unmarked, edited or deleted ends the routes
// that use it within about that long and not within the Agent's five-minute poll, and it answers
// "cannot say" once no pull has succeeded for MaxAge: a gateway that cannot see Settings refuses
// rather than trust a copy it cannot date.
type SettingsSource struct {
	// Pull fetches the list from the Control Plane; ok is false when it could not.
	Pull        func() (map[string]awsx.Profile, bool)
	Now         func() time.Time
	MinInterval time.Duration
	MaxAge      time.Duration

	mu      sync.Mutex
	last    map[string]awsx.Profile
	lastTry time.Time
	lastOK  time.Time
}

// Get is the Deps.Settings function.
func (s *SettingsSource) Get() (map[string]awsx.Profile, bool) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := now()
	if s.lastTry.IsZero() || t.Sub(s.lastTry) >= s.MinInterval {
		s.lastTry = t
		if m, ok := s.Pull(); ok {
			s.last, s.lastOK = m, t
		}
	}
	if s.last == nil || t.Sub(s.lastOK) > s.MaxAge {
		return nil, false
	}
	return s.last, true
}
