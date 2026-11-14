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

// An image call is recorded on a route its provider declares.
func TestProviderLaunchIsADeclaredRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{ProviderOpenAICompat, ProviderComfy, ProviderAgy} {
		l := providerLaunch(id)
		if !slices.Contains(providerRoutes[id], l.Route) {
			t.Errorf("%s launches on %q, not one of %v", id, l.Route, providerRoutes[id])
		}
	}
	if l := providerLaunch(ProviderComfy); l.Binding != "af-engine-image@comfy" {
		t.Errorf("comfy binding = %q", l.Binding)
	}
	// codex with no login resolves to nothing rather than to a guess.
	if l := providerLaunch(ProviderCodex); l != (routes.Launch{}) {
		t.Errorf("codex without auth.json = %+v", l)
	}
	if l := providerLaunch("someone-elses"); l != (routes.Launch{}) {
		t.Errorf("an unknown provider = %+v", l)
	}
}
