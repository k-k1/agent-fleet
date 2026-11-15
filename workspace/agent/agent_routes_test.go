package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
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
	Routes []routes.Route `json:"routes"`
	// Destinations is every destination with its vendor host patterns (none for the engine, an
	// approved endpoint and unclassified): the CP's vocabulary and its refusal of a vendor host
	// as a self-hosted endpoint (ADR 0138 §2).
	Destinations []routeDestinationWire `json:"destinations"`
	Kinds        []routeKindWire        `json:"kinds"`
	Chat         []routeOwnerWire       `json:"chat"`
	Imagegen     []routeOwnerWire       `json:"imagegen"`
}

type routeDestinationWire struct {
	ID    routes.Destination `json:"id"`
	Hosts []string           `json:"hosts,omitempty"`
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
	for _, d := range routes.Destinations() {
		out.Destinations = append(out.Destinations, routeDestinationWire{ID: d, Hosts: routes.VendorHosts(d)})
	}
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

// Every credential path has a route id (ADR 0138 D2): every kind that runs a model declares the
// fixed routes its no-selection resolution lands on, so a policy can see every session, adapter
// kinds included (a selectable list says nothing about what an empty selection runs on). shell
// and ssm run no model. A route no kind, chat backend or image provider declares is one nothing
// records; the ones listed are catalogued ahead of their harness on purpose.
func TestEveryCredentialPathHasARoute(t *testing.T) {
	noModel := []string{"shell", "ssm"}
	cat := buildRouteCatalog()
	declared := map[string]bool{}
	for _, k := range cat.Kinds {
		if len(k.Fixed) == 0 && !slices.Contains(noModel, k.Kind) {
			t.Errorf("kind %q declares no fixed route; a policy could not see its sessions without a selection", k.Kind)
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

// opencodeDefaultRoutes is everything opencode reaches with no selection: its account login, the
// Zen/Go key, every provider key keyEnv hands its serve, and the deployment's engine.
var opencodeDefaultRoutes = []string{"af-engine-llm", "anthropic-api", "custom-api", "gemini-api", "openai-api",
	"opencode-account", "opencode-go", "opencode-zen", "openrouter-api", "sakana-api"}

// TestRouteOwnersPinned is the independent contract per owner (ADR 0138 D2, D10): each kind's
// fixed routes, each chat backend's and each image provider's routes, written out here rather
// than derived, so losing one login mode (codex's openai-api, muse's meta-api) cannot be hidden
// by another owner declaring the same id, nor blessed by regenerating the snapshot.
func TestRouteOwnersPinned(t *testing.T) {
	wantFixed := map[string][]string{
		"agy":      {"agy-login"},
		"claude":   {"anthropic-subscription"},
		"codex":    {"chatgpt-subscription", "openai-api"},
		"copilot":  {"copilot-login"},
		"cursor":   {"cursor-login"},
		"kiro":     {"kiro-login"},
		"lcpp":     {"af-engine-llm", "member-llamacpp"},
		"muse":     {"meta-api", "muse-login"},
		"opencode": opencodeDefaultRoutes,
		"shell":    {},
		"ssm":      {},
	}
	wantChat := map[string][]string{
		"agy":      {"agy-login"},
		"claude":   {"anthropic-subscription"},
		"codex":    {"chatgpt-subscription", "openai-api"},
		"cursor":   {"cursor-login"},
		"lcpp":     {"af-engine-llm", "member-llamacpp"},
		"muse":     {"meta-api", "muse-login"},
		"opencode": opencodeDefaultRoutes,
	}
	wantImagegen := map[string][]string{
		"agy":           {"agy-login"},
		"codex":         {"chatgpt-subscription", "openai-api"},
		"comfy":         {"af-engine-image"},
		"openai-compat": {"af-engine-image"},
	}
	cat := buildRouteCatalog()
	gotFixed := map[string][]string{}
	for _, k := range cat.Kinds {
		gotFixed[k.Kind] = k.Fixed
	}
	owners := func(os []routeOwnerWire) map[string][]string {
		m := map[string][]string{}
		for _, o := range os {
			m[o.ID] = o.Routes
		}
		return m
	}
	for _, c := range []struct {
		what      string
		got, want map[string][]string
	}{{"kind fixed routes", gotFixed, wantFixed}, {"chat backend routes", owners(cat.Chat), wantChat},
		{"image provider routes", owners(cat.Imagegen), wantImagegen}} {
		if diff := routeOwnersDiff(c.got, c.want); diff != "" {
			t.Errorf("%s differ from the pinned contract:%s", c.what, diff)
		}
	}
}

// routeOwnersDiff lists every owner whose routes differ, "" when the two maps are equal.
func routeOwnersDiff(got, want map[string][]string) string {
	ids := map[string]bool{}
	for k := range got {
		ids[k] = true
	}
	for k := range want {
		ids[k] = true
	}
	var b strings.Builder
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		g, w := got[id], want[id]
		if !slices.Equal(slices.Sorted(slices.Values(g)), slices.Sorted(slices.Values(w))) {
			fmt.Fprintf(&b, "\n  %s: got %v, want %v", id, g, w)
		}
	}
	return b.String()
}
