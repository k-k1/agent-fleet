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
	if acct := oauthStatus(); acct.known && acct.connected {
		b := ""
		if acct.id != "" {
			b = credbind.Account(routes.RouteOpencodeAccount, acct.id)
		}
		out = append(out, agents.StoredCredential{ID: "account", Routes: []string{routes.RouteOpencodeAccount}, Binding: b})
	}
	return out
}

func (agentImpl) RemoveCredential(_ context.Context, id string) error {
	if id == "account" {
		return oauthDisconnect()
	}
	env, ok := strings.CutPrefix(id, keyIDPrefix)
	if !ok || !envNameRe.MatchString(env) {
		return agents.ErrUnknownCredential
	}
	return deleteKey(env)
}
