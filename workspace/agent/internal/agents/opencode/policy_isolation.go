package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Isolation of opencode under the credential policy (ADR 0138 D12, "Isolate shared daemons"):
// while the held policy restricts anything, opencode processes get only the credentials and
// providers it allows. Nothing here changes while nothing is restricted: the policy's absence is
// today's behaviour, exactly.
//
//   - Keys: a stored key whose routes the policy all forbids is not put in any opencode process's
//     environment (keyEnv), and the same provider variables inherited from the Agent's own
//     environment are removed from the shared serve's.
//   - Providers: the shared serve runs with an inline configuration whose enabled_providers lists
//     only the providers the policy allows. opencode.ai's login lives in opencode's own store, not
//     in the environment, so only this list keeps it out.
//   - A config file that sets enabled_providers, or a policies rule reaching provider.use, would
//     replace or widen that list (measured for the bedrock route, route.go configConflict), so
//     under a restriction it makes the shared serve, and a session in its directory, unavailable
//     with the reason.

// policyRestriction is the held policy when it restricts anything.
func policyRestriction() (routepolicy.Policy, bool) { return routepolicy.Hiding() }

// keyAllowed reports whether a key stored (or inherited) under env may reach opencode under p:
// some route it opens is allowed. A route allowed only because its endpoint is approved
// (AllowsAt with no host) does not count: a generic key name opens custom-api, and approving one
// host must not release every such name into the process.
func keyAllowed(p routepolicy.Policy, env string) bool {
	return slices.ContainsFunc(keyEnvRoutes(env), func(r string) bool { return p.AllowsAt(r, "") })
}

// providerKeyEnvs are every variable opencode reads a provider key from that af knows a route
// for: the stored providers' and the opencode.ai key.
func providerKeyEnvs() []string {
	out := []string{opencodeKeyEnv}
	for _, pk := range providerKeys {
		out = append(out, pk.key)
	}
	return out
}

// forbiddenInheritedKeys are the provider variables the Agent's own environment must not pass to
// opencode under p.
func forbiddenInheritedKeys(p routepolicy.Policy) []string {
	var out []string
	for _, env := range providerKeyEnvs() {
		if !keyAllowed(p, env) {
			out = append(out, env)
		}
	}
	return out
}

// allowedServeProviders is the enabled_providers list of the shared serve under p: the providers
// whose every possible payer here is allowed.
//   - A provider with a key af stores (anthropic, openai, …): its key route.
//   - opencode (Zen, free models) and opencode-go: whoever could pay there. The key pays on Zen or
//     Go, a signed-in account pays on its own route, and with neither a free model goes to Zen.
//     An account state not known yet counts as signed in: it might be.
//   - The deployment's engines: af-engine-llm.
//
// Bedrock is never listed (it runs on a session's own server, route_serve.go), nor a custom
// provider (custom-api is unclassified).
func allowedServeProviders(p routepolicy.Policy, keys map[string]string, pref string, acct oauthState, engines []string) []string {
	var out []string
	for id, pk := range providerKeys {
		if p.Allows(pk.route) {
			out = append(out, id)
		}
	}
	keyed := keys[opencodeKeyEnv] != "" && pref != UsageFree && pref != UsageOwn
	account := (acct.connected || !acct.known) && pref != UsageFree && pref != UsageOwn
	payers := func(keyRoute string, freeRoute string) []string {
		var r []string
		if keyed {
			r = append(r, keyRoute)
		}
		if account {
			r = append(r, routes.RouteOpencodeAccount)
		}
		if len(r) == 0 && freeRoute != "" {
			r = append(r, freeRoute)
		}
		return r
	}
	all := func(rs []string) bool {
		return len(rs) > 0 && !slices.ContainsFunc(rs, func(r string) bool { return !p.Allows(r) })
	}
	if pref != UsageOwn && all(payers(routes.RouteOpencodeZen, routes.RouteOpencodeZen)) {
		out = append(out, "opencode")
	}
	if pref != UsageOwn && pref != UsageFree && all(payers(routes.RouteOpencodeGo, "")) {
		out = append(out, "opencode-go")
	}
	if p.Allows(routes.RouteAFEngineLLM) {
		out = append(out, engines...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// servePolicyEnv is what the shared serve's environment changes under a restriction: the
// inherited provider variables the policy forbids removed, the config sources that could replace
// the pin unset, and the inline pin added. ok is false while nothing is restricted.
func servePolicyEnv(base []string) (env []string, ok bool) {
	return servePolicyEnvFrom(base, readView())
}

func servePolicyEnvFrom(base []string, v policyView) (env []string, ok bool) {
	p, restricted := v.p, v.restricted
	if !restricted {
		return base, false
	}
	keys, pref, acct, custom := v.keys, v.pref, v.acct, v.custom
	engines := v.engines
	cfg, _ := json.Marshal(pinConfig(map[string]any{}, p, keys, pref, acct, engines, custom))
	env = withoutEnv(base, append(forbiddenInheritedKeys(p), routeUnset...)...)
	env = withoutEnv(env, routeConfigKey)
	return append(env, routeConfigKey+"="+string(cfg)), true
}

// RunPolicyEnv isolates a headless `opencode run` (the assistant chat, the title, reply and other
// one-shots) under a restriction the way servePolicyEnv isolates the shared serve: the inherited
// provider variables the policy forbids and the config sources that could replace the pin
// removed, and enabled_providers merged into the run's own inline configuration, so opencode's
// own calls (its small model, its titles) stay on the allowed providers too. A config file the
// run reads, the global ones or those of dir and above, that would widen the pin refuses the run
// (ErrPolicyConfig), as for a session. env is the run's whole environment, the stored keys already
// filtered (keyEnv); while nothing is restricted it is returned unchanged.
func RunPolicyEnv(env []string, dir string) ([]string, error) {
	v := readView()
	p, restricted := v.p, v.restricted
	if !restricted {
		return env, nil
	}
	if err := policyConfigErrFrom(append(globalConfigFiles(), projectConfigFiles(session.Meta{Dir: dir})...), v); err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	if v := envValue(env, routeConfigKey); strings.TrimSpace(v) != "" {
		if json.Unmarshal([]byte(v), &cfg) != nil || cfg == nil {
			return nil, fmt.Errorf("%w: its inline configuration cannot be read, so its providers cannot be pinned", ErrPolicyConfig)
		}
	}
	pinConfig(cfg, p, v.keys, v.pref, v.acct, v.engines, v.custom)
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	out := withoutEnv(env, append(forbiddenInheritedKeys(p), append(routeUnset, routeConfigKey)...)...)
	return append(out, routeConfigKey+"="+string(b)), nil
}

// allowedCustom is the custom provider's connection when the policy allows its route on the host of
// this very snapshot (approved endpoint, or the route allowed outright), nil otherwise. It judges
// the snapshot's URL, not the registered reader's current one, so what is approved is what is pinned.
func allowedCustom(p routepolicy.Policy, engines []string, custom *secrets.CustomAPIConn) *secrets.CustomAPIConn {
	if c := customConn(custom, engines); c != nil && p.AllowsAt(routes.RouteCustomAPI, routepolicy.HostOf(c.URL)) {
		return c
	}
	return nil
}

// serveProviders is allowedServeProviders plus the custom provider while its endpoint is approved.
func serveProviders(p routepolicy.Policy, keys map[string]string, pref string, acct oauthState, engines []string, custom *secrets.CustomAPIConn) []string {
	out := allowedServeProviders(p, keys, pref, acct, engines)
	if c := allowedCustom(p, engines, custom); c != nil {
		out = append(out, strings.TrimSpace(c.Provider))
		slices.Sort(out)
		out = slices.Compact(out)
	}
	return out
}

// pinConfig sets the pin into an inline configuration: enabled_providers, and for an approved
// custom provider its base URL (provider.<id>.options.baseURL), so a call on it goes to the host
// the policy approved. It returns cfg.
func pinConfig(cfg map[string]any, p routepolicy.Policy, keys map[string]string, pref string, acct oauthState, engines []string, custom *secrets.CustomAPIConn) map[string]any {
	cfg["enabled_providers"] = nonNil(serveProviders(p, keys, pref, acct, engines, custom))
	c := allowedCustom(p, engines, custom)
	if c == nil {
		return cfg
	}
	sub := func(m map[string]any, k string) map[string]any {
		if v, ok := m[k].(map[string]any); ok {
			return v
		}
		v := map[string]any{}
		m[k] = v
		return v
	}
	id := strings.TrimSpace(c.Provider)
	// A base URL the run's own configuration carries for this provider (on a model or a variant, in
	// either provider block) would win over the provider-level pin: drop it.
	for _, block := range []string{"provider", "providers"} {
		if m, ok := cfg[block].(map[string]any); ok {
			stripBaseURL(m[id])
		}
	}
	sub(sub(sub(cfg, "provider"), id), "options")["baseURL"] = strings.TrimSpace(c.URL)
	return cfg
}

// stripBaseURL removes every base URL key below v.
func stripBaseURL(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if strings.EqualFold(k, "baseURL") {
				delete(x, k)
				continue
			}
			stripBaseURL(e)
		}
	case []any:
		for _, e := range x {
			stripBaseURL(e)
		}
	}
}

// hasBaseURL reports whether a base URL key appears anywhere in raw: on the provider, a model, a
// variant, whichever block shape (provider or providers) the file uses.
func hasBaseURL(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if strings.EqualFold(k, "baseURL") || walk(e) {
					return true
				}
			}
		case []any:
			for _, e := range x {
				if walk(e) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

// customPinDigest identifies the custom pin for the credential generation: a changed URL or
// provider is another pin, so the serve is restarted onto it. The key is not part of it (it is not
// in the configuration).
func customPinDigest(p routepolicy.Policy, engines []string, custom *secrets.CustomAPIConn) string {
	c := allowedCustom(p, engines, custom)
	if c == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(c.Provider) + "\x00" + strings.TrimSpace(c.URL)))
	return hex.EncodeToString(sum[:6])
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// pinConflict names the first config file among files, and its key, that would replace or widen
// an inline enabled_providers: an enabled_providers of its own, or a policies rule that can reach
// provider.use. A file that cannot be parsed is judged by whether one of the keys appears in it.
func pinConflict(files []string, customProvider string) (file, key string) {
	set := func(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var top map[string]json.RawMessage
		if json.Unmarshal(stripJSONC(b), &top) != nil {
			ks := []string{"enabled_providers", "policies"}
			if customProvider != "" {
				ks = append(ks, "baseURL", "baseUrl")
			}
			for _, k := range ks {
				if strings.Contains(string(b), `"`+k+`"`) {
					return f, k
				}
			}
			continue
		}
		if v, ok := top["enabled_providers"]; ok && set(v) {
			return f, "enabled_providers"
		}
		if v, ok := top["policies"]; ok && providerPolicy(v) {
			return f, "policies (provider.use)"
		}
		if customProvider != "" && overridesBaseURL(top, customProvider) {
			return f, "provider." + customProvider + ".options.baseURL"
		}
		var exp map[string]json.RawMessage
		if v, ok := top["experimental"]; ok && json.Unmarshal(v, &exp) == nil {
			if pv, ok := exp["policies"]; ok && providerPolicy(pv) {
				return f, "experimental.policies (provider.use)"
			}
		}
	}
	return "", ""
}

// overridesBaseURL reports whether a config sets a base URL anywhere under provider id, in the
// provider block or the providers block: the inline pin's URL is the one an approval covers, so a
// file may not move it, on the provider or on a model of it.
func overridesBaseURL(top map[string]json.RawMessage, id string) bool {
	for _, block := range []string{"provider", "providers"} {
		raw, ok := top[block]
		if !ok {
			continue
		}
		var byID map[string]json.RawMessage
		if json.Unmarshal(raw, &byID) != nil {
			if hasBaseURL(raw) { // another shape: judged whole, the safe direction
				return true
			}
			continue
		}
		if sub, ok := byID[id]; ok && hasBaseURL(sub) {
			return true
		}
	}
	return false
}

// ErrPolicyConfig marks opencode made unavailable by a config file that would override the
// credential policy's provider pin.
var ErrPolicyConfig = fmt.Errorf("opencode cannot run under the credential policy")

// policyConfigErr is the refusal of running opencode on files under a restriction, nil when none
// conflicts or nothing is restricted.
func policyConfigErr(files []string) error { return policyConfigErrFrom(files, readView()) }

// policyConfigErrFrom is policyConfigErr on a given view. The URL check covers only the provider
// the pin actually sets: a stored connection the policy does not approve pins nothing, so it must
// not stop another route's launch.
func policyConfigErrFrom(files []string, v policyView) error {
	p, restricted := v.p, v.restricted
	if !restricted {
		return nil
	}
	pin := ""
	if c := allowedCustom(p, v.engines, v.custom); c != nil {
		pin = strings.TrimSpace(c.Provider)
	}
	if f, k := pinConflict(files, pin); f != "" {
		return fmt.Errorf("%w: %s sets %s, which would widen the providers the policy allows; remove it", ErrPolicyConfig, f, k)
	}
	return nil
}

// customManagedOnlyErr refuses a launch of m on the approved custom endpoint everywhere but a
// Managed session on the shared serve with the pinned config root. Only that serve carries the
// inline pin (enabled_providers and the provider's base URL) and has its config files checked; a
// Terminal pane, a private route server or a standalone server starts without it. A process whose
// config root is not the pinned one reads files nobody checked, so readView withholds the pin
// there, whatever model a launch names (the serve is shared), and a launch on the custom provider
// is refused here. managed is true for a launch through the shared serve's driver. Nil while
// nothing is restricted, while the endpoint is not approved, or for any other provider.
func customManagedOnlyErr(m session.Meta, managed bool) error {
	return customManagedOnlyErrFrom(m, managed, readView())
}

func customManagedOnlyErrFrom(m session.Meta, managed bool, v policyView) error {
	if !v.restricted {
		return nil
	}
	c := allowedCustom(v.p, v.engines, v.custom)
	if c == nil {
		c = allowedCustom(v.p, v.engines, v.foreign)
	}
	if c == nil {
		return nil
	}
	prov, _, ok := splitModel(m.Model)
	if !ok || prov != strings.TrimSpace(c.Provider) {
		return nil
	}
	switch {
	case !managed:
		return fmt.Errorf("%w: the approved custom endpoint (custom-api) is available to Managed sessions only; this launch has no provider pin", ErrPolicyConfig)
	case v.custom == nil:
		return fmt.Errorf("%w: the approved custom endpoint (custom-api) needs the opencode config root to be %s (XDG_CONFIG_HOME differs), the only one whose files are checked",
			ErrPolicyConfig, filepath.Dir(paths.OpencodeConfigDir()))
	}
	return nil
}
