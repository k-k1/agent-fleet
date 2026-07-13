package main

import (
	"testing"
	"time"
)

func TestOOMTrackerObserve(t *testing.T) {
	tr := &oomTracker{m: map[string]oomState{}}
	t0 := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

	// First sample only baselines — a pre-existing count is not news, even if non-zero.
	if tr.observe("c1", 3, t0) {
		t.Fatal("first sample reported recent; want baseline only")
	}
	// No change across polls → not recent.
	if tr.observe("c1", 3, t0.Add(time.Second)) {
		t.Fatal("unchanged count reported recent")
	}
	// A new kill → recent.
	if !tr.observe("c1", 4, t0.Add(2*time.Second)) {
		t.Fatal("new oom_kill not reported recent")
	}
	// Still recent within the window even though this poll saw no further increment.
	if !tr.observe("c1", 4, t0.Add(2*time.Second+oomRecentWindow-time.Second)) {
		t.Fatal("kill within window not reported recent")
	}
	// Past the window → no longer recent.
	if tr.observe("c1", 4, t0.Add(2*time.Second+oomRecentWindow+time.Second)) {
		t.Fatal("kill past window still reported recent")
	}
	// A different container is independent (its first sample baselines).
	if tr.observe("c2", 9, t0.Add(time.Hour)) {
		t.Fatal("new container's first sample reported recent")
	}
}
