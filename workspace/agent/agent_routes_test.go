package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

var updateRouteCatalog = flag.Bool("update-route-catalog", false,
	"rewrite the CP's checked-in route catalog from the Agent's catalogue and registry")

// routeCatalogSnapshotPath is the copy the CP embeds (control-plane/internal/agentroutes).
const routeCatalogSnapshotPath = "../../control-plane/internal/agentroutes/catalog.json"

// routeCatalogWire is the snapshot: the route catalogue, and for each registered kind the routes
// its RouteAdapter declares (none for a kind without one). The CP validates a selection against
// it before it wakes a Workspace (ADR 0128 decision 12).
type routeCatalogWire struct {
	Routes []routes.Route  `json:"routes"`
	Kinds  []routeKindWire `json:"kinds"`
}

type routeKindWire struct {
	Kind   string   `json:"kind"`
	Routes []string `json:"routes"`
}

func buildRouteCatalog() routeCatalogWire {
	out := routeCatalogWire{Routes: routes.Catalogue()}
	for _, k := range sessionx.RegisteredKinds() {
		rs := []string{}
		if a, ok := sessionx.RegisteredAgent(k); ok {
			if ra, ok := a.(agents.RouteAdapter); ok {
				rs = slices.Sorted(slices.Values(ra.Routes()))
			}
		}
		out.Kinds = append(out.Kinds, routeKindWire{Kind: k, Routes: rs})
	}
	return out
}

// TestRouteCatalogSnapshot keeps the CP's embedded route catalog equal to the Agent's.
//
// Update: cd workspace/agent && go test -run TestRouteCatalogSnapshot -update-route-catalog .
func TestRouteCatalogSnapshot(t *testing.T) {
	b, err := json.MarshalIndent(buildRouteCatalog(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := append(b, '\n')
	if *updateRouteCatalog {
		if err := os.WriteFile(routeCatalogSnapshotPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(routeCatalogSnapshotPath)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with -update-route-catalog)", routeCatalogSnapshotPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s is stale against the route catalogue and the harness allow-lists; regenerate:\n"+
			"  cd workspace/agent && go test -run TestRouteCatalogSnapshot -update-route-catalog .\nwant:\n%s",
			routeCatalogSnapshotPath, want)
	}
}

// Every id a harness declares is a catalogued route, and the ledger's own route ids are all in
// the catalogue, so a recorded route can be selected by the same name.
func TestRouteAdaptersDeclareCataloguedRoutes(t *testing.T) {
	for _, k := range buildRouteCatalog().Kinds {
		for _, id := range k.Routes {
			if _, ok := routes.Lookup(id); !ok {
				t.Errorf("kind %q declares route %q, which is not in routes.Catalogue", k.Kind, id)
			}
		}
	}
	for _, id := range []string{usagex.RouteOpencodeZen, usagex.RouteOpencodeGo, usagex.RouteAFEngineLLM, usagex.RouteMemberLcpp} {
		if _, ok := routes.Lookup(id); !ok {
			t.Errorf("usagex records route %q, which is not in routes.Catalogue", id)
		}
	}
}
