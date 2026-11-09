package main

// One check that usage_wiring.go is live: usagesvc.Configure only catches an unwired field, not
// one wired to the wrong target. Functions are compared by what they return, since both sides
// are plain values.

import (
	"reflect"
	"testing"

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
