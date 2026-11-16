package opencode

import (
	"context"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var _ agents.CredentialStore = agentImpl{}

const keyIDPrefix = "key:"

// StoredCredentials and RemoveCredential implement agents.CredentialStore (ADR 0138 D14a).
// opencode owns every key in its slot of the Agent's store (the Anthropic key claude uses among
// them, listed once here) and the opencode.ai account login. The account is listed only while
// opencode serve answers for it: an unknown state is not a credential to remove.
func (agentImpl) StoredCredentials(context.Context) []agents.StoredCredential {
	var out []agents.StoredCredential
	if s, err := secrets.Load(); err == nil {
		for env, v := range s.Opencode {
			if strings.TrimSpace(v) == "" {
				continue
			}
			out = append(out, agents.StoredCredential{ID: keyIDPrefix + env, Routes: keyEnvRoutes(env), Binding: credbind.Key(env, v)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if acct := oauthStatus(); acct.known && acct.connected && acct.id != "" {
		out = append(out, agents.StoredCredential{ID: "account", Routes: []string{routes.RouteOpencodeAccount}, Binding: accountBinding(acct.id)})
	}
	return out
}

func accountBinding(credentialID string) string {
	return credbind.Account(routes.RouteOpencodeAccount, credentialID)
}

func (agentImpl) RemoveCredential(_ context.Context, id, binding string, forbidden agents.Forbidden) error {
	if id == "account" {
		return removeAccount(binding, forbidden)
	}
	env, ok := strings.CutPrefix(id, keyIDPrefix)
	if !ok || !envNameRe.MatchString(env) {
		return agents.ErrUnknownCredential
	}
	return removeKey(env, binding, forbidden)
}

// removeKey deletes the key stored under env only if, inside the store's lock that every write of
// the slot takes, it is still the confirmed key and still forbidden.
func removeKey(env, binding string, forbidden agents.Forbidden) error {
	removed := false
	if err := secrets.Update(func(s *secrets.Data) error {
		v := strings.TrimSpace(s.Opencode[env])
		if v == "" || credbind.Key(env, s.Opencode[env]) != binding || !forbidden(keyEnvRoutes(env), "") {
			return agents.ErrCredentialChanged
		}
		delete(s.Opencode, env)
		removed = true
		return nil
	}); err != nil {
		return err
	}
	if removed {
		applyKeyChange("provider key removed: " + env)
	}
	return nil
}

// removeAccount deletes the Console credential only if the one serve holds now, read from serve
// and never from the status cache, is the confirmed one. It deletes that credential by its id, so
// a sign-in that replaces it in between leaves a credential under another id, which is not
// touched. The account's sign-in is serve's own, outside the Agent's locks; deleting by id is
// what keeps it conditional.
func removeAccount(binding string, forbidden agents.Forbidden) error {
	addr, err := oauthDaemon()
	if err != nil {
		return serveUnavailable{err}
	}
	id, err := credentialID(addr)
	if err != nil {
		return err
	}
	if id == "" || accountBinding(id) != binding || !forbidden([]string{routes.RouteOpencodeAccount}, "") {
		return agents.ErrCredentialChanged
	}
	if err := deleteCredential(addr, id); err != nil {
		return err
	}
	if cur, err := credentialID(addr); err != nil || cur == id {
		return agents.ErrStillPresent
	}
	return nil
}
