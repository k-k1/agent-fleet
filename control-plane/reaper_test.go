package main

import (
	"testing"
	"time"
)

// TestIdleBase pins the tier-2 idle clock's start to the LATEST of the three
// activity signals (boot time / in-memory lastSeen / DB last_active_at). The
// headline case is the regression that made a just-started workspace stop right
// after coming up: a stale in-memory lastSeen must NOT mask a fresh DB
// last_active_at written by the Start.
func TestIdleBase(t *testing.T) {
	boot := time.Date(2026, 7, 12, 4, 10, 0, 0, time.UTC)
	rp := &reaper{bootTime: boot}
	rfc := func(h, m int) string {
		return time.Date(2026, 7, 12, h, m, 0, 0, time.UTC).Format(time.RFC3339)
	}
	ts := func(h, m int) time.Time {
		return time.Date(2026, 7, 12, h, m, 0, 0, time.UTC)
	}

	cases := []struct {
		name         string
		seen         bool
		lastSeen     time.Time
		dbLastActive string
		want         time.Time
	}{
		{
			// The bug: an old terminal left an in-memory lastSeen (05:00), the user
			// then Start-ed hours later (DB last_active_at bumped to 07:41). base must
			// follow the fresh DB stamp, not the stale in-memory one.
			name:         "fresh DB start wins over stale in-memory lastSeen",
			seen:         true,
			lastSeen:     ts(5, 0),
			dbLastActive: rfc(7, 41),
			want:         ts(7, 41),
		},
		{
			// No in-memory record and a stale DB stamp: fall back to boot time so a CP
			// restart grants a fresh grace window.
			name:         "boot floor when nothing newer",
			seen:         false,
			lastSeen:     time.Time{},
			dbLastActive: rfc(3, 0),
			want:         boot,
		},
		{
			// Live proxy/preview traffic (recent lastSeen) beats an older DB stamp.
			name:         "recent in-memory lastSeen wins",
			seen:         true,
			lastSeen:     ts(6, 30),
			dbLastActive: rfc(5, 0),
			want:         ts(6, 30),
		},
		{
			// Unparseable/empty DB value is ignored, not treated as zero-time.
			name:         "empty DB last_active ignored",
			seen:         true,
			lastSeen:     ts(6, 0),
			dbLastActive: "",
			want:         ts(6, 0),
		},
		{
			// seen==false must still consult the DB (this is the branch the old
			// `if !seen` guard covered — kept working).
			name:         "DB consulted when unseen",
			seen:         false,
			lastSeen:     time.Time{},
			dbLastActive: rfc(7, 41),
			want:         ts(7, 41),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rp.idleBase(tc.seen, tc.lastSeen, tc.dbLastActive)
			if !got.Equal(tc.want) {
				t.Fatalf("idleBase = %s, want %s", got, tc.want)
			}
		})
	}
}
