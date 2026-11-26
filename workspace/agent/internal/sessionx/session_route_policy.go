package sessionx

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

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

// modelKind reports whether kind runs on a model credential at all: it declares routes (a
// RouteAdapter) or resolves one (a CredentialResolver). A kind that runs no model (shell, ssm)
// is outside the credential policy entirely, its Gate included: a policy that cannot be confirmed
// must not stop a shell.
func modelKind(kind string) bool {
	a, ok := RegisteredAgent(NormalizeKind(kind))
	if !ok {
		return true // unknown: judged, never waved through
	}
	_, resolves := a.(agents.CredentialResolver)
	return resolves || len(routesOf(kind)) > 0
}

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
// one follows D11b: it is judged on the route the kind resolves it to now when the kind proves one,
// that is a kind that resolves to exactly one route per login or connection
// (agents.OneRouteResolver), or one whose route follows the model (agents.PerCallRouter) when the
// model it names resolves; any other kind (claude), an unnamed default model, or a resolution that
// cannot tell, must name its route. The first allowed route is never picked on the member's
// behalf. The returned launch is the one judged, for the caller to record, nil when nothing was
// resolved here.
//
// A Managed launch that names no model on a kind that can prove its default from the working
// directory (agents.DefaultLaunchResolver) is refused here as well, since no directory is known
// to a caller that judges early. A caller that can wait for the directory asks
// judgeNewLaunch instead and finishes with deferredDefaultRefusal.
func newLaunchPolicyRefusal(kind string, sel *routes.Selection, m session.Meta) (*agents.RouteRefusal, *routes.Launch) {
	ref, l, _ := judgeNewLaunch(kind, sel, m)
	return ref, l
}

// judgeNewLaunch is newLaunchPolicyRefusal that also says when the refusal it returns is only for
// want of the working directory (deferred): the launch is a Managed one on a kind that proves its
// default model from the directory, under a policy that allows the route such a proof can give.
// The caller then judges again with deferredDefaultRefusal once the directory is known, and the
// refusal returned here is the one that judgement falls back to.
func judgeNewLaunch(kind string, sel *routes.Selection, m session.Meta) (ref *agents.RouteRefusal, launch *routes.Launch, deferred bool) {
	if !modelKind(kind) {
		return nil, nil, false
	}
	if ref := policyGate(); ref != nil {
		return ref, nil, false
	}
	p, restricted := restrictingPolicy()
	if !restricted {
		return nil, nil, false
	}
	if sel != nil {
		if r := sel.Normalize().Route; !p.Allows(r) {
			return forbiddenRefusal(kind, r, p), nil, false
		}
		return nil, nil, false
	}
	a, _ := RegisteredAgent(NormalizeKind(kind))
	_, oneRoute := a.(agents.OneRouteResolver)
	_, perCall := a.(agents.PerCallRouter)
	cr, resolves := a.(agents.CredentialResolver)
	if !resolves || !oneRoute && !perCall {
		return requiredRefusal(kind, p, ""), nil, false
	}
	l, _ := withinBudget(kind+" ResolveLaunch", func() routes.Launch { return cr.ResolveLaunch(m) })
	switch {
	case l.Route == "" && perCall && !oneRoute:
		if r := forbiddenModelRoute(a, m.Model, p); r != "" {
			return forbiddenRefusal(kind, r, p), nil, false
		}
		return requiredRefusal(kind, p, "the route of its model cannot be told"), nil, defaultDeferrable(a, m, p)
	case l.Route == "":
		return requiredRefusal(kind, p, "its current login cannot be told"), nil, false
	case !p.Allows(l.Route):
		return forbiddenRefusal(kind, l.Route, p), nil, false
	}
	return nil, &l, false
}

// defaultDeferrable reports that m is a launch whose route only its working directory can tell
// (agents.DefaultLaunchResolver) and that the policy allows the one route such a proof gives
// (opencode.ai's keyless free models, filed under Zen). Only a Managed session qualifies: a
// Terminal runs its own opencode, which the shared serve says nothing about.
func defaultDeferrable(a agents.Agent, m session.Meta, p routepolicy.Policy) bool {
	_, ok := a.(agents.DefaultLaunchResolver)
	return ok && strings.TrimSpace(m.Model) == "" && m.DriverKind() == session.DriverManaged && p.Allows(routes.RouteOpencodeZen)
}

// deferredDefaultRefusal finishes a launch judgeNewLaunch deferred, once m names its working
// directory (Dir, Subdir): the running serve is asked about it, and only the route it proves is
// judged. Anything else (several providers, a stored key, an unanswered serve, a route the policy
// forbids) is the route_required it was deferred from. The probe blocks for up to the launch
// budget, so it never runs under a lock, from a resume, or from a Managed input revalidation. The
// returned launch is the one judged, for the caller to record.
func deferredDefaultRefusal(kind string, m session.Meta) (*agents.RouteRefusal, *routes.Launch) {
	p, restricted := restrictingPolicy()
	if !restricted {
		return nil, nil
	}
	a, _ := RegisteredAgent(NormalizeKind(kind))
	dr, ok := a.(agents.DefaultLaunchResolver)
	if !ok || !defaultDeferrable(a, m, p) {
		return requiredRefusal(kind, p, ""), nil
	}
	start := time.Now()
	l, _ := withinBudget(kind+" ResolveDefaultLaunch", func() routes.Launch { return dr.ResolveDefaultLaunch(m, start) })
	switch {
	case l.Route == "":
		return requiredRefusal(kind, p, "the route of its default model is not proven for this folder"), nil
	case !p.Allows(l.Route):
		return forbiddenRefusal(kind, l.Route, p), nil
	}
	return nil, &l
}

// forbiddenModelRoute is the first route a named model may run on when the policy forbids every
// one of them, "" otherwise (no model named, no candidates, or any candidate allowed). It lets a
// create whose payer cannot be proven be refused as forbidden rather than asking for a route that
// cannot help; it never allows anything.
func forbiddenModelRoute(a agents.Agent, model string, p routepolicy.Policy) string {
	l, ok := a.(agents.ModelRouteLister)
	if !ok || model == "" {
		return ""
	}
	cands := l.ModelRoutes(model)
	for _, r := range cands {
		if p.Allows(r) {
			return ""
		}
	}
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}

// storedLaunchPolicyRefusal judges a start of a stored session (a start, a resume, an
// execution-method switch, a route switch's candidate) on launch-time evidence, never by running
// the create rule again (D11): an explicit selection on its route; otherwise the launch record.
//   - No record (a session from before it), or one that could not tell its route: nothing proves
//     what the session would run on, so it is refused; the member chooses a route or recreates.
//   - A record on a forbidden route: refused.
//   - A record whose credential the kind would no longer launch on (another route, or another
//     binding where the record has one): a replacement (D4), refused, never moved. An explicit
//     selection's binding is the one the selection launches on (launchOfSelection), compared
//     with the record the same way. A kind whose route follows its model (agents.PerCallRouter,
//     unless it also resolves one route per connection, as lcpp does) may have changed model
//     since the launch: on the record's route the binding must still match; on another route
//     at the same destination (opencode.ai's Zen, Go and account) only the same binding is a
//     change of model, another one is the payer changing under the same model (an account
//     signed out, a key added); on another destination that route must be allowed.
//
// It is also the launch handoff's check (handoffRefusal): a session about to start is judged on
// the record it carries, so a credential that changed after the create judged it is refused.
func storedLaunchPolicyRefusal(m session.Meta) *agents.RouteRefusal {
	if !modelKind(m.Kind) {
		return nil
	}
	if ref := policyGate(); ref != nil {
		return ref
	}
	p, restricted := restrictingPolicy()
	if !restricted {
		return nil
	}
	if m.Route != nil {
		sel := *m.Route.Normalize()
		if !p.Allows(sel.Route) {
			return forbiddenRefusal(m.Kind, sel.Route, p)
		}
		if rec := m.LaunchRoute; rec != nil && rec.Binding != "" {
			a, _ := RegisteredAgent(NormalizeKind(m.Kind))
			if cur := launchOfSelection(a, sel); cur.Route != rec.Route || cur.Binding != rec.Binding {
				return replacementRefusal(m.Kind, rec.Route)
			}
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
	_, perCall := a.(agents.PerCallRouter)
	_, oneRoute := a.(agents.OneRouteResolver)
	if perCall && !oneRoute {
		switch {
		case cur.Route == "" && storedDefaultHolds(a, m, *rec):
			// A session that named no model and was launched on a route its folder proved (the
			// record deferredDefaultRefusal gave it): nothing but af's own credentials can have
			// changed it since, and those are read without a probe.
		case cur.Route == "":
			return requiredRefusal(m.Kind, p, "the route of its current model cannot be told")
		case !p.Allows(cur.Route):
			return forbiddenRefusal(m.Kind, cur.Route, p)
		case cur.Route == rec.Route && rec.Binding != "" && cur.Binding != rec.Binding:
			return replacementRefusal(m.Kind, rec.Route)
		case cur.Route != rec.Route && sameDestination(cur.Route, rec.Route) && (rec.Binding == "" || cur.Binding != rec.Binding):
			return replacementRefusal(m.Kind, rec.Route)
		}
		return nil
	}
	if cur.Route != rec.Route || rec.Binding != "" && cur.Binding != rec.Binding {
		return replacementRefusal(m.Kind, rec.Route)
	}
	return nil
}

// storedDefaultHolds is agents.DefaultLaunchHolder's answer for a stored session with no model
// named, false for a kind without one.
func storedDefaultHolds(a agents.Agent, m session.Meta, rec routes.Launch) bool {
	h, ok := a.(agents.DefaultLaunchHolder)
	return ok && strings.TrimSpace(m.Model) == "" && h.HoldsDefaultLaunch(m, rec)
}

// sameDestination reports whether two catalogued routes send prompts to the same destination. An
// uncatalogued route never reaches it allowed: the policy lists catalogued routes only.
func sameDestination(a, b string) bool {
	ra, okA := routes.Lookup(a)
	rb, okB := routes.Lookup(b)
	return okA && okB && ra.Destination == rb.Destination
}

// runningPolicyRefusal is the sweep's judgement of a session that is running (D12, "A session
// already running"): an explicit selection on its route, any other session on its launch-time
// record, and a session with no record (or one that could not tell its route) is refused under a
// restriction, since nothing proves what it runs on. While the policy cannot be confirmed or
// recorded, running sessions are left alone: only new work waits for the Control Plane.
func runningPolicyRefusal(m session.Meta) *agents.RouteRefusal {
	if !modelKind(m.Kind) || routePolicyStore().Gate() != nil {
		return nil
	}
	p, restricted := restrictingPolicy()
	if !restricted {
		return nil
	}
	route := ""
	switch {
	case m.Route != nil:
		route = m.Route.Normalize().Route
	case m.LaunchRoute != nil && m.LaunchRoute.Route != "":
		route = m.LaunchRoute.Route
	default:
		return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodePolicyRequired,
			Message: "the credential policy restricts the routes " + m.Kind + " may run on, and nothing records the route this session was launched on"}
	}
	if !p.Allows(route) {
		return &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodePolicyForbidden,
			Message: fmt.Sprintf("the credential policy no longer allows %s on %s", m.Kind, route)}
	}
	return nil
}

// handoffRefusal is the check at the moment a new launch is handed to its runtime (a create, a
// fork, a recreate, on either execution method): the session is judged on the launch record it
// carries, which is what the policy judged when it was created. A credential that changed in
// between (a login replaced while the working copy was prepared) is refused, never started.
func handoffRefusal(m session.Meta) *agents.RouteRefusal { return storedLaunchPolicyRefusal(m) }

func replacementRefusal(kind, route string) *agents.RouteRefusal {
	return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodePolicyForbidden,
		Message: fmt.Sprintf("this %s session was launched on %s, and the credential it would start on now is another one; "+
			"under the credential policy it is not moved to a different credential: recreate it to start on the current one", kind, route)}
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
