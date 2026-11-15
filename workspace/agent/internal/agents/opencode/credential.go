package opencode

import (
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var (
	_ agents.CredentialResolver = agentImpl{}
	_ agents.PerCallRouter      = agentImpl{}
)

// RoutesPerCall implements agents.PerCallRouter: a call's route is its model's provider.
func (agentImpl) RoutesPerCall() {}

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
// opencode.ai account as last seen (no daemon round trip). A seam for tests.
var credentialView = func() (keys map[string]string, pref string, account oauthState) {
	if s, err := secrets.Load(); err == nil {
		keys = s.Opencode
	}
	return keys, UsagePref(), oauthCached()
}

// ResolveLaunch implements agents.CredentialResolver: the route of the model the session is
// launched on, from its provider id (ADR 0138 D2), and the credential the stored keys or the
// account login give that provider. A session can change model later; this is the launch. With no
// model named, the model opencode will choose (defaultModel). A launch whose model or payer
// cannot be told resolves to nothing rather than to a guess.
func (agentImpl) ResolveLaunch(m session.Meta) routes.Launch {
	keys, pref, acct := credentialView()
	return resolveModel(launchModel(m.Model, append(globalConfigFiles(), projectConfigFiles(m)...)), keys, pref, acct,
		engineProviderIDs())
}

// ModelLaunch is ResolveLaunch for an opencode run outside any session (the assistant chat and the
// one-shots), on model.
func ModelLaunch(model string) routes.Launch {
	keys, pref, acct := credentialView()
	// The chat runs in a working directory under the state directory whose own config (af's)
	// sets no model; the directories above it (the home directory's opencode.json among them)
	// are read like any project's.
	files := append(globalConfigFiles(), projectConfigFiles(session.Meta{Dir: paths.AgentStateDir()})...)
	return resolveModel(launchModel(model, files), keys, pref, acct, engineProviderIDs())
}

// launchModel is model when it names one, else defaultModel over the config files that apply.
func launchModel(model string, files []string) string {
	if _, _, ok := splitModel(model); ok {
		return model
	}
	return defaultModel(files)
}

// defaultModel is the model opencode will run a launch that names none on, or "" when that
// cannot be told. opencode's documented order is the --model flag (af passes none), then the
// config's "model", then the last model used, then the first by its internal priority. Only the
// config can be read with certainty:
//   - The config's "model", when the files that set one agree. Which of two disagreeing files
//     wins is opencode's merge order, not a contract, so disagreement resolves to nothing, and so
//     does a file that cannot be parsed but mentions "model".
//   - An agent (or a legacy mode) with a model of its own overrides it for that agent, so a file
//     that sets one resolves to nothing.
//   - Without a configured model, nothing: the last model used and opencode's priority depend on
//     the providers this launch's project and opencode's own credential store make available,
//     which nothing af holds proves at launch.
func defaultModel(files []string) string {
	set := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var top struct {
			Model *string                    `json:"model"`
			Agent map[string]json.RawMessage `json:"agent"`
			Mode  map[string]json.RawMessage `json:"mode"`
		}
		if json.Unmarshal(stripJSONC(b), &top) != nil {
			if strings.Contains(string(b), `"model"`) {
				return ""
			}
			continue
		}
		for _, entries := range []map[string]json.RawMessage{top.Agent, top.Mode} {
			for _, raw := range entries {
				var a struct {
					Model *string `json:"model"`
				}
				if json.Unmarshal(raw, &a) == nil && a.Model != nil && strings.TrimSpace(*a.Model) != "" {
					return ""
				}
			}
		}
		if top.Model != nil && strings.TrimSpace(*top.Model) != "" {
			set[strings.TrimSpace(*top.Model)] = true
		}
	}
	if len(set) != 1 {
		return ""
	}
	for m := range set {
		return m
	}
	return ""
}

func resolveModel(model string, keys map[string]string, pref string, acct oauthState, engines []string) routes.Launch {
	prov, _, ok := splitModel(model)
	if !ok || pref == UsageOff {
		return routes.Launch{}
	}
	if slices.Contains(engines, prov) {
		return routes.Launch{Route: routes.RouteAFEngineLLM, Binding: credbind.Named(routes.RouteAFEngineLLM, "llm")}
	}
	if p, ok := providerKeys[prov]; ok {
		if keys[p.key] == "" {
			// No stored key: opencode's own auth store may hold a login for the provider (its
			// `auth login`), which would be another class altogether.
			return routes.Launch{}
		}
		return routes.Launch{Route: p.route, Binding: credbind.Key(p.key, keys[p.key])}
	}
	switch prov {
	case "opencode", "opencode-go":
		return resolveOpencodeAI(prov, keys, pref, acct)
	case bedrockProvider:
		// Bedrock runs only on an explicit selection (the route's own launch); without one the
		// AWS credential chain decides, which cannot be told.
		return routes.Launch{}
	}
	return routes.Launch{Route: routes.RouteCustomAPI}
}

// resolveOpencodeAI resolves a model on opencode.ai's own providers. Who pays is proven only
// when exactly one payer can be: the Zen/Go key alone (the account known to be signed out), or
// the Console account alone (no key on the launch). The account state comes from the cached view
// (oauthCached), never a daemon round trip; an unknown state, both payers at once (opencode
// gates paid models on the OR of the two, and which one pays is not measured), or a billing
// choice that keeps opencode.ai out resolves to nothing, route included, since the two payers
// are different classes.
func resolveOpencodeAI(prov string, keys map[string]string, pref string, acct oauthState) routes.Launch {
	route := routes.RouteOpencodeZen
	if prov == "opencode-go" {
		route = routes.RouteOpencodeGo
	}
	if pref == UsageOwn || !acct.known {
		return routes.Launch{}
	}
	key := keys[opencodeKeyEnv]
	if pref == UsageFree {
		key = "" // the launch drops it (keyEnv)
	}
	signedIn := acct.connected
	switch {
	case key != "" && signedIn:
		return routes.Launch{}
	case key != "":
		return routes.Launch{Route: route, Binding: credbind.Key(opencodeKeyEnv, key)}
	case signedIn && acct.id != "":
		return routes.Launch{Route: routes.RouteOpencodeAccount, Binding: credbind.Account(routes.RouteOpencodeAccount, acct.id)}
	case signedIn:
		return routes.Launch{}
	case prov == "opencode":
		// No credential at all: a free model, which still goes to opencode.ai and which the
		// ledger files under Zen. It has no credential to fingerprint, so it gets the one fixed
		// binding of the keyless route (D5b: every row carries a binding).
		return routes.Launch{Route: routes.RouteOpencodeZen, Binding: FreeBinding}
	}
	return routes.Launch{}
}

// FreeBinding is the binding of opencode.ai's free models, which run with no credential at all.
var FreeBinding = credbind.Named(routes.RouteOpencodeZen, "free")

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
// login, the billing choice and the engine config open now, and bedrock while it is offered and
// the gateway holds a marked profile for it.
func (a agentImpl) UsableRoutes() []string {
	keys, pref, acct := credentialView()
	out := usableOf(keys, pref, acct, HasEngineProviders())
	for _, r := range a.OfferedRoutes() {
		if agents.GatewayHolds(r) {
			out = append(out, r)
		}
	}
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
