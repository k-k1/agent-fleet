package sessionx

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestBackgroundSinceTracksPeriod(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	name := "bgsince-" + t.Name()
	poll := func(busy, alive bool, at time.Time) session.Session {
		s := session.Session{Name: name, BackgroundBusy: busy, Alive: alive}
		fillBackgroundSince(&s, at)
		return s
	}

	s := poll(true, true, t0)
	if s.BackgroundSince != t0.Format(time.RFC3339) || s.BackgroundAgeSec != 0 {
		t.Fatalf("first poll = %q/%d, want since=t0 age=0", s.BackgroundSince, s.BackgroundAgeSec)
	}
	s = poll(true, true, t0.Add(3*time.Hour))
	if s.BackgroundSince != t0.Format(time.RFC3339) || s.BackgroundAgeSec != 3*3600 {
		t.Fatalf("later poll = %q/%d, want since kept and age 10800", s.BackgroundSince, s.BackgroundAgeSec)
	}
	s = poll(false, true, t0.Add(4*time.Hour))
	if s.BackgroundSince != "" || s.BackgroundAgeSec != 0 {
		t.Fatalf("not busy = %q/%d, want cleared", s.BackgroundSince, s.BackgroundAgeSec)
	}
	t1 := t0.Add(5 * time.Hour)
	s = poll(true, true, t1)
	if s.BackgroundSince != t1.Format(time.RFC3339) {
		t.Fatalf("new period since = %q, want restart at t1", s.BackgroundSince)
	}
	// A dead session forgets the period.
	if s = poll(true, false, t1.Add(time.Minute)); s.BackgroundSince != "" {
		t.Fatalf("dead row carries since %q", s.BackgroundSince)
	}
	if s = poll(true, true, t1.Add(2*time.Minute)); s.BackgroundSince != t1.Add(2*time.Minute).Format(time.RFC3339) {
		t.Fatalf("after death since = %q, want fresh start", s.BackgroundSince)
	}
}

func TestPruneBackgroundSinceDropsUnlisted(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	keep, gone := "bgprune-keep", "bgprune-gone"
	backgroundSinceOf(keep, true, true, now)
	backgroundSinceOf(gone, true, true, now) // never observed dead: archived while busy
	pruneBackgroundSince(func(n string) bool { return n == keep })
	backgroundSeenMu.Lock()
	_, hasKeep := backgroundSeenMap[keep]
	_, hasGone := backgroundSeenMap[gone]
	backgroundSeenMu.Unlock()
	if !hasKeep || hasGone {
		t.Fatalf("keep=%v gone=%v, want true/false", hasKeep, hasGone)
	}
	backgroundSinceOf(keep, false, true, now)
}
