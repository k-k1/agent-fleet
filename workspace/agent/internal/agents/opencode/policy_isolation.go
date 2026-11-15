package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
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
// some route it opens is allowed.
func keyAllowed(p routepolicy.Policy, env string) bool {
	return slices.ContainsFunc(keyEnvRoutes(env), p.Allows)
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
	p, restricted := policyRestriction()
	if !restricted {
		return base, false
	}
	keys, pref, acct := credentialView()
	cfg, _ := json.Marshal(map[string]any{
		"enabled_providers": nonNil(allowedServeProviders(p, keys, pref, acct, engineProviderIDs())),
	})
	env = withoutEnv(base, append(forbiddenInheritedKeys(p), routeUnset...)...)
	env = withoutEnv(env, routeConfigKey)
	return append(env, routeConfigKey+"="+string(cfg)), true
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
func pinConflict(files []string) (file, key string) {
	set := func(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var top map[string]json.RawMessage
		if json.Unmarshal(stripJSONC(b), &top) != nil {
			for _, k := range []string{"enabled_providers", "policies"} {
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
		var exp map[string]json.RawMessage
		if v, ok := top["experimental"]; ok && json.Unmarshal(v, &exp) == nil {
			if pv, ok := exp["policies"]; ok && providerPolicy(pv) {
				return f, "experimental.policies (provider.use)"
			}
		}
	}
	return "", ""
}

// ErrPolicyConfig marks opencode made unavailable by a config file that would override the
// credential policy's provider pin.
var ErrPolicyConfig = fmt.Errorf("opencode cannot run under the credential policy")

// policyConfigErr is the refusal of running opencode on files under a restriction, nil when none
// conflicts or nothing is restricted.
func policyConfigErr(files []string) error {
	if _, restricted := policyRestriction(); !restricted {
		return nil
	}
	if f, k := pinConflict(files); f != "" {
		return fmt.Errorf("%w: %s sets %s, which would widen the providers the policy allows; remove it", ErrPolicyConfig, f, k)
	}
	return nil
}
