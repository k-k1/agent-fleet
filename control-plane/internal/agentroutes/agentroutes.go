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

// Route is one catalogue entry.
type Route struct {
	ID         string `json:"id"`
	Wire       string `json:"wire"`
	Credential string `json:"credential"`
}

// Selection is a session's explicit route choice as it rides a create: the route id and a stable
// reference to the connection. It mirrors the Agent's routes.Selection wire shape.
type Selection struct {
	Route      string `json:"route"`
	Connection string `json:"connection,omitempty"`
}

// Refusal codes, equal to the Agent's routes.Code* values.
const (
	CodeBadRoute         = "bad_route"
	CodeRouteUnsupported = "route_unsupported"
)

var snapshot = func() (c struct {
	Routes []Route `json:"routes"`
	Kinds  []struct {
		Kind   string   `json:"kind"`
		Routes []string `json:"routes"`
	} `json:"kinds"`
}) {
	// A snapshot that does not parse is a build defect, not a runtime condition.
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("agentroutes: catalog.json: " + err.Error())
	}
	return c
}()

// Routes returns the route catalogue, sorted by id.
func Routes() []Route { return slices.Clone(snapshot.Routes) }

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
