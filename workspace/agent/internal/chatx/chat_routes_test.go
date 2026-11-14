package chatx

import (
	"maps"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// Every chat backend declares at least one catalogued route, and nothing else does.
func TestChatBackendRoutes(t *testing.T) {
	if got, want := slices.Sorted(maps.Keys(chatBackendRoutes)), chatBackendKinds(); !slices.Equal(got, want) {
		t.Fatalf("chatBackendRoutes kinds = %v, chatBackends kinds = %v", got, want)
	}
	for k, rs := range ChatBackendRoutes() {
		if len(rs) == 0 {
			t.Errorf("chat backend %q declares no route", k)
		}
		for _, id := range rs {
			if _, ok := routes.Lookup(id); !ok {
				t.Errorf("chat backend %q declares %q, which is not in routes.Catalogue", k, id)
			}
		}
	}
}
