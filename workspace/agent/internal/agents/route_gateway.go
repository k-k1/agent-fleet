package agents

import (
	"context"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RouteGrant is what the AF route gateway (ADR 0128 decision 7, A2) gives one launch: the Unix
// socket to call, the base URL to put in front of the operation paths, the bearer, the region
// the calls go to, and when the bearer ends unless the gateway renews it.
type RouteGrant struct {
	Socket  string
	BaseURL string
	Token   string
	Region  string
	Expires time.Time
}

// RouteGateway is the gateway as a harness adapter sees it. internal/routegw implements it and
// sits above the adapters in the import graph (through awsx and sessionx), so it registers
// itself here when it starts instead of being imported.
type RouteGateway interface {
	// Issue mints the bearer for m's stored selection, after every check a call repeats.
	Issue(m session.Meta) (RouteGrant, *RouteRefusal)
	// Bind is the stored form of an accepted selection, bound to the connection's identity.
	Bind(sel RouteSelection) (RouteSelection, *RouteRefusal)
	// CheckRoute is the live half of decision 12 for a selection the gateway serves.
	CheckRoute(ctx context.Context, sel RouteSelection, model string) *RouteRefusal
}

var routeGateway struct {
	mu sync.RWMutex
	g  RouteGateway
}

// SetRouteGateway registers the running gateway; nil when it stops.
func SetRouteGateway(g RouteGateway) {
	routeGateway.mu.Lock()
	routeGateway.g = g
	routeGateway.mu.Unlock()
}

// CurrentRouteGateway is the running gateway, nil when none is: a route that needs it is then
// refused, never run another way.
func CurrentRouteGateway() RouteGateway {
	routeGateway.mu.RLock()
	defer routeGateway.mu.RUnlock()
	return routeGateway.g
}
