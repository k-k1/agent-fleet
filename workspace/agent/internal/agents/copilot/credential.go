package copilot

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.CredentialResolver = agentImpl{}

// githubLogin is the GitHub account of the member's GitHub connection (the handle cached at
// connect time), "" without one. A seam for tests.
var githubLogin = func() (login string, connected bool) {
	s, err := secrets.Load()
	if err != nil {
		return "", false
	}
	e, ok := s.Git["github.com"]
	if !ok || e.Token == "" {
		return "", false
	}
	return e.Login, true
}

// Login is what a copilot session runs on (ADR 0138 D2, D5b): copilot-login, the GitHub
// connection's model plan, bound to the GitHub account. A token renewal keeps the account and so
// the binding; connecting another account changes it.
func Login() routes.Launch {
	login, _ := githubLogin()
	return routes.Launch{Route: routes.RouteCopilotLogin, Binding: routes.AccountBinding(routes.RouteCopilotLogin, login)}
}

// ResolveLaunch implements agents.CredentialResolver.
func (agentImpl) ResolveLaunch(session.Meta) routes.Launch { return Login() }

// UsableRoutes implements agents.CredentialResolver: copilot-login while a GitHub connection
// exists.
func (agentImpl) UsableRoutes() []string {
	if _, ok := githubLogin(); ok {
		return []string{routes.RouteCopilotLogin}
	}
	return nil
}
