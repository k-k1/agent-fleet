package claude

import (
	"context"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

var _ agents.CredentialStore = agentImpl{}

// StoredCredentials and RemoveCredential implement agents.CredentialStore (ADR 0138 D14a).
// claude owns one: its subscription login file. The Anthropic API key lives in opencode's slot
// and is opencode's to list, so a key both harnesses use is listed once. A token in the
// environment (CLAUDE_CODE_OAUTH_TOKEN) cannot be removed and is not listed.
func (agentImpl) StoredCredentials(context.Context) []agents.StoredCredential {
	if _, err := os.Stat(credsPath()); err != nil {
		return nil
	}
	return []agents.StoredCredential{{ID: "login", Routes: []string{RouteSubscription}, Binding: subscriptionBinding(accountID)}}
}

func (agentImpl) RemoveCredential(_ context.Context, id string) error {
	if id != "login" {
		return agents.ErrUnknownCredential
	}
	return logout()
}
