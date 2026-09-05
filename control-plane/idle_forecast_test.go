package main

import (
	"testing"
	"time"
)

// TestHoldersOf pins the vocabulary of "why has it not stopped" — the inputs and their
// order. Drift from what the reaper decides and the screen built to investigate gives a
// different answer (docs/log/75 P4), which is worse than having no screen at all.
func TestHoldersOf(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Minute).Format(time.RFC3339)

	t.Run("empty when there is nothing", func(t *testing.T) {
		got := holdersOf([]sessionWire{{Alive: true, State: stateIdle}}, false, now, 0)
		if len(got) != 0 {
			t.Errorf("holders = %+v, want empty", got)
		}
	})

	t.Run("waiting for a human is not a reason to stay up", func(t *testing.T) {
		// The point of docs/log/75: a pending question does not keep the workspace up.
		for _, st := range []string{stateQuestion, statePlan, statePermission, stateBlocked, stateAuth, stateSpendLimit, stateLimited} {
			if got := holdersOf([]sessionWire{{Alive: true, Name: "s1", State: st}}, false, now, 0); len(got) != 0 {
				t.Errorf("state %q is being treated as a reason to stay up: %+v", st, got)
			}
		}
	})

	t.Run("lists working, background work and presence separately", func(t *testing.T) {
		got := holdersOf([]sessionWire{
			{Alive: true, Name: "s2", State: stateWorking},
			{Alive: true, Name: "s1", State: stateIdle, BackgroundBusy: true},
			{Alive: false, Name: "s9", State: stateWorking}, // a stopped session does not count
		}, true, now, 0)
		if len(got) != 3 {
			t.Fatalf("holders = %+v, want 3", got)
		}
		// Sessions by name, presence last: the reasons someone can act on come first.
		if got[0].Session != "s1" || got[0].Kind != "background" {
			t.Errorf("item 1 = %+v, want background/s1", got[0])
		}
		if got[1].Session != "s2" || got[1].Kind != "working" {
			t.Errorf("item 2 = %+v, want working/s2", got[1])
		}
		if got[2].Kind != "watching" || got[2].Session != "" {
			t.Errorf("item 3 = %+v, want watching", got[2])
		}
	})

	// working is not the only state the reaper reads as busy. Drop compacting (codex
	// context compaction) from holders and the screen shows a StopAt for a workspace the
	// reaper will not stop — it promises a stop that never comes, which docs/log/75
	// decision 11 forbids.
	t.Run("lists every state the reaper reads as busy", func(t *testing.T) {
		for _, st := range []string{stateWorking, stateCompacting} {
			s := sessionWire{Alive: true, Name: "s1", State: st}
			got := holdersOf([]sessionWire{s}, false, now, 0)
			if len(got) != 1 || got[0].Kind != "working" {
				t.Errorf("state %q: holders = %+v, want working (holdsWorkspace=%v)", st, got, holdsWorkspace(s))
			}
			if !holdsWorkspace(s) {
				t.Errorf("state %q is no longer busy on the reaper side (the premise changed)", st)
			}
		}
	})

	t.Run("a pin is explained before working", func(t *testing.T) {
		got := holdersOf([]sessionWire{{Alive: true, Name: "s1", State: stateWorking, KeepAwakeUntil: future}}, false, now, 0)
		if len(got) != 1 || got[0].Kind != "pin" || got[0].Until != future {
			t.Errorf("holders = %+v, want pin (the correct explanation is: release it and it stops)", got)
		}
	})

	t.Run("an expired pin is not a reason", func(t *testing.T) {
		got := holdersOf([]sessionWire{{Alive: true, Name: "s1", State: stateIdle, KeepAwakeUntil: past}}, false, now, 0)
		if len(got) != 0 {
			t.Errorf("holders = %+v, want empty", got)
		}
	})

	// A workspace with no sessions at all still does not stop while a repository import
	// is running (docs/log/78). This is the pair of the reaper's own busy check; with only
	// one of the two the workspace stays up with no reason to show.
	t.Run("a running import is a reason even with no sessions", func(t *testing.T) {
		got := holdersOf(nil, false, now, 1)
		if len(got) != 1 || got[0].Kind != "repojob" {
			t.Errorf("holders = %+v, want repojob", got)
		}
	})
}

func TestIdleForecastStore(t *testing.T) {
	m := &manager{}
	if _, ok := m.idleForecastFor("ws1"); ok {
		t.Error("returned a forecast although nothing was observed")
	}
	f := idleForecast{Enabled: true, StopAt: time.Now().Add(time.Hour), ObservedAt: time.Now()}
	m.putIdleForecast("ws1", f)
	got, ok := m.idleForecastFor("ws1")
	if !ok || !got.StopAt.Equal(f.StopAt) || !got.Enabled {
		t.Errorf("idleForecastFor = %+v/%v, want %+v", got, ok, f)
	}
}
