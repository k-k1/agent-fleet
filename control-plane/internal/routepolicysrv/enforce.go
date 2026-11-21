package routepolicysrv

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The Control Plane's half of the credential policy's enforcement (ADR 0138 D12): it is the gate
// where it can see the request, before a Workspace is woken for it. The Agent is the authoritative
// gate: it judges again, and it alone can resolve an empty selection (D11), which depends on the
// Workspace's own credentials. Nothing here refuses anything while the tenant's policy restricts
// nothing.

// Refusal codes, equal to the Agent's routes.CodePolicy* values.
const (
	CodePolicyForbidden   = "route_forbidden"
	CodePolicyUnconfirmed = "policy_unconfirmed"
	// CodeAgentUnenforcing: the Workspace runs an Agent that does not enforce the policy, so a
	// restricted member's create is not handed to it.
	CodeAgentUnenforcing = "agent_policy_unsupported"
)

// AgentEnforceCapability is the value the Agent reports as "route_policy" in its /healthz when
// it enforces the policy at its create and start points. An Agent that reports nothing is from
// before enforcement.
const AgentEnforceCapability = "enforce-v1"

// RouteRefusal judges an explicit route selection of a create (a session, a schedule's save or
// fire) for a member of tenantID. route "" is an empty selection: the Agent judges it. When the
// policy cannot be computed, a create is refused only while the operator ceiling restricts the
// deployment, as the start environment is (PolicyEnv); otherwise the Agent's held copy decides.
func RouteRefusal(ctx context.Context, st store.Store, tenantID, route string) *APIError {
	route = strings.TrimSpace(route)
	if route == "" {
		return nil
	}
	p, err := MemberPolicyOf(ctx, st, tenantID)
	switch {
	case err != nil && (currentCeiling().rule.Restricted() || errors.Is(err, errCeilingUnsettled)):
		return &APIError{http.StatusServiceUnavailable, CodePolicyUnconfirmed,
			"the credential policy cannot be computed now, and this deployment restricts routes; try again shortly"}
	case err != nil || !p.Restricted:
		return nil
	case !slices.Contains(p.Allowed, route):
		return &APIError{http.StatusForbidden, CodePolicyForbidden, "the credential policy does not allow route " + route}
	}
	return nil
}

// Restricted reports whether the policy restricts anything for a member of tenantID. A policy
// that cannot be computed counts as restricting while the operator ceiling restricts.
func Restricted(ctx context.Context, st store.Store, tenantID string) bool {
	p, err := MemberPolicyOf(ctx, st, tenantID)
	if err != nil {
		return currentCeiling().rule.Restricted() || errors.Is(err, errCeilingUnsettled)
	}
	return p.Restricted
}

// AgentRefusal is the Upgrade rule (D12) for a restricted member (the caller asks Restricted
// first): an Agent that does not report AgentEnforceCapability is not trusted with the create.
// reported is the Agent's /healthz "route_policy" value, answered whether /healthz answered at all.
func AgentRefusal(reported string, answered bool) *APIError {
	switch {
	case !answered:
		return &APIError{http.StatusServiceUnavailable, CodePolicyUnconfirmed,
			"the Workspace's Agent did not answer, so whether it enforces the credential policy cannot be confirmed; try again shortly"}
	case reported != AgentEnforceCapability:
		return &APIError{http.StatusConflict, CodeAgentUnenforcing,
			"the Workspace runs an Agent from before the credential policy was enforced; restart the Workspace to upgrade it"}
	}
	return nil
}
