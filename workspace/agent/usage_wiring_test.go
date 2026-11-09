package main

// One check that usage_wiring.go is live: usagesvc.Configure only catches an unwired field, not
// one wired to the wrong target. Functions are compared by what they return, since both sides
// are plain values.

import (
	"reflect"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routehealth"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagesvc"
)

func TestUsageWiringIsLive(t *testing.T) {
	w := usagesvc.Wired()
	checks := map[string]func(t *testing.T){
		"ModelsDevPath": func(t *testing.T) {
			if got, want := w.ModelsDevPath(), modelsDevPath(); got != want {
				t.Errorf("ModelsDevPath() = %q, want %q", got, want)
			}
		},
	}
	typ := reflect.TypeOf(w)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if _, ok := checks[name]; !ok {
			t.Errorf("usagesvc.Deps.%s has no wiring check (add one whenever you add a field)", name)
		}
	}
	for name, check := range checks {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("usagesvc.Deps has no %s (only the check is stale)", name)
			continue
		}
		t.Run(name, check)
	}
}

// The agy popover's learned limits come out of a real cooldown table: a typo in the kind or in
// the field copy would otherwise leave the popover silently empty.
func TestLearnedFromTableReadsAgyQuotaParks(t *testing.T) {
	tb := routehealth.NewTable()
	now := time.Now()
	tb.Fail("agy", "gemini-4-argon", "agy execution failed: error: Individual quota reached. Resets in 108h50m38s.", now)
	tb.Fail("codex", "gpt", "usage limit reached. resets in 3h", now)
	got := learnedFromTable(tb, now.Add(time.Minute))
	if len(got) != 1 || got[0].Model != "gemini-4-argon" || !got[0].Until.After(now.Add(100*time.Hour)) {
		t.Fatalf("learned = %+v, want the one agy window", got)
	}
}
