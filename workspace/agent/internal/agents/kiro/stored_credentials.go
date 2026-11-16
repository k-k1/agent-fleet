package kiro

import (
	"context"

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

func (agentImpl) RemoveCredential(ctx context.Context, id string) error {
	if id != "login" {
		return agents.ErrUnknownCredential
	}
	return logout(ctx)
}
