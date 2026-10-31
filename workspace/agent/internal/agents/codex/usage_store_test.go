package codex

import (
	"testing"
	"time"
)

// With every live source empty (rollouts cleaned out, the account view unreachable) the chip
// answers with the reading kept in usagestore, re-adjusted at read time and with its real age
// (#3127).
func TestWithStoredFallsBackToTheLastReading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if u := withStored(usage{}); u.OK {
		t.Fatalf("nothing stored yet, got %+v", u)
	}
	observed := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	reset := time.Now().Add(3 * time.Hour).Unix()
	live, ok := classifyWindows(&recordedWindow{UsedPercent: 42, WindowMinutes: 300, ResetsAt: reset}, nil, "plus")
	if !ok {
		t.Fatal("classifyWindows")
	}
	live.ObservedAt = observed
	withStored(live)

	u := withStored(usage{})
	if !u.OK || u.FiveHour == nil || u.FiveHour.Pct != 42 || u.PlanType != "plus" {
		t.Fatalf("fallback = %+v, want the stored 42%% plus reading", u)
	}
	if !u.ObservedAt.Equal(observed) || u.AgeSec < 29*60 {
		t.Errorf("observedAt %v age %d, want the stored observation's", u.ObservedAt, u.AgeSec)
	}
	if lr := LimitReading(); len(lr.Windows) != 1 || lr.Windows[0].UsedPct != 42 {
		t.Errorf("limit reading = %+v, want the stored window", lr)
	}
}
