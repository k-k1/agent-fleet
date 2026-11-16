package codex

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"path/filepath"

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

func (agentImpl) RemoveCredential(ctx context.Context, id, binding string, forbidden agents.Forbidden) error {
	if id != "login" {
		return agents.ErrUnknownCredential
	}
	// Every codex login change, an API-key login included, is begun on chatgpt-subscription.
	return agents.RemoveLogin(ctx, agents.LoginRemoval{
		Login: routes.RouteChatGPTSubscription,
		Current: func(context.Context) ([]string, string, agents.LoginState) {
			return agents.JSONLoginState(filepath.Join(paths.HomeDir(), ".codex", "auth.json"), loginOf)
		},
		Remove: clearLogin,
	}, binding, forbidden)
}
