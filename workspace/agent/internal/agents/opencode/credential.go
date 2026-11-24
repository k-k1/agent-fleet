package opencode

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var (
	_ agents.CredentialResolver = agentImpl{}
	_ agents.PerCallRouter      = agentImpl{}
	_ agents.ModelRouteLister   = agentImpl{}
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

// keyEnvRoutes are the routes a key stored under env opens (usableOf's mapping): Zen and Go for
// the opencode.ai key, the provider's route for a provider key, custom-api for any other name.
// KeyEnvRoutes is keyEnvRoutes for the member's provider-key cards (ADR 0138 D16), which list the
// keys of this store by the routes they open.
func KeyEnvRoutes(env string) []string { return keyEnvRoutes(env) }

// KeyListed reports whether a key stored under env may be listed, entered and injected under p:
// some route it opens is allowed outright, an endpoint approval not counting (keyAllowed).
func KeyListed(p routepolicy.Policy, env string) bool { return keyAllowed(p, env) }

// KeyEnvOpencode is the opencode.ai Zen/Go key's name: it belongs to opencode.ai's own card, not
// to a provider's.
const KeyEnvOpencode = opencodeKeyEnv

func keyEnvRoutes(env string) []string {
	if env == opencodeKeyEnv {
		return []string{routes.RouteOpencodeZen, routes.RouteOpencodeGo}
	}
	if r := keyRoutes()[env]; r != "" {
		return []string{r}
	}
	return []string{routes.RouteCustomAPI}
}

// keyRoutes is providerKeys the other way round: the route a stored key opens.
func keyRoutes() map[string]string {
	out := map[string]string{}
	for _, p := range providerKeys {
		out[p.key] = p.route
	}
	return out
}

// policyView is one reading of everything a launch decision rests on. The serve's start takes it
// once and builds its file check, its inline pin and its generation from it, so an update of the
// connection between those steps cannot approve one URL and pin another.
//
// The held policy and the engine provider ids are part of it, and the credential store is read only
// while something is restricted: without a restriction the launch path must not wait on the store.
type policyView struct {
	p          routepolicy.Policy
	restricted bool
	engines    []string
	keys       map[string]string
	pref       string
	acct       oauthState
	custom     *secrets.CustomAPIConn
}

func readView() policyView {
	p, restricted := policyRestriction()
	v := policyView{p: p, restricted: restricted}
	if restricted {
		v.engines = engineProviderIDs()
		v.keys, v.pref, v.acct, v.custom = credentialView()
	}
	return v
}

// credentialView is what the resolution reads: the stored keys, the billing choice, the
// opencode.ai account as last seen (no daemon round trip) and the custom provider's connection
// (nil without one), all from one store read. A seam for tests.
var credentialView = func() (keys map[string]string, pref string, account oauthState, custom *secrets.CustomAPIConn) {
	if s, err := secrets.Load(); err == nil {
		keys, custom = s.Opencode, s.CustomAPI
	}
	return keys, UsagePref(), oauthCached(), custom
}

// reservedProviders are provider ids a custom connection may not take: each already has a route of
// its own, so pinning a URL under one would redirect that route's calls.
func reservedProviders(engines []string) []string {
	out := append([]string{"opencode", "opencode-go", bedrockProvider}, engines...)
	for id := range providerKeys {
		out = append(out, id)
	}
	return out
}

// customConn is the custom provider's connection as far as the policy may rely on it: nil unless
// it names a provider id no other route owns and a URL. Every reader (binding, endpoint host,
// inline pin, config-file refusal) goes through it, so they agree on whether a connection exists.
func customConn(c *secrets.CustomAPIConn, engines []string) *secrets.CustomAPIConn {
	if c == nil || strings.TrimSpace(c.Provider) == "" || strings.TrimSpace(c.URL) == "" ||
		slices.Contains(reservedProviders(engines), strings.TrimSpace(c.Provider)) {
		return nil
	}
	return c
}

// CustomConnUsable reports whether c would be honoured as the custom provider's connection: it
// names a provider id no other route owns, and a URL. The writer uses it so a connection that
// every reader would ignore is refused at entry instead of stored silently.
func CustomConnUsable(c *secrets.CustomAPIConn) bool {
	return customConn(c, engineProviderIDs()) != nil
}

// The custom provider's endpoint is a per-connection route: under a restriction it is allowed only
// while its host is an approved self-hosted endpoint (ADR 0138 §2). Approval is per route, so a
// launch on any other custom provider must not resolve to the route (resolveModel).
func init() {
	routepolicy.RegisterEndpoint(routes.RouteCustomAPI, func() string {
		_, _, _, c := credentialView()
		if c = customConn(c, engineProviderIDs()); c != nil {
			return routepolicy.HostOf(c.URL)
		}
		return ""
	})
}

// ResolveLaunch implements agents.CredentialResolver: the route of the model the session is
// launched on, from its provider id (ADR 0138 D2), and the credential the stored keys or the
// account login give that provider. A session can change model later; this is the launch. A
// launch on a provider whose payer cannot be told resolves to nothing rather than to a guess, and
// so does one on opencode's own default model (no model named): opencode falls back from a
// configured model that is unavailable or disabled, and an agent's own model (JSON or Markdown,
// global or project) overrides it, so nothing read at launch proves which model will run.
func (agentImpl) ResolveLaunch(m session.Meta) routes.Launch {
	keys, pref, acct, custom := credentialView()
	return resolveModel(m.Model, keys, pref, acct, engineProviderIDs(), custom)
}

// ResolveDefaultLaunch implements agents.DefaultLaunchResolver: the launch record of a session that
// names no model, which ResolveLaunch leaves unresolved. Only the running serve's answer for the
// session's working directory counts (default_model.go), and only for the one case it proves.
func (agentImpl) ResolveDefaultLaunch(m session.Meta, start time.Time) routes.Launch {
	return resolveDefaultModelLaunch(m, start)
}

// ModelLaunch is ResolveLaunch for an opencode run outside any session (the assistant chat and the
// one-shots), on model.
func ModelLaunch(model string) routes.Launch {
	keys, pref, acct, custom := credentialView()
	return resolveModel(model, keys, pref, acct, engineProviderIDs(), custom)
}

// ModelRoutes implements agents.ModelRouteLister for opencode.ai's own providers: Zen (or Go) by
// key, or the Console account. They share one destination, so a policy that forbids all of them
// forbids the model whichever payer would pay. A billing choice that keeps opencode.ai out gives
// none: that launch does not run there at all. Provider models (anthropic/…) stay nil: without a
// stored key they may run on opencode's own auth-store login, which no policy route judges, so a
// forbid-all over their single catalogued route would be unsound.
func (agentImpl) ModelRoutes(model string) []string {
	prov, _, ok := splitModel(model)
	_, pref, _, _ := credentialView()
	if !ok || pref == UsageOff || pref == UsageOwn {
		return nil
	}
	switch prov {
	case "opencode":
		return []string{routes.RouteOpencodeZen, routes.RouteOpencodeAccount}
	case "opencode-go":
		return []string{routes.RouteOpencodeGo, routes.RouteOpencodeAccount}
	}
	return nil
}

func resolveModel(model string, keys map[string]string, pref string, acct oauthState, engines []string, custom *secrets.CustomAPIConn) routes.Launch {
	p, restricted := policyRestriction()
	return resolveModelUnder(p, restricted, model, keys, pref, acct, engines, custom)
}

// resolveModelUnder is resolveModel on a policy the caller already read, so the launch and the
// pin built beside it (RunPin) cannot disagree about it.
func resolveModelUnder(p routepolicy.Policy, restricted bool, model string, keys map[string]string, pref string, acct oauthState, engines []string, custom *secrets.CustomAPIConn) routes.Launch {
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
	// A custom provider. Unrestricted, the URL is not pinned, so no binding claims it: the route
	// alone is recorded, as before. Under a restriction with a stored connection (customConn, from
	// the credentialView snapshot) only the provider the URL belongs to is on that endpoint; any
	// other custom provider resolves to nothing, since the route's approval is of the one host.
	if !restricted {
		return routes.Launch{Route: routes.RouteCustomAPI}
	}
	if c := customConn(custom, engines); c != nil {
		if prov != strings.TrimSpace(c.Provider) {
			return routes.Launch{}
		}
		if allowedCustom(p, engines, custom) == nil {
			return routes.Launch{Route: routes.RouteCustomAPI}
		}
		// The binding is of the endpoint alone: the stored key is not handed to the process (the
		// provider's own configuration supplies it), so a binding that covered it would claim a
		// credential nothing here proves.
		return routes.Launch{Route: routes.RouteCustomAPI, Binding: credbind.Endpoint(routes.RouteCustomAPI, c.URL, "")}
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
	keys, pref, acct, _ := credentialView()
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
