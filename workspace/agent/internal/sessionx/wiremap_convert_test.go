// wiremap_convert_test.go — equivalence proofs for the map sites converted in sessionx
// (CONTRACT-MAP / leg 3).
package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/wiretest"
)

type threadSettingsIn struct {
	Model, Effort, Mode                      string
	DynamicModel, DynamicEffort, DynamicMode bool
}

func TestWireEquivManagedThreadSettings(t *testing.T) {
	inputs := []threadSettingsIn{
		{Model: "opus", Effort: "high", Mode: "plan",
			DynamicModel: true, DynamicEffort: true, DynamicMode: false},
		// Unset means the empty string; the keys must still be emitted (omitempty would drop them).
		{Model: "", Effort: "", Mode: "", DynamicModel: false, DynamicEffort: true, DynamicMode: true},
	}
	got := wiretest.AssertEquiv(t, "HandleSessionSettingsGet", inputs,
		func(in threadSettingsIn) any { // old shape (a copy of the map literal in session_turn.go)
			return map[string]any{
				"model": in.Model, "effort": in.Effort, "mode": in.Mode,
				"dynamicModel": in.DynamicModel, "dynamicEffort": in.DynamicEffort,
				"dynamicMode": in.DynamicMode,
			}
		},
		func(in threadSettingsIn) any {
			return managedThreadSettingsWire{
				Model: in.Model, Effort: in.Effort, Mode: in.Mode,
				DynamicModel: in.DynamicModel, DynamicEffort: in.DynamicEffort,
				DynamicMode: in.DynamicMode,
			}
		})
	t.Logf("comparison mode: %s", got)
}
