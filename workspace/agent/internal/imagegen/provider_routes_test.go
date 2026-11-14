package imagegen

import (
	"maps"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// Every image provider in the built-in order declares at least one catalogued route.
func TestProviderRoutes(t *testing.T) {
	if got, want := slices.Sorted(maps.Keys(providerRoutes)), slices.Sorted(slices.Values(providerOrder)); !slices.Equal(got, want) {
		t.Fatalf("providerRoutes = %v, providerOrder = %v", got, want)
	}
	for id, rs := range ProviderRoutes() {
		if len(rs) == 0 {
			t.Errorf("image provider %q declares no route", id)
		}
		for _, r := range rs {
			if _, ok := routes.Lookup(r); !ok {
				t.Errorf("image provider %q declares %q, which is not in routes.Catalogue", id, r)
			}
		}
	}
}
