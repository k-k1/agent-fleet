package cursor

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.CredentialResolver = agentImpl{}

// Login is what a cursor session runs on (ADR 0138 D2, D5b): cursor-login, bound to the account
// `cursor-agent status` reports. It reads the cached probe without waiting (Peek): a create must
// not pay the CLI's seconds, so a cold probe leaves the binding unproven. A login the probe shows
// with no e-mail falls back to the login's generation.
func Login() routes.Launch {
	l := routes.Launch{Route: routes.RouteCursorLogin}
	if st, ok := statusProbe.Peek(); ok && st.IsAuthenticated {
		l.Binding = credbind.AccountOrGeneration(routes.RouteCursorLogin, st.UserInfo.Email)
	}
	return l
}

// ResolveLaunch implements agents.CredentialResolver.
func (agentImpl) ResolveLaunch(session.Meta) routes.Launch { return Login() }

// UsableRoutes implements agents.CredentialResolver: the login route while signed in.
func (agentImpl) UsableRoutes() []string {
	if LoggedIn() {
		return []string{routes.RouteCursorLogin}
	}
	return nil
}
