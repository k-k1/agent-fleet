package kiro

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

var _ agents.CredentialStore = agentImpl{}

// StoredCredentials and RemoveCredential implement agents.CredentialStore (ADR 0138 D14a).
// kiro owns one login. LoggedIn warms the status probe Login's binding is read from.
func (agentImpl) StoredCredentials(context.Context) []agents.StoredCredential {
	if !LoggedIn() {
		return nil
	}
	l := Login()
	return []agents.StoredCredential{{ID: "login", Routes: []string{l.Route}, Binding: l.Binding}}
}

func (agentImpl) RemoveCredential(ctx context.Context, id, binding string, forbidden func([]string) bool) error {
	if id != "login" {
		return agents.ErrUnknownCredential
	}
	return agents.RemoveLogin(ctx, agents.LoginRemoval{
		Login: routes.RouteKiroLogin,
		// The status probe's own run, not its cache: the login must be read as it is now.
		Current: func(context.Context) ([]string, string, bool) {
			st, ok := statusProbe.Probe()
			if !ok || !st.loggedIn {
				return nil, "", false
			}
			return []string{routes.RouteKiroLogin}, credbind.AccountOrGeneration(routes.RouteKiroLogin, st.email), true
		},
		Remove: clearLogin,
	}, binding, forbidden)
}
