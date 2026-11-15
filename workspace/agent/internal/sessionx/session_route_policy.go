package sessionx

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The credential policy at the Agent's create and start points (ADR 0138 D11, D12): the
// authoritative check inside the Workspace, since some creates never pass the Control Plane.
// Nothing here changes a launch while the held policy restricts nothing: the only answer then is
// the store's own Gate (a start that could record no floor waits for the CP).

// routePolicyStore is the policy the points judge by. A seam for tests.
var routePolicyStore = routepolicy.Shared

// policyGate is the refusal of new work while the policy cannot be confirmed or recorded, nil
// otherwise. It is asked before anything else, restricted or not.
func policyGate() *agents.RouteRefusal {
	if err := routePolicyStore().Gate(); err != nil {
		return &agents.RouteRefusal{Status: http.StatusServiceUnavailable, Code: routes.CodePolicyUnconfirmed, Message: err.Error()}
	}
	return nil
}

// restrictingPolicy is the held policy when it restricts anything, ok false when it does not (or
// none is held): every route is then allowed and the empty-selection rule does not apply.
func restrictingPolicy() (routepolicy.Policy, bool) {
	st := routePolicyStore().State()
	return st.Policy, st.Held && st.Policy.Restricted
}

// newLaunchPolicyRefusal judges a launch that starts something new (a create, a fork, a recreate,
// a move's target) under the held policy. An explicit selection is judged on its route. An empty
// one follows D11b: a kind that resolves to exactly one route (agents.OneRouteResolver) is judged
// on that route, resolved now; any other kind, or a resolution that cannot tell, must name its
// route. The first allowed route is never picked on the member's behalf. The returned launch is
// the one judged, for the caller to record, nil when nothing was resolved here.
func newLaunchPolicyRefusal(kind string, sel *routes.Selection, m session.Meta) (*agents.RouteRefusal, *routes.Launch) {
	if ref := policyGate(); ref != nil {
		return ref, nil
	}
	p, restricted := restrictingPolicy()
	if !restricted {
		return nil, nil
	}
	if sel != nil {
		if r := sel.Normalize().Route; !p.Allows(r) {
			return forbiddenRefusal(kind, r, p), nil
		}
		return nil, nil
	}
	a, _ := RegisteredAgent(NormalizeKind(kind))
	if _, ok := a.(agents.OneRouteResolver); !ok {
		return requiredRefusal(kind, p, ""), nil
	}
	l, _ := withinBudget(kind+" ResolveLaunch", func() routes.Launch { return a.(agents.CredentialResolver).ResolveLaunch(m) })
	switch {
	case l.Route == "":
		return requiredRefusal(kind, p, "its current login cannot be told"), nil
	case !p.Allows(l.Route):
		return forbiddenRefusal(kind, l.Route, p), nil
	}
	return nil, &l
}

// storedLaunchPolicyRefusal judges a start of a stored session (a start, a resume, an
// execution-method switch, a route switch's candidate) on launch-time evidence, never by running
// the create rule again (D11): an explicit selection on its route; otherwise the launch record.
//   - No record (a session from before it), or one that could not tell its route: nothing proves
//     what the session would run on, so it is refused; the member chooses a route or recreates.
//   - A record on a forbidden route: refused.
//   - A record whose credential the kind would no longer launch on (another route, or another
//     binding where the record has one): a replacement (D4), refused, never moved. For a kind
//     whose route follows each call (agents.PerCallRouter) the session's current model must
//     resolve to an allowed route instead, since its model may change after the launch.
func storedLaunchPolicyRefusal(m session.Meta) *agents.RouteRefusal {
	if ref := policyGate(); ref != nil {
		return ref
	}
	p, restricted := restrictingPolicy()
	if !restricted {
		return nil
	}
	if m.Route != nil {
		if r := m.Route.Normalize().Route; !p.Allows(r) {
			return forbiddenRefusal(m.Kind, r, p)
		}
		return nil
	}
	rec := m.LaunchRoute
	if rec == nil || rec.Route == "" {
		return requiredRefusal(m.Kind, p, "it has no launch-time record of its route; choose a route explicitly or recreate it")
	}
	if !p.Allows(rec.Route) {
		return forbiddenRefusal(m.Kind, rec.Route, p)
	}
	a, _ := RegisteredAgent(NormalizeKind(m.Kind))
	cr, ok := a.(agents.CredentialResolver)
	if !ok {
		return nil
	}
	cur, _ := withinBudget(m.Kind+" ResolveLaunch", func() routes.Launch { return cr.ResolveLaunch(m) })
	if _, per := a.(agents.PerCallRouter); per {
		switch {
		case cur.Route == "":
			return requiredRefusal(m.Kind, p, "the route of its current model cannot be told")
		case !p.Allows(cur.Route):
			return forbiddenRefusal(m.Kind, cur.Route, p)
		}
		return nil
	}
	if cur.Route != rec.Route || rec.Binding != "" && cur.Binding != rec.Binding {
		return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodePolicyForbidden,
			Message: fmt.Sprintf("this %s session was launched on %s, and the credential it would start on now is another one; "+
				"under the credential policy it is not moved to a different credential: recreate it to start on the current one", m.Kind, rec.Route)}
	}
	return nil
}

func forbiddenRefusal(kind, route string, p routepolicy.Policy) *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodePolicyForbidden,
		Message: fmt.Sprintf("the credential policy does not allow %s on %s; %s", kind, route, allowedPhrase(kind, p))}
}

// requiredRefusal is route_required. why, when set, says what is missing. It tells the member
// what to do: choose an allowed route, and for a kind whose route follows its model, name a
// model whose provider is allowed; with a default route for the harness (D16) the choice is
// made once.
func requiredRefusal(kind string, p routepolicy.Policy, why string) *agents.RouteRefusal {
	msg := "the credential policy requires choosing a route for " + kind
	if why != "" {
		msg += " (" + why + ")"
	}
	a, _ := RegisteredAgent(NormalizeKind(kind))
	if _, per := a.(agents.PerCallRouter); per {
		msg += "; name a model whose provider the policy allows, or set a default route for " + kind
	} else {
		msg += "; choose one of its allowed routes, or set a default route for " + kind
	}
	return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodePolicyRequired, Message: msg + "; " + allowedPhrase(kind, p)}
}

// allowedPhrase lists the kind's routes the policy allows. Which layer decided is not named:
// members are not told (D10a).
func allowedPhrase(kind string, p routepolicy.Policy) string {
	a, _ := RegisteredAgent(NormalizeKind(kind))
	own := append(slices.Clone(routesOf(kind)), agents.FixedRoutesOf(a)...)
	var allowed []string
	for _, r := range own {
		if p.Allows(r) && !slices.Contains(allowed, r) {
			allowed = append(allowed, r)
		}
	}
	if len(allowed) == 0 {
		return "none of " + kind + "'s routes is allowed"
	}
	slices.Sort(allowed)
	return "allowed: " + strings.Join(allowed, ", ")
}
