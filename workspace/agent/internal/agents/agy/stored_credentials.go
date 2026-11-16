package agy

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

var _ agents.CredentialStore = agentImpl{}

// StoredCredentials and RemoveCredential implement agents.CredentialStore (ADR 0138 D14a).
// agy owns one login, its token file.
func (agentImpl) StoredCredentials(context.Context) []agents.StoredCredential {
	if !SignedIn() {
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
		Login: routes.RouteAgyLogin,
		Current: func(context.Context) ([]string, string, bool) {
			if !SignedIn() {
				return nil, "", false
			}
			l := Login()
			return []string{l.Route}, l.Binding, true
		},
		Remove: func(context.Context) error { return clearLogin() },
	}, binding, forbidden)
}
