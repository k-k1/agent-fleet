package agents

import (
	"context"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// RouteSelection is a session's explicit route choice (ADR 0128 decision 3).
type RouteSelection = routes.Selection

// RouteAdapter is an optional Agent capability (ADR 0128 decision 4): the harness accepts an
// explicit route selection. A kind without it accepts none, and every create, fork and switch
// naming a route for it is refused: never ignored, never run on its default route instead.
// Resolved by exact registry key, never through the claude fallback.
//
// The allow-list is the harness's to declare and is not a cartesian product: the Claude
// subscription login is for Claude Code only, so anthropic-subscription is never on another
// harness's list.
type RouteAdapter interface {
	// Routes are the catalogue ids this harness may run on. Every id must be in routes.Catalogue
	// (TestRouteAdaptersDeclareCataloguedRoutes). The CP snapshot is generated from it.
	Routes() []string
	// ApplyRoute turns a validated selection into what the launch needs, for this session only. A
	// credential reaches this session and no other (decision 5), and never the Agent's own
	// environment.
	ApplyRoute(m session.Meta, sel RouteSelection) (RoutePlan, error)
	// ResolveRoute says what a turn actually ran on, from that turn's own evidence. It is what
	// the usage ledger records, so it must not read the session's current settings.
	ResolveRoute(m session.Meta, turn TurnInfo) ResolvedRoute
}

// RoutePlan is what ApplyRoute adds to a launch.
type RoutePlan struct {
	// Env is KEY=VALUE pairs for this session's process only (LaunchPlan.Env).
	Env []string
	// Unset names environment variables the launch removes from the process before the CLI
	// starts (`env -u`). A route that must not be overridden by something the Agent's own
	// environment or the tmux server carries lists them here: the credential of the OTHER route
	// is the case that matters.
	Unset []string
	// Args are extra program arguments.
	Args []string
	// Daemon names the shared runtime the session must run on when the route needs a daemon of
	// its own (a daemon reads its credentials once, at start). Empty: the harness's usual one.
	Daemon string
}

// TurnInfo is what ResolveRoute is told about one turn.
type TurnInfo struct {
	Model string
}

// ResolvedRoute is what a turn ran on. An empty Route means the harness cannot prove it.
type ResolvedRoute struct {
	Route      string
	Connection string
}

// RouteChecker is the live half of the validation (ADR 0128 decision 12), an optional
// capability of a RouteAdapter. It runs after the wake, before anything starts, and again at a
// fork: what the static list cannot know. The model has already been resolved against the
// kind's live catalogue when it is called.
//
// An implementation refuses with a *RouteRefusal, in this order of concern:
//   - the model is not one this route serves (routes.CodeRouteModel);
//   - the caller may not USE the referenced connection (routes.CodeRouteForbidden). Permission
//     is use, not ownership: a member's private connection is usable by its owner only, a tenant
//     connection by the members the tenant allows, a deployment route by its own gates;
//   - the connection is gone, expired or not ready (routes.CodeRouteUnavailable).
//
// It must never fall back to another connection.
type RouteChecker interface {
	CheckRoute(ctx context.Context, sel RouteSelection, model string) *RouteRefusal
}

// RouteRefusal is a refused selection: an HTTP status, one of the routes.Code* values and a
// message the member can act on.
type RouteRefusal struct {
	Status  int
	Code    string
	Message string
}

// Error makes a refusal usable as the error ApplyRoute returns.
func (r *RouteRefusal) Error() string { return r.Message }
