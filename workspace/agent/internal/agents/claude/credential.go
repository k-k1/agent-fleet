package claude

import (
	"os"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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
		return routes.Launch{Route: RouteAPI, Binding: routes.KeyBinding(apiKeyConnection, key)}
	case tok != "":
		return routes.Launch{Route: RouteSubscription, Binding: routes.KeyBinding("CLAUDE_CODE_OAUTH_TOKEN", tok)}
	}
	return routes.Launch{Route: RouteSubscription, Binding: routes.AccountBinding(RouteSubscription, account())}
}

var _ agents.LaunchBinder = agentImpl{}

// LaunchBinding implements agents.LaunchBinder: an explicit subscription selection runs on the
// OAuth file alone (its launch unsets the environment tokens), so it binds to the account
// .claude.json names. The other routes are bound by BindRoute.
func (agentImpl) LaunchBinding(sel agents.RouteSelection) string {
	if sel.Route == RouteSubscription {
		return routes.AccountBinding(RouteSubscription, accountID())
	}
	return ""
}

// UsableRoutes implements agents.CredentialResolver: the subscription while claude's credentials
// file exists, anthropic-api while a key is stored, and each offered Bedrock route (its profile or
// grant is checked at create).
func (a agentImpl) UsableRoutes() []string {
	var out []string
	if _, err := os.Stat(credsPath()); err == nil {
		out = append(out, RouteSubscription)
	}
	if k, _ := loadAPIKey(); k != "" {
		out = append(out, RouteAPI)
	}
	for _, r := range a.OfferedRoutes() {
		if isBedrock(r) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out
}
