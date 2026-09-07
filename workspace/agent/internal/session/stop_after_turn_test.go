package session

import (
	"testing"
	"time"
)

// TestStopArmedAt pins the predicate both packages read (docs/log/85). The unreadable case is
// the one that matters: the instant doubles as the lower bound the end-of-turn evidence is cut
// by, so an arm with no usable bound must read as "not armed" rather than as "armed since the
// zero time", which any earlier marker would immediately satisfy.
func TestStopArmedAt(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		raw  string
		live bool
	}{
		{"not armed", "", false},
		{"armed now", now.Format(time.RFC3339), true},
		{"armed just inside the window", now.Add(-StopArmMaxAge + time.Minute).Format(time.RFC3339), true},
		{"expired", now.Add(-StopArmMaxAge - time.Minute).Format(time.RFC3339), false},
		{"unreadable", "yesterday", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, live := StopArmedAt(Meta{StopAfterTurnAt: tc.raw}, now); live != tc.live {
				t.Fatalf("StopArmedAt(%q) live=%v, want %v", tc.raw, live, tc.live)
			}
		})
	}
}
