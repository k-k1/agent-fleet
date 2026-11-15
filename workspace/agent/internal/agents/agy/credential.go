package agy

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.OneRouteResolver = agentImpl{}

// Login is what an agy session runs on (ADR 0138 D2, D5b): agy-login, bound to the account the
// token file's id_token_email names, which a refresh keeps; a token file without one falls back
// to the login's generation. Nothing signed in leaves the binding empty.
func Login() routes.Launch {
	l := routes.Launch{Route: routes.RouteAgyLogin}
	if SignedIn() {
		l.Binding = credbind.AccountOrGeneration(routes.RouteAgyLogin, accountEmail())
	}
	return l
}

// ResolveLaunch implements agents.CredentialResolver.
func (agentImpl) ResolveLaunch(session.Meta) routes.Launch { return Login() }

// ResolvesOneRoute implements agents.OneRouteResolver.
func (agentImpl) ResolvesOneRoute() {}

// UsableRoutes implements agents.CredentialResolver: the login route while signed in.
func (agentImpl) UsableRoutes() []string {
	if SignedIn() {
		return []string{routes.RouteAgyLogin}
	}
	return nil
}
