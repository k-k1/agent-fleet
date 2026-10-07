package main

import (
	"testing"
	"time"
)

// A busy row whose progressAt lapsed stops holding the Workspace (#1818). The pin and
// BackgroundBusy are decided before it; an absent or unparseable stamp keeps today's hold.
func TestBusyProgressLapse(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	future := now.Add(time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		in   sessionWire
		want activity
	}{
		{"fresh progress holds", sessionWire{Alive: true, State: stateWorking, ProgressAt: ago(5 * time.Minute)}, activityMachineBusy},
		{"just inside the bound holds", sessionWire{Alive: true, State: stateWorking, ProgressAt: ago(busyProgressLapse)}, activityMachineBusy},
		{"stale working lapses to unknown", sessionWire{Alive: true, State: stateWorking, ProgressAt: ago(busyProgressLapse + time.Second)}, activityUnknown},
		{"stale compacting lapses too", sessionWire{Alive: true, State: stateCompacting, ProgressAt: ago(21 * time.Hour)}, activityUnknown},
		{"absent holds (older Agent)", sessionWire{Alive: true, State: stateWorking}, activityMachineBusy},
		{"unparseable holds", sessionWire{Alive: true, State: stateWorking, ProgressAt: "yesterday"}, activityMachineBusy},
		{"stale but background work still holds", sessionWire{Alive: true, State: stateWorking, BackgroundBusy: true, ProgressAt: ago(21 * time.Hour)}, activityMachineBusy},
		{"stale but pinned still holds", sessionWire{Alive: true, State: stateWorking, KeepAwakeUntil: future, ProgressAt: ago(21 * time.Hour)}, activityMachineBusy},
		{"a stale stamp on an idle row changes nothing", sessionWire{Alive: true, State: stateIdle, ProgressAt: ago(21 * time.Hour)}, activityIdleWait},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sessionActivityAt(c.in, now); got != c.want {
				t.Errorf("sessionActivityAt = %v, want %v", got, c.want)
			}
			// The lapsed row is neither a holder nor foldable (tier 2 may stop the
			// Workspace; tier 1 does not touch what it cannot understand).
			if c.want == activityUnknown && tier1Reapable(c.in) {
				t.Error("a lapsed row became foldable by tier 1")
			}
		})
	}
}

// The reaper and the forecast must read one predicate (docs/log/75 decision 11): a row is a
// holder on the screen exactly when sessionActivity says it holds.
func TestForecastAgreesWithReaperOnLapse(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	for _, s := range []sessionWire{
		{Name: "a", Alive: true, State: stateWorking, ProgressAt: ago(5 * time.Minute)},
		{Name: "a", Alive: true, State: stateWorking, ProgressAt: ago(3 * time.Hour)},
		{Name: "a", Alive: true, State: stateCompacting, ProgressAt: ago(3 * time.Hour)},
		{Name: "a", Alive: true, State: stateWorking},
		{Name: "a", Alive: true, State: stateWorking, ProgressAt: "garbage"},
		{Name: "a", Alive: true, State: stateWorking, BackgroundBusy: true, ProgressAt: ago(3 * time.Hour)},
		{Name: "a", Alive: true, State: stateWorking, KeepAwakeUntil: now.Add(time.Hour).Format(time.RFC3339), ProgressAt: ago(3 * time.Hour)},
		{Name: "a", Alive: true, State: stateIdle, ProgressAt: ago(3 * time.Hour)},
	} {
		holds := holdsWorkspace(s)
		shown := len(holdersOf([]sessionWire{s}, false, now, 0, 0)) > 0
		if holds != shown {
			t.Errorf("%+v: reaper holds=%v but forecast shows a holder=%v", s, holds, shown)
		}
	}
}
