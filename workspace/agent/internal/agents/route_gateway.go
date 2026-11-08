package agents

import (
	"context"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RouteGrant is what the AF route gateway (ADR 0128 decision 7, A2) gives one launch: the Unix
// socket to call (empty for a loopback grant), the base URL to put in front of the operation
// paths, the bearer, the region the calls go to, and when the bearer ends unless the gateway
// renews it.
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
	// IssueLoopback is Issue for a harness that can only speak TCP: the grant has no Socket, its
	// BaseURL is the gateway's loopback port, and the token is served there only.
	IssueLoopback(m session.Meta) (RouteGrant, *RouteRefusal)
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

// RouteOffering is the gateway's answer to whether a route it serves is offered to the launch
// dialog now: a provided route only while the Control Plane says the member may use it
// (ADR 0129 D3, ADR 0084 decision 8's first place). A gateway without it offers every route.
type RouteOffering interface {
	Offered(route string) bool
}
