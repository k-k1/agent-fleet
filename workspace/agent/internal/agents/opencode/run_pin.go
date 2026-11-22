package opencode

import (
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// RunPin is one reading of the credential policy and the stored connections for a headless
// `opencode run` (the assistant chat and the one-shots): the route and binding the call is
// judged and recorded on (Launch) and the isolation its process gets (Env) both come from it. Two
// readings could straddle a connection update, so a call recorded under URL A would be sent to
// the approved URL B; the policy's check compares route ids only and would not notice.
type RunPin struct {
	view policyView
	ok   bool
	// Launch is the route and binding of the model the run was pinned for.
	Launch routes.Launch
}

// PinRun reads the policy and the connections once and resolves model on that reading. The zero
// RunPin (a reading that could not be taken) refuses every Env.
func PinRun(model string) RunPin {
	v := readView()
	return RunPin{
		view:   v,
		ok:     true,
		Launch: resolveModelUnder(v.p, v.restricted, model, v.keys, v.pref, v.acct, v.engines, v.custom),
	}
}

// Env is RunPolicyEnv on this reading. Under a restriction it first checks the pin against the
// recorded launch: a custom endpoint binding that is not the pinned endpoint's is refused, so a
// call is never sent anywhere but where it was recorded.
func (r RunPin) Env(env []string, dir string) ([]string, error) {
	if !r.ok {
		return nil, fmt.Errorf("%w: the policy and connections could not be read, so the run cannot be pinned", ErrPolicyConfig)
	}
	if err := r.checkBinding(); err != nil {
		return nil, err
	}
	return runPolicyEnvFrom(r.view, env, dir)
}

func (r RunPin) checkBinding() error {
	v := r.view
	if !v.restricted || r.Launch.Route != routes.RouteCustomAPI || r.Launch.Binding == "" {
		return nil
	}
	c := allowedCustom(v.p, v.engines, v.custom)
	if c == nil || credbind.Endpoint(routes.RouteCustomAPI, strings.TrimSpace(c.URL), "") != r.Launch.Binding {
		return fmt.Errorf("%w: the custom endpoint this call was recorded on is not the one approved now", ErrPolicyConfig)
	}
	return nil
}
