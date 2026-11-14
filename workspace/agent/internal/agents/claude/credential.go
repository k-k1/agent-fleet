package claude

import (
	"os"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.CredentialResolver = agentImpl{}

// ResolveLaunch implements agents.CredentialResolver: what a pane with no selection runs on
// (ADR 0138 D11). Such a pane keeps the CLI's own rules, which prefer a token in the environment
// it inherits over the OAuth file, so the environment is read first:
//   - ANTHROPIC_API_KEY alone: anthropic-api, bound to that key;
//   - CLAUDE_CODE_OAUTH_TOKEN alone: the subscription, bound to that token;
//   - neither: the subscription in claude's credentials file, bound to the account
//     .claude.json names (accountID), which a token refresh keeps.
//
// Anything that moves the CLI elsewhere (a provider switch, a base URL, both tokens at once, or a
// settings file that sets one of them) resolves to nothing rather than to a guess.
func (agentImpl) ResolveLaunch(m session.Meta) routes.Launch {
	return resolveDefault(os.Getenv, append(userSettingFiles(), projectSettingFiles(m)...), accountID)
}

func resolveDefault(getenv func(string) string, settingFiles []string, account func() string) routes.Launch {
	key, tok := strings.TrimSpace(getenv("ANTHROPIC_API_KEY")), strings.TrimSpace(getenv("CLAUDE_CODE_OAUTH_TOKEN"))
	for _, n := range routeOverrides {
		if n != "ANTHROPIC_API_KEY" && n != "CLAUDE_CODE_OAUTH_TOKEN" && strings.TrimSpace(getenv(n)) != "" {
			return routes.Launch{}
		}
	}
	if key != "" && tok != "" || settingsConflict(RouteSubscription, settingFiles) != nil {
		return routes.Launch{}
	}
	switch {
	case key != "":
		return routes.Launch{Route: RouteAPI, Binding: credbind.Key(apiKeyConnection, key)}
	case tok != "":
		return routes.Launch{Route: RouteSubscription, Binding: credbind.Key("CLAUDE_CODE_OAUTH_TOKEN", tok)}
	}
	return routes.Launch{Route: RouteSubscription, Binding: subscriptionBinding(account)}
}

// subscriptionBinding binds the OAuth login to the account .claude.json names, the login's
// generation when it names none, and nothing when there is no credentials file to bind.
func subscriptionBinding(account func() string) string {
	if _, err := os.Stat(credsPath()); err != nil {
		return ""
	}
	return credbind.AccountOrGeneration(RouteSubscription, account())
}

var _ agents.LaunchBinder = agentImpl{}

// LaunchBinding implements agents.LaunchBinder. An explicit subscription selection runs on the
// OAuth file alone (its launch unsets the environment tokens), so it binds to the login. An
// anthropic-api selection stores the key's plain fingerprint for BindRoute's own check; the
// record gets the keyed binding of the same key instead, since it travels further (credbind).
// Bedrock's stored binding names a profile's identity, not a secret, and is kept.
func (agentImpl) LaunchBinding(sel agents.RouteSelection) string {
	switch sel.Route {
	case RouteSubscription:
		return subscriptionBinding(accountID)
	case RouteAPI:
		k, _ := loadAPIKey()
		if _, fp := splitConnection(sel.Connection); k == "" || fp != "" && fp != keyFingerprint(k) {
			return ""
		}
		return credbind.Key(apiKeyConnection, k)
	}
	return ""
}

// UsableRoutes implements agents.CredentialResolver: the subscription while claude's credentials
// file exists or CLAUDE_CODE_OAUTH_TOKEN is in the environment, anthropic-api while a key is
// stored or ANTHROPIC_API_KEY is in the environment, and an offered Bedrock route while the
// gateway holds a credential for it (a marked profile, the deployment's grant). An acceptance
// gate alone proves nothing is held.
func (a agentImpl) UsableRoutes() []string {
	var out []string
	if _, err := os.Stat(credsPath()); err == nil || strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")) != "" {
		out = append(out, RouteSubscription)
	}
	if k, _ := loadAPIKey(); k != "" || strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != "" {
		out = append(out, RouteAPI)
	}
	for _, r := range a.OfferedRoutes() {
		if isBedrock(r) && agents.GatewayHolds(r) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}
