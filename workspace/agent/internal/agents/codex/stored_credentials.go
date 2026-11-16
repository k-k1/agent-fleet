package codex

import (
	"context"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

var _ agents.CredentialStore = agentImpl{}

// StoredCredentials and RemoveCredential implement agents.CredentialStore (ADR 0138 D14a).
// codex owns one login, auth.json, on chatgpt-subscription or openai-api by its mode.
func (agentImpl) StoredCredentials(context.Context) []agents.StoredCredential {
	l := Login()
	if l.Route == "" {
		return nil
	}
	return []agents.StoredCredential{{ID: "login", Routes: []string{l.Route}, Binding: l.Binding}}
}

func (agentImpl) RemoveCredential(_ context.Context, id string) error {
	if id != "login" {
		return agents.ErrUnknownCredential
	}
	return logout()
}
