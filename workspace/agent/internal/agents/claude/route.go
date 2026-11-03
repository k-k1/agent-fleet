package claude

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// Explicit routes for claude (ADR 0128 P1b), Terminal only: claude has no managed driver, so a
// selection on a managed launch is refused by the create's own driver check before it gets here.
//
// A route's credential reaches THIS session's process and no other, through two things:
//   - LaunchPlan.Env (`tmux new-session -e`), for the one credential the route adds;
//   - `env -u` in front of the CLI, for everything that would otherwise override it. claude
//     prefers CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY from the environment over its OAuth
//     file (authexpiry.go), and the pane inherits the tmux server's environment, so without
//     the unset a key in the Agent's environment would move a subscription session onto the
//     metered bill and a stale OAuth token would shadow an API key.
//
// The Agent's own environment never carries a route credential.

// Route ids this harness declares.
const (
	RouteSubscription = "anthropic-subscription"
	RouteAPI          = "anthropic-api"
)

// apiKeyConnection is the connection reference of anthropic-api: the member's Anthropic API key
// as Settings > Connections stores it (the provider-key map, under its environment variable
// name, secrets.Data.Opencode). It is the only possible connection, so an empty reference means
// it; a reference naming anything else is refused. The store is the member's own, so permission
// to USE the connection is the same as owning it, and another member's connection can never be
// named from here.
const apiKeyConnection = "ANTHROPIC_API_KEY"

// loadAPIKey reads the member's Anthropic API key; replaceable in tests.
var loadAPIKey = func() (string, error) {
	s, err := secrets.Load()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s.Opencode[apiKeyConnection]), nil
}

// routeUnset are the variables that would route a claude process somewhere other than the
// selected route, removed from the pane for both routes. The credential variables of the other
// route are added per route below.
var routeUnset = []string{
	"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
}

// Routes implements agents.RouteAdapter.
func (agentImpl) Routes() []string { return []string{RouteAPI, RouteSubscription} }

// ApplyRoute implements agents.RouteAdapter.
func (agentImpl) ApplyRoute(_ session.Meta, sel agents.RouteSelection) (agents.RoutePlan, error) {
	if ref := checkRouteSelection(sel); ref != nil {
		return agents.RoutePlan{}, ref
	}
	unset := append([]string(nil), routeUnset...)
	switch sel.Route {
	case RouteAPI:
		key, _ := loadAPIKey()
		if key = strings.TrimSpace(key); key == "" {
			return agents.RoutePlan{}, noAPIKey()
		}
		// The login of the other route must not shadow the key.
		unset = append(unset, "CLAUDE_CODE_OAUTH_TOKEN")
		return agents.RoutePlan{Env: []string{"ANTHROPIC_API_KEY=" + key}, Unset: unset}, nil
	default: // RouteSubscription: the OAuth login in claude's own credentials file
		unset = append(unset, "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN")
		return agents.RoutePlan{Unset: unset}, nil
	}
}

// ResolveRoute implements agents.RouteAdapter. A session with an explicit selection ran on it:
// the credential is fixed at process start and the selection cannot change while the session
// exists, so the selection is the evidence. A session with none reports nothing, as before.
func (agentImpl) ResolveRoute(m session.Meta, _ agents.TurnInfo) agents.ResolvedRoute {
	if m.Route == nil {
		return agents.ResolvedRoute{}
	}
	return agents.ResolvedRoute{Route: m.Route.Route, Connection: m.Route.Connection}
}

// CheckRoute implements agents.RouteChecker: the live half of the validation. The model needs
// no check (both routes serve every Claude model the CLI offers).
func (agentImpl) CheckRoute(_ context.Context, sel agents.RouteSelection, _ string) *agents.RouteRefusal {
	if ref := checkRouteSelection(sel); ref != nil {
		return ref
	}
	if sel.Route == RouteAPI {
		if key, err := loadAPIKey(); err != nil || strings.TrimSpace(key) == "" {
			return noAPIKey()
		}
	}
	return nil
}

// checkRouteSelection is the connection reference check: whose connection it is.
func checkRouteSelection(sel agents.RouteSelection) *agents.RouteRefusal {
	conn := strings.TrimSpace(sel.Connection)
	switch sel.Route {
	case RouteAPI:
		if conn != "" && conn != apiKeyConnection {
			return &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodeRouteForbidden,
				Message: "connection " + conn + " is not your Anthropic API key connection; a route runs only on a connection of your own"}
		}
	case RouteSubscription:
		if conn != "" {
			return &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodeRouteForbidden,
				Message: "anthropic-subscription runs on this workspace's own Claude login and takes no connection reference"}
		}
	}
	return nil
}

func noAPIKey() *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteUnavailable,
		Message: "no Anthropic API key is connected; add one under Settings > Connections (provider keys), or use the subscription route"}
}

// AuthExpiredFor is AuthExpired judged for one session (ADR 0128 decision 9).
//
//   - No selection: exactly AuthExpired, the Workspace-wide verdict.
//   - anthropic-api: false. The OAuth login is not what this session runs on, so its expiry
//     says nothing about it; whether the key works is only learnt by a turn.
//   - anthropic-subscription: the OAuth file alone, judged as if no token were in the
//     environment. The launch removes those variables, so the file is what the session uses,
//     and a token in the Agent's environment must not hide that it expired.
func AuthExpiredFor(m session.Meta) bool {
	if m.Route == nil {
		return AuthExpired()
	}
	switch m.Route.Route {
	case RouteAPI:
		return false
	case RouteSubscription:
		c, ok := readCreds()
		return ok && expiryOf(c, false).Dead(time.Now())
	}
	return AuthExpired()
}

// routeLaunchProgram puts the route's `env -u` in front of the CLI command.
func routeLaunchProgram(program string, unset []string) string {
	if len(unset) == 0 {
		return program
	}
	return "env -u " + strings.Join(unset, " -u ") + " " + program
}

// stampRoute records on each turn the route the session ran on and, where the route proves it,
// how the call was charged. Only an explicit selection is stamped: with none, the turns stay
// unresolved exactly as before (claude's transcript carries no login or usage-credit signal).
// anthropic-api is metered by construction: the key is the only credential in the process. The
// subscription route stays unresolved, because past a window usage credits can bill the same
// login (decision 8).
func stampRoute(m session.Meta, turns []transcript.Turn) []transcript.Turn {
	if m.Route == nil {
		return turns
	}
	r := agentImpl{}.ResolveRoute(m, agents.TurnInfo{})
	// A copy: the turn list may be a memoized one shared with other readers.
	turns = append([]transcript.Turn(nil), turns...)
	for i := range turns {
		turns[i].Route = r.Route
		if r.Route == RouteAPI {
			turns[i].Billing = usagex.BillingMetered
		}
	}
	return turns
}
