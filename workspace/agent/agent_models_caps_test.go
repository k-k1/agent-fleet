package main

import (
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
)

// The kinds GET /agents/{kind}/models serves: every agent kind. shell and ssm run no model, so
// they answer 404; a kind registered without a ModelLister would silently lose its picker.
func TestEveryAgentKindListsModels(t *testing.T) {
	noCatalog := []string{"shell", "ssm"}
	for _, k := range sessionx.RegisteredKinds() {
		a, _ := sessionx.RegisteredAgent(k)
		_, ok := a.(agents.ModelLister)
		if want := !slices.Contains(noCatalog, k); ok != want {
			t.Errorf("%s: implements agents.ModelLister = %v, want %v", k, ok, want)
		}
	}
}
