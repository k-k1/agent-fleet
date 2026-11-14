package opencode

import (
	"encoding/json"
	"os"
	"slices"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.CredentialResolver = agentImpl{}

// providerKeys maps opencode's provider ids to the route and the stored key (Settings >
// Connections, secrets.Data.Opencode) a call on that provider is paid by. The names are the
// Console's presets (OpencodeCard.tsx OC_PRESETS). Which provider id opencode gives Sakana is
// not measured; a provider missing here resolves to custom-api.
var providerKeys = map[string]struct{ route, key string }{
	"anthropic":  {routes.RouteAnthropicAPI, "ANTHROPIC_API_KEY"},
	"openai":     {routes.RouteOpenAIAPI, "OPENAI_API_KEY"},
	"openrouter": {routes.RouteOpenRouterAPI, "OPENROUTER_API_KEY"},
	"google":     {routes.RouteGeminiAPI, "GEMINI_API_KEY"},
	"sakana":     {routes.RouteSakanaAPI, "SAKANA_API_KEY"},
}

// keyRoutes is providerKeys the other way round: the route a stored key opens.
func keyRoutes() map[string]string {
	out := map[string]string{}
	for _, p := range providerKeys {
		out[p.key] = p.route
	}
	return out
}

// credentialView is what the resolution reads: the stored keys, the billing choice and the
// opencode.ai account. A seam for tests.
var credentialView = func() (keys map[string]string, pref string, account oauthState) {
	if s, err := secrets.Load(); err == nil {
		keys = s.Opencode
	}
	return keys, UsagePref(), oauthStatus()
}

// ResolveLaunch implements agents.CredentialResolver: the route of the model the session is
// launched on, from its provider id (ADR 0138 D2), and the credential the stored keys or the
// account login give that provider. A session can change model later; this is the launch. A
// launch on opencode's own default model (no model named), or on a provider whose payer cannot
// be told, resolves to nothing rather than to a guess.
func (agentImpl) ResolveLaunch(m session.Meta) routes.Launch {
	keys, pref, acct := credentialView()
	return resolveModel(m.Model, keys, pref, acct, engineProviderIDs())
}

func resolveModel(model string, keys map[string]string, pref string, acct oauthState, engines []string) routes.Launch {
	prov, _, ok := splitModel(model)
	if !ok || pref == UsageOff {
		return routes.Launch{}
	}
	if slices.Contains(engines, prov) {
		return routes.Launch{Route: routes.RouteAFEngineLLM, Binding: routes.RouteAFEngineLLM + "@llm"}
	}
	if p, ok := providerKeys[prov]; ok {
		if keys[p.key] == "" {
			// No stored key: opencode's own auth store may hold a login for the provider (its
			// `auth login`), which would be another class altogether.
			return routes.Launch{}
		}
		return routes.Launch{Route: p.route, Binding: routes.KeyBinding(p.key, keys[p.key])}
	}
	switch prov {
	case "opencode", "opencode-go":
		route := routes.RouteOpencodeZen
		if prov == "opencode-go" {
			route = routes.RouteOpencodeGo
		}
		if pref == UsageFree {
			// The zero-auth tier: no credential, and OPENCODE_API_KEY is dropped from the launch.
			return routes.Launch{Route: route}
		}
		key, signedIn := keys[opencodeKeyEnv], acct.connected && acct.id != ""
		switch {
		case key != "" && signedIn:
			// Either may pay (opencode gates paid models on the OR of the two); which one did is
			// not measured.
			return routes.Launch{Route: route}
		case key != "":
			return routes.Launch{Route: route, Binding: routes.KeyBinding(opencodeKeyEnv, key)}
		case signedIn:
			// The Console account pays for its org's models: the account route.
			return routes.Launch{Route: routes.RouteOpencodeAccount, Binding: routes.AccountBinding(routes.RouteOpencodeAccount, acct.id)}
		}
		// A free model with no credential still goes to opencode.ai; the ledger files it under Zen.
		return routes.Launch{Route: route}
	case bedrockProvider:
		// Bedrock runs only on an explicit selection (the route's own launch); without one the
		// AWS credential chain decides, which cannot be told.
		return routes.Launch{}
	}
	return routes.Launch{Route: routes.RouteCustomAPI}
}

// engineProviderIDs are the provider ids of the deployment's engines in opencode's config (the
// entries af wrote, carrying engineProviderMarker).
func engineProviderIDs() []string {
	b, err := os.ReadFile(engineConfigPath())
	if err != nil {
		return nil
	}
	var root struct {
		Providers map[string]map[string]any `json:"provider"`
	}
	if json.Unmarshal(b, &root) != nil {
		return nil
	}
	var out []string
	for id, p := range root.Providers {
		if p[engineProviderMarker] == true {
			out = append(out, id)
		}
	}
	return out
}

// UsableRoutes implements agents.CredentialResolver: every route the stored keys, the account
// login, the billing choice and the engine config open now, and bedrock while it is offered.
func (a agentImpl) UsableRoutes() []string {
	keys, pref, acct := credentialView()
	out := usableOf(keys, pref, acct, HasEngineProviders())
	out = append(out, a.OfferedRoutes()...)
	slices.Sort(out)
	return slices.Compact(out)
}

func usableOf(keys map[string]string, pref string, acct oauthState, engine bool) []string {
	if pref == UsageOff {
		return nil
	}
	var out []string
	byKey := keyRoutes()
	noOpencodeAI := pref == UsageFree || pref == UsageOwn
	for name, v := range keys {
		switch {
		case v == "":
		case name == opencodeKeyEnv:
			if !noOpencodeAI {
				out = append(out, routes.RouteOpencodeZen, routes.RouteOpencodeGo)
			}
		case byKey[name] != "":
			out = append(out, byKey[name])
		default:
			out = append(out, routes.RouteCustomAPI)
		}
	}
	if acct.connected && !noOpencodeAI {
		out = append(out, routes.RouteOpencodeAccount)
	}
	if pref == UsageFree {
		out = append(out, routes.RouteOpencodeZen)
	}
	if engine {
		out = append(out, routes.RouteAFEngineLLM)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
