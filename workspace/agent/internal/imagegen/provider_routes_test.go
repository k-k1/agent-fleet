package imagegen

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

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
	for _, p := range []Provider{&agyProvider{}, fleetLaunchProvider{ProviderComfy}, fleetLaunchProvider{ProviderOpenAICompat}} {
		l := providerLaunch(p)
		if !slices.Contains(providerRoutes[p.ID()], l.Route) {
			t.Errorf("%s launches on %q, not one of %v", p.ID(), l.Route, providerRoutes[p.ID()])
		}
	}
	if l := providerLaunch(fleetLaunchProvider{ProviderComfy}); l.Binding != "af-engine-image@comfy" {
		t.Errorf("comfy binding = %q", l.Binding)
	}
	if l := providerLaunch(fleetLaunchProvider{"someone-elses"}); l != (routes.Launch{}) {
		t.Errorf("an unknown provider = %+v", l)
	}
}

// The codex provider is resolved against the home it runs codex with ($CODEX_HOME), not the
// default ~/.codex: two auth files, and the record follows the one the provider uses.
func TestCodexProviderLaunchUsesItsOwnHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-default"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	alt := t.TempDir()
	if err := os.WriteFile(filepath.Join(alt, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"acct-alt"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if l := providerLaunch(&codexProvider{home: alt}); l.Route != routes.RouteChatGPTSubscription {
		t.Errorf("codex provider on its own home = %+v, want its ChatGPT login", l)
	}
	if l := providerLaunch(&codexProvider{home: filepath.Join(t.TempDir(), "none")}); l != (routes.Launch{}) {
		t.Errorf("codex provider with no auth.json = %+v", l)
	}
}

// A provider whose launch read hangs costs the record, never the generation's start.
func TestProviderLaunchIsBounded(t *testing.T) {
	old := providerObserveBudget
	providerObserveBudget = 50 * time.Millisecond
	release := make(chan struct{})
	t.Cleanup(func() { providerObserveBudget = old; close(release) })
	start := time.Now()
	if l := providerLaunch(hungLaunchProvider{fleetLaunchProvider{ProviderCodex}, release}); l != (routes.Launch{}) {
		t.Errorf("hung = %+v", l)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("providerLaunch took %v", el)
	}
}

// fleetLaunchProvider is a Provider stand-in with only an id.
type fleetLaunchProvider struct{ id string }

func (f fleetLaunchProvider) ID() string                                      { return f.id }
func (fleetLaunchProvider) Caps(string) Caps                                  { return Caps{} }
func (fleetLaunchProvider) Ready(context.Context) bool                        { return true }
func (fleetLaunchProvider) Generate(context.Context, Request) (Result, error) { return Result{}, nil }

type hungLaunchProvider struct {
	fleetLaunchProvider
	release chan struct{}
}

func (h hungLaunchProvider) launch() routes.Launch { <-h.release; return routes.Launch{Route: "late"} }
