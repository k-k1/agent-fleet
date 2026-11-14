package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/imagegen"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

var updateRouteCatalog = flag.Bool("update-route-catalog", false,
	"rewrite the CP's checked-in route catalog from the Agent's catalogue and registry")

// routeCatalogSnapshotPath is the copy the CP embeds (control-plane/internal/agentroutes).
const routeCatalogSnapshotPath = "../../control-plane/internal/agentroutes/catalog.json"

// routeCatalogWire is the snapshot: the route catalogue with each route's class and destination,
// for each registered kind the routes its RouteAdapter declares (none for a kind without one)
// and the fixed routes its own rules resolve to, and the routes of each chat backend and image
// provider (ADR 0138 D2). The CP validates a selection against it before it wakes a Workspace
// (ADR 0128 decision 12).
type routeCatalogWire struct {
	Routes   []routes.Route   `json:"routes"`
	Kinds    []routeKindWire  `json:"kinds"`
	Chat     []routeOwnerWire `json:"chat"`
	Imagegen []routeOwnerWire `json:"imagegen"`
}

type routeKindWire struct {
	Kind   string   `json:"kind"`
	Routes []string `json:"routes"`
	Fixed  []string `json:"fixed"`
}

// routeOwnerWire is a chat backend (by kind) or an image provider (by id) and its routes.
type routeOwnerWire struct {
	ID     string   `json:"id"`
	Routes []string `json:"routes"`
}

func routeOwners(m map[string][]string) []routeOwnerWire {
	out := []routeOwnerWire{}
	for _, id := range slices.Sorted(maps.Keys(m)) {
		out = append(out, routeOwnerWire{ID: id, Routes: m[id]})
	}
	return out
}

func buildRouteCatalog() routeCatalogWire {
	out := routeCatalogWire{Routes: routes.Catalogue(),
		Chat: routeOwners(chatx.ChatBackendRoutes()), Imagegen: routeOwners(imagegen.ProviderRoutes())}
	for _, k := range sessionx.RegisteredKinds() {
		rs, fixed := []string{}, []string{}
		if a, ok := sessionx.RegisteredAgent(k); ok {
			if ra, ok := a.(agents.RouteAdapter); ok {
				rs = slices.Sorted(slices.Values(ra.Routes()))
			}
			if f := agents.FixedRoutesOf(a); f != nil {
				fixed = slices.Sorted(slices.Values(f))
			}
		}
		out.Kinds = append(out.Kinds, routeKindWire{Kind: k, Routes: rs, Fixed: fixed})
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
		for _, id := range append(slices.Clone(k.Routes), k.Fixed...) {
			if _, ok := routes.Lookup(id); !ok {
				t.Errorf("kind %q declares route %q, which is not in routes.Catalogue", k.Kind, id)
			}
		}
	}
	for _, id := range []string{usagex.RouteOpencodeZen, usagex.RouteOpencodeGo, usagex.RouteAFEngineLLM, usagex.RouteMemberLcpp,
		usagex.RouteBedrock, usagex.RouteBedrockTenant, usagex.RouteBedrockDeployment} {
		if _, ok := routes.Lookup(id); !ok {
			t.Errorf("usagex records route %q, which is not in routes.Catalogue", id)
		}
	}
}

// Every credential path has a route id (ADR 0138 D2): every kind that runs a model declares at
// least one route, selectable or fixed, so a policy can see every session. shell and ssm run no
// model. A route no kind, chat backend or image provider declares is one nothing records; the
// ones listed are catalogued ahead of their harness on purpose.
func TestEveryCredentialPathHasARoute(t *testing.T) {
	noModel := []string{"shell", "ssm"}
	cat := buildRouteCatalog()
	declared := map[string]bool{}
	for _, k := range cat.Kinds {
		if len(k.Routes)+len(k.Fixed) == 0 && !slices.Contains(noModel, k.Kind) {
			t.Errorf("kind %q declares no route at all; a policy could not see its sessions", k.Kind)
		}
		for _, id := range append(slices.Clone(k.Routes), k.Fixed...) {
			declared[id] = true
		}
	}
	for _, o := range append(slices.Clone(cat.Chat), cat.Imagegen...) {
		for _, id := range o.Routes {
			declared[id] = true
		}
	}
	// Catalogued ahead of a harness: provided Bedrock on the tenant's account (ADR 0129) and
	// Vertex (ADR 0128 decision 5).
	ahead := []string{routes.RouteBedrockTenant, routes.RouteVertex}
	for _, r := range cat.Routes {
		if !declared[r.ID] && !slices.Contains(ahead, r.ID) {
			t.Errorf("route %q is declared by no kind, chat backend or image provider", r.ID)
		}
	}
}
