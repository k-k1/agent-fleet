package main

import (
	"encoding/json"
	"testing"
	"time"
)

// A background row older than background_hold_max stops holding the Workspace (#2338).
func TestBackgroundLapseClassification(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339)
	const bound = 2 * time.Hour
	bg := func(age int, mod func(*sessionWire)) sessionWire {
		s := sessionWire{Alive: true, State: stateIdle, BackgroundBusy: true, BackgroundAgeSec: age}
		if mod != nil {
			mod(&s)
		}
		return s
	}
	cases := []struct {
		name  string
		in    sessionWire
		bgMax time.Duration
		want  activity
	}{
		{"under the bound holds", bg(3600, nil), bound, activityMachineBusy},
		{"exactly at the bound holds", bg(7200, nil), bound, activityMachineBusy},
		{"over the bound lapses", bg(7201, nil), bound, activityUnknown},
		{"absent age holds (older Agent)", bg(0, nil), bound, activityMachineBusy},
		{"bound 0 disables", bg(999999, nil), 0, activityMachineBusy},
		{"pin wins over a lapsed row", bg(999999, func(s *sessionWire) { s.KeepAwakeUntil = future }), bound, activityMachineBusy},
		{"dead row stays unknown", bg(1, func(s *sessionWire) { s.Alive = false }), bound, activityUnknown},
		{"lapsed row ignores a working state", bg(999999, func(s *sessionWire) { s.State = stateWorking }), bound, activityUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sessionActivityAt(c.in, now, c.bgMax)
			if got != c.want {
				t.Fatalf("sessionActivityAt = %v, want %v", got, c.want)
			}
			// A lapsed row must not become foldable: tier 1 may not start halting rows it did
			// not halt before.
			if c.want == activityUnknown && tier1Reapable(c.in, c.bgMax) {
				t.Error("tier1Reapable = true for a lapsed row")
			}
			// The screen and the reaper must agree (ADR 0055 decision 11).
			holders := holdersOf([]sessionWire{c.in}, false, now, 0, 0, c.bgMax)
			if shown := len(holders) > 0; shown != (c.want == activityMachineBusy && c.in.Alive) {
				t.Errorf("holdersOf = %+v disagrees with sessionActivityAt = %v", holders, got)
			}
		})
	}
}

func TestBackgroundHolderSinceAndLapse(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	h := holdersOf([]sessionWire{{
		Name: "s1", Alive: true, State: stateIdle, BackgroundBusy: true,
		BackgroundSince: "2020-01-01T00:00:00Z", BackgroundAgeSec: 30 * 60,
	}}, false, now, 0, 0, 2*time.Hour)
	if len(h) != 1 || h[0].Kind != "background" || h[0].Since != "2020-01-01T00:00:00Z" {
		t.Fatalf("holders = %+v, want background with the Agent's since", h)
	}
	// Bound minus age, on the CP clock (the skewed since string is not used).
	if want := now.Add(90 * time.Minute).Format(time.RFC3339); h[0].LapseAt != want {
		t.Errorf("LapseAt = %q, want %q", h[0].LapseAt, want)
	}
	h = holdersOf([]sessionWire{{Name: "s1", Alive: true, BackgroundBusy: true, BackgroundSince: "x", BackgroundAgeSec: 5}}, false, now, 0, 0, 0)
	if len(h) != 1 || h[0].LapseAt != "" {
		t.Errorf("bound 0: holders = %+v, want no lapseAt", h)
	}
	h = holdersOf([]sessionWire{{Name: "s1", Alive: true, BackgroundBusy: true}}, false, now, 0, 0, 2*time.Hour)
	if len(h) != 1 || h[0].LapseAt != "" || h[0].Since != "" {
		t.Errorf("older Agent: holders = %+v, want neither since nor lapseAt", h)
	}
}

func TestBackgroundHoldMaxResolution(t *testing.T) {
	def := 2 * time.Hour
	for _, c := range []struct {
		val  string
		want time.Duration
	}{
		{"", def}, {"30m", 30 * time.Minute}, {"0", 0}, {"garbage", def},
	} {
		if got := backgroundHoldMax(tenantLimits{BackgroundHoldMax: c.val}, def); got != c.want {
			t.Errorf("backgroundHoldMax(%q) = %v, want %v", c.val, got, c.want)
		}
	}
	if got := backgroundHoldMax(tenantLimits{}, 0); got != 0 {
		t.Errorf("deployment default 0 = %v, want unbounded", got)
	}
	if got := parseLimits(`{"background_hold_max":"45m"}`).BackgroundHoldMax; got != "45m" {
		t.Errorf("parseLimits dropped background_hold_max: %q", got)
	}
}

// The fields must survive the relay through sessionWire, or the reaper never sees an age.
func TestSessionWireRelaysBackgroundAge(t *testing.T) {
	var s sessionWire
	if err := json.Unmarshal([]byte(`{"alive":true,"backgroundBusy":true,"backgroundSince":"2026-10-18T10:00:00Z","backgroundAgeSec":9000}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.BackgroundSince == "" || s.BackgroundAgeSec != 9000 {
		t.Fatalf("relayed = %q/%d", s.BackgroundSince, s.BackgroundAgeSec)
	}
	if holdsWorkspace(s, 2*time.Hour) {
		t.Error("a 2.5h background row still holds under a 2h bound")
	}
	out, _ := json.Marshal(s)
	var back sessionWire
	_ = json.Unmarshal(out, &back)
	if back.BackgroundAgeSec != 9000 {
		t.Error("age lost on re-marshal")
	}
}

func TestTierClocksCarryBackgroundBound(t *testing.T) {
	c := tierClocks{session: time.Hour, sessionOn: true, bgMax: time.Hour}
	lapsed := sessionWire{Alive: true, State: stateIdle, BackgroundBusy: true, BackgroundAgeSec: 7200}
	if _, on := c.tier1For(lapsed); on {
		t.Error("tier 1 would halt a lapsed background row")
	}
}
