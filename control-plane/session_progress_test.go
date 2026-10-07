package main

import (
	"testing"
	"time"
)

// A busy row whose progress age exceeds the bound stops holding the Workspace (#1818). The pin
// and BackgroundBusy are decided before it; an absent age keeps today's hold.
func TestBusyProgressLapse(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	age := func(d time.Duration) int { return int(d / time.Second) }
	future := now.Add(time.Hour).Format(time.RFC3339)
	cases := []struct {
		name string
		in   sessionWire
		want activity
	}{
		{"fresh progress holds", sessionWire{Alive: true, State: stateWorking, ProgressAgeSec: age(5 * time.Minute)}, activityMachineBusy},
		{"exactly the bound holds", sessionWire{Alive: true, State: stateWorking, ProgressAgeSec: age(busyProgressLapse)}, activityMachineBusy},
		{"stale working lapses to unknown", sessionWire{Alive: true, State: stateWorking, ProgressAgeSec: age(busyProgressLapse) + 1}, activityUnknown},
		{"stale compacting lapses too", sessionWire{Alive: true, State: stateCompacting, ProgressAgeSec: age(21 * time.Hour)}, activityUnknown},
		{"absent holds (older Agent, or signals not observable)", sessionWire{Alive: true, State: stateWorking}, activityMachineBusy},
		// The Workspace's clock may be hours off the CP's: only the Agent-computed age counts,
		// so a stamp that looks ancient against this clock but has age 0 still holds.
		{"a skewed progressAt string is not compared with the CP clock", sessionWire{Alive: true, State: stateWorking, ProgressAt: "2020-01-01T00:00:00Z"}, activityMachineBusy},
		{"stale but background work still holds", sessionWire{Alive: true, State: stateWorking, BackgroundBusy: true, ProgressAgeSec: age(21 * time.Hour)}, activityMachineBusy},
		{"stale but pinned still holds", sessionWire{Alive: true, State: stateWorking, KeepAwakeUntil: future, ProgressAgeSec: age(21 * time.Hour)}, activityMachineBusy},
		{"a stale age on an idle row changes nothing", sessionWire{Alive: true, State: stateIdle, ProgressAgeSec: age(21 * time.Hour)}, activityIdleWait},
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
	h3 := int((3 * time.Hour) / time.Second)
	for _, s := range []sessionWire{
		{Name: "a", Alive: true, State: stateWorking, ProgressAgeSec: 300},
		{Name: "a", Alive: true, State: stateWorking, ProgressAgeSec: h3},
		{Name: "a", Alive: true, State: stateCompacting, ProgressAgeSec: h3},
		{Name: "a", Alive: true, State: stateWorking},
		{Name: "a", Alive: true, State: stateWorking, BackgroundBusy: true, ProgressAgeSec: h3},
		{Name: "a", Alive: true, State: stateWorking, KeepAwakeUntil: now.Add(time.Hour).Format(time.RFC3339), ProgressAgeSec: h3},
		{Name: "a", Alive: true, State: stateIdle, ProgressAgeSec: h3},
	} {
		holds := holdsWorkspace(s)
		shown := len(holdersOf([]sessionWire{s}, false, now, 0, 0)) > 0
		if holds != shown {
			t.Errorf("%+v: reaper holds=%v but forecast shows a holder=%v", s, holds, shown)
		}
	}
}
