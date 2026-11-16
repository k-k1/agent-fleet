package muse

import (
	"context"
	"errors"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

var _ agents.CredentialStore = agentImpl{}

// StoredCredentials and RemoveCredential implement agents.CredentialStore (ADR 0138 D14a).
// muse owns one credential, its auth.json, on muse-login or meta-api by its mechanism. A key in
// the environment (META_API_KEY) cannot be removed and is not listed. Removing needs the CLI,
// whose logout clears the file whichever way it was written.
func (agentImpl) StoredCredentials(context.Context) []agents.StoredCredential {
	b, err := os.ReadFile(authPath())
	if err != nil {
		return nil
	}
	l := loginOf(b)
	if l.Route == "" {
		return nil
	}
	return []agents.StoredCredential{{ID: "login", Routes: []string{l.Route}, Binding: l.Binding}}
}

func (agentImpl) RemoveCredential(ctx context.Context, id, binding string, forbidden func([]string) bool) error {
	if id != "login" {
		return agents.ErrUnknownCredential
	}
	if !Installed() {
		return errors.New("Muse Code is not installed, so its login cannot be removed")
	}
	return agents.RemoveLogin(ctx, agents.LoginRemoval{
		Login:   routes.RouteMuseLogin,
		Current: func(context.Context) ([]string, string, bool) { return storedLogin() },
		Remove:  clearLogin,
	}, binding, forbidden)
}

// storedLogin is auth.json's credential read afresh: its route, binding, and whether there is one.
func storedLogin() ([]string, string, bool) {
	b, err := os.ReadFile(authPath())
	if err != nil {
		return nil, "", false
	}
	l := loginOf(b)
	return []string{l.Route}, l.Binding, l.Route != ""
}
