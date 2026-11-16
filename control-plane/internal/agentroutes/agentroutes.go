// Package agentroutes is the CP's copy of the Agent's route catalogue and of which harness may
// run on which route (ADR 0128 decisions 3 and 12).
//
// The CP checks an explicit route selection on every create path before it wakes a Workspace,
// where no Agent is there to ask, so it embeds catalog.json: a snapshot the Agent module
// generates from its route catalogue and its harnesses' RouteAdapter allow-lists, and holds equal
// to them (workspace/agent TestRouteCatalogSnapshot). Never edit catalog.json by hand:
// regenerate it with
//
//	cd workspace/agent && go test -run TestRouteCatalogSnapshot -update-route-catalog .
//
// The check here is static only. Model against route, permission to use the connection and the
// connection's availability need live state and stay with the Agent.
package agentroutes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

//go:embed catalog.json
var catalogJSON []byte

// Route is one catalogue entry. Class and Destination are what ADR 0138's policy judges; a
// PerConnection route's real pair comes from the endpoint its connection is bound to, and the
// catalogue's pair is what it reads as until that endpoint is approved.
type Route struct {
	ID            string `json:"id"`
	Wire          string `json:"wire"`
	Credential    string `json:"credential"`
	Class         string `json:"class"`
	Destination   string `json:"destination"`
	PerConnection bool   `json:"perConnection,omitempty"`
}

// owner is a chat backend (by kind) or an image provider (by id) and the routes it runs on.
type owner struct {
	ID     string   `json:"id"`
	Routes []string `json:"routes"`
}

// Selection is a session's explicit route choice as it rides a create: the route id and a stable
// reference to the connection. It mirrors the Agent's routes.Selection wire shape.
type Selection struct {
	Route      string `json:"route"`
	Connection string `json:"connection,omitempty"`
}

// Launch is a session's launch-time route record (ADR 0138 D11), mirroring the Agent's
// routes.Launch wire shape: the route and a non-secret binding of the credential.
type Launch struct {
	Route   string `json:"route,omitempty"`
	Binding string `json:"binding,omitempty"`
}

// Refusal codes, equal to the Agent's routes.Code* values.
const (
	CodeBadRoute         = "bad_route"
	CodeRouteUnsupported = "route_unsupported"
	CodeRouteUnavailable = "route_connection_unavailable"
)

// Destination is one destination of the catalogue with its vendor host patterns: an exact host or
// "*.suffix". None for the engine, an approved self-hosted endpoint and unclassified.
type Destination struct {
	ID    string   `json:"id"`
	Hosts []string `json:"hosts,omitempty"`
}

var snapshot = func() (c struct {
	Routes       []Route       `json:"routes"`
	Destinations []Destination `json:"destinations"`
	Kinds        []struct {
		Kind   string   `json:"kind"`
		Routes []string `json:"routes"`
		Fixed  []string `json:"fixed"`
	} `json:"kinds"`
	Chat     []owner `json:"chat"`
	Imagegen []owner `json:"imagegen"`
}) {
	// A snapshot that does not parse is a build defect, not a runtime condition.
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("agentroutes: catalog.json: " + err.Error())
	}
	return c
}()

// Routes returns the route catalogue, sorted by id.
func Routes() []Route { return slices.Clone(snapshot.Routes) }

// Destinations returns every destination in the catalogue's order, with its vendor hosts.
func Destinations() []Destination {
	out := slices.Clone(snapshot.Destinations)
	for i := range out {
		out[i].Hosts = slices.Clone(out[i].Hosts)
	}
	return out
}

// RoutesOf returns the routes the harness declares; nil for a kind that declares none, which
// therefore accepts no explicit selection. An empty kind means claude, as on every create path.
func RoutesOf(kind string) []string {
	if kind == "" {
		kind = "claude"
	}
	for _, k := range snapshot.Kinds {
		if k.Kind == kind {
			return slices.Clone(k.Routes)
		}
	}
	return nil
}

// RunsNoModel reports a kind the snapshot knows that declares no route, selectable or fixed: it
// runs no model credential (shell, ssm), so the credential policy has nothing to judge. An
// unknown kind is not one: it is judged. An empty kind means claude.
func RunsNoModel(kind string) bool {
	if kind == "" {
		kind = "claude"
	}
	for _, k := range snapshot.Kinds {
		if k.Kind == kind {
			return len(k.Routes) == 0 && len(k.Fixed) == 0
		}
	}
	return false
}

// Lookup finds a route by its exact id.
func Lookup(id string) (Route, bool) {
	for _, r := range snapshot.Routes {
		if r.ID == id {
			return r, true
		}
	}
	return Route{}, false
}

// FixedRoutesOf returns the routes a session of the kind runs on with no explicit selection, the
// ones its own rules resolve to (ADR 0138 D2); nil for a kind that runs no model. They are never
// selectable: a create that names one is judged by RoutesOf alone.
func FixedRoutesOf(kind string) []string {
	if kind == "" {
		kind = "claude"
	}
	for _, k := range snapshot.Kinds {
		if k.Kind == kind {
			return slices.Clone(k.Fixed)
		}
	}
	return nil
}

// ModelKinds returns every kind in the snapshot that runs on a model credential (declares a
// route, selectable or fixed), in the snapshot's order.
func ModelKinds() []string {
	var out []string
	for _, k := range snapshot.Kinds {
		if len(k.Routes) > 0 || len(k.Fixed) > 0 {
			out = append(out, k.Kind)
		}
	}
	return out
}

// ChatBackends returns the kinds with an assistant chat backend, in the snapshot's order.
func ChatBackends() []string { return ownerIDs(snapshot.Chat) }

// ImagegenProviders returns the image providers, in the snapshot's order.
func ImagegenProviders() []string { return ownerIDs(snapshot.Imagegen) }

func ownerIDs(owners []owner) []string {
	out := make([]string, 0, len(owners))
	for _, o := range owners {
		out = append(out, o.ID)
	}
	return out
}

// ChatRoutesOf returns the routes the assistant chat's backend of the kind runs on; nil for a
// kind with no chat backend.
func ChatRoutesOf(kind string) []string { return ownerRoutes(snapshot.Chat, kind) }

// ImagegenRoutesOf returns the routes the image provider runs on; nil for an unknown provider.
func ImagegenRoutesOf(provider string) []string { return ownerRoutes(snapshot.Imagegen, provider) }

func ownerRoutes(owners []owner, id string) []string {
	for _, o := range owners {
		if o.ID == id {
			return slices.Clone(o.Routes)
		}
	}
	return nil
}

// CheckCreateRoute is the create paths' gate for a route selection, the CP's copy of the Agent's
// static check. A nil selection always passes: that is every existing session and every create
// that names no route, which keep the kind's own resolution rules. A selection the snapshot
// cannot prove valid is refused with the reason, and the code is the Agent's
// (CodeBadRoute, CodeRouteUnsupported), so one mistake reads the same on either side. The CP
// asks here so a doomed create does not first wake a stopped Workspace.
func CheckCreateRoute(kind string, sel *Selection) (code string, err error) {
	if sel == nil {
		return "", nil
	}
	route := strings.TrimSpace(sel.Route)
	if route == "" {
		return CodeBadRoute, fmt.Errorf("route is required when a route selection is given")
	}
	ids := make([]string, 0, len(snapshot.Routes))
	known := false
	for _, r := range snapshot.Routes {
		ids = append(ids, r.ID)
		known = known || r.ID == route
	}
	if !known {
		return CodeBadRoute, fmt.Errorf("unknown route %q; known routes: %s", route, strings.Join(ids, ", "))
	}
	if kind == "" {
		kind = "claude"
	}
	allowed := RoutesOf(kind)
	if len(allowed) == 0 {
		return CodeRouteUnsupported, fmt.Errorf("kind %q does not accept an explicit route yet; leave the route empty to keep its default resolution", kind)
	}
	if !slices.Contains(allowed, route) {
		return CodeRouteUnsupported, fmt.Errorf("kind %q cannot run on route %q; it accepts: %s", kind, route, strings.Join(allowed, ", "))
	}
	return "", nil
}
