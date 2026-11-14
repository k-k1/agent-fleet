package agy

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.CredentialResolver = agentImpl{}

// Login is what a agy session runs on (ADR 0138 D2, D5b): its one login route, bound to the
// account named by the token file's id_token_email, which a token refresh keeps and a sign-in to another account changes. The
// route is resolved even when nothing is signed in, since it is the only one the CLI can use;
// the binding is then empty.
func Login() routes.Launch {
	return routes.Launch{Route: routes.RouteAgyLogin, Binding: routes.AccountBinding(routes.RouteAgyLogin, accountEmail())}
}

// ResolveLaunch implements agents.CredentialResolver.
func (agentImpl) ResolveLaunch(session.Meta) routes.Launch { return Login() }

// UsableRoutes implements agents.CredentialResolver: the login route while signed in.
func (agentImpl) UsableRoutes() []string {
	if SignedIn() {
		return []string{routes.RouteAgyLogin}
	}
	return nil
}
