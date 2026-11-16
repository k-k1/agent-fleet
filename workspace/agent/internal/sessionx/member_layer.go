package sessionx

import (
	"net/http"
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// The member's own layer of the credential policy (ADR 0138 D7 layer 4): the on/off switch per
// harness and the default route per harness, both in ui-prefs. The switches are enforced here,
// not only hidden in the Console, because a hidden kind can still be named by MCP's
// create_session, a schedule or a direct POST (D10a: requests that name a hidden thing are
// refused).

// switchableKind reports whether the member's agentEnabled switch applies to kind: a registered
// kind that runs on a model credential. A kind that runs no model (shell, ssm) is not a harness
// in the policy's sense and cannot be switched off this way.
func switchableKind(kind string) bool {
	_, ok := RegisteredAgent(kind)
	return ok && modelKind(kind)
}

// KindSwitchedOff reports whether the member has switched kind off: its own CreatePolicy switch
// reads off (lcpp's lcppEnabled), or ui-prefs agentEnabled names it false. kind is an exact
// registry key.
func KindSwitchedOff(kind string) bool {
	if t, ok := createTraitsOf(kind); ok && t.EnabledPref != "" && !prefOn(t.EnabledPref) {
		return true
	}
	return switchableKind(kind) && !uiprefs.AgentEnabled(kind)
}

// switchRefusal is the refusal of a new launch of kind the member has switched off, status 0 when
// it is on. Every path that starts a new session asks it before any side effect: a create, a fork,
// a recreate and a move's target. The kind's own switch comes first, so lcpp switched off by its
// older setting keeps its own refusal.
func switchRefusal(kind string) (status int, code, msg string) {
	if t, ok := createTraitsOf(kind); ok && t.EnabledPref != "" && !prefOn(t.EnabledPref) {
		return http.StatusForbidden, t.DisabledCode, t.DisabledMessage
	}
	if switchableKind(kind) && !uiprefs.AgentEnabled(kind) {
		return http.StatusForbidden, "agent_disabled",
			kind + " は設定でオフになっています。設定 > エージェントでオンにするか、「自分の設定をリセット」で戻してください。"
	}
	return 0, "", ""
}

// defaultRouteOf is the member's default route for kind (ADR 0138 D16, D11b): ui-prefs
// agentLaunchDefaults[<kind>].route, {"route": id, "connection": ref}. nil when none is set or
// the value is not of that shape.
func defaultRouteOf(kind string) *routes.Selection {
	defs, _ := uiprefs.Read()["agentLaunchDefaults"].(map[string]any)
	row, _ := defs[kind].(map[string]any)
	raw, _ := row["route"].(map[string]any)
	id, _ := raw["route"].(string)
	if strings.TrimSpace(id) == "" {
		return nil
	}
	conn, _ := raw["connection"].(string)
	return (&routes.Selection{Route: id, Connection: conn}).Normalize()
}

// applyDefaultRoute is D11b's first step for a create with no selection under a restriction: the
// member's default route for the harness counts as naming it, and becomes the create's explicit
// selection, recorded like one. With no restriction nothing changes (an empty selection keeps
// today's rules), and a create that names its route is never touched.
//
// A default that cannot be used is refused with route_required, never replaced by a fallback:
// not the next allowed route, and not the route the kind would resolve to on its own, since the
// member chose another payer. checkRoute turns the live checks' refusal of it into the same.
func (c *createRun) applyDefaultRoute() bool {
	kind := NormalizeKind(c.req.Kind)
	if c.req.Route != nil || !modelKind(kind) || routePolicyStore().Gate() != nil {
		return true
	}
	p, restricted := restrictingPolicy()
	if !restricted {
		return true
	}
	def := defaultRouteOf(kind)
	if def == nil {
		return true
	}
	if !p.Allows(def.Route) {
		return c.refuseDefault()
	}
	if slices.Contains(routesOf(kind), def.Route) {
		if checkRouteStatic(kind, def) != nil {
			return c.refuseDefault()
		}
		c.req.Route = def
		c.fromDefault = true
		return true
	}
	// A route the kind resolves by itself (codex's API-key login, a CLI-locked login) cannot be
	// selected: the create stays empty, and checkRoute holds it to the default, so a login that
	// resolves to another route is refused rather than run on a payer the member did not choose.
	a, _ := RegisteredAgent(kind)
	if !slices.Contains(agents.FixedRoutesOf(a), def.Route) {
		return c.refuseDefault()
	}
	// A per-connection route (the member's llama.cpp endpoint) is the same route whatever the
	// endpoint and key: only the saved reference says which one the member chose, so a default
	// without one cannot be held to it.
	if r, ok := routes.Lookup(def.Route); ok && r.PerConnection && def.Connection == "" {
		return c.refuseDefault()
	}
	if !defaultUsable(a, def.Route) {
		return c.refuseDefault()
	}
	c.defaultFixed = def
	return true
}

// defaultUsable reports whether the kind's credential for route is held and reachable now
// (agents.CredentialResolver's UsableRoutes, within the launch budget): resolving a launch alone
// does not say so, as lcpp resolves to the deployment's engine whether or not one is up. A kind
// that does not answer in time is not usable.
func defaultUsable(a agents.Agent, route string) bool {
	cr, ok := a.(agents.CredentialResolver)
	if !ok {
		return false
	}
	l, ok := withinBudget("default route usable", func() routes.Launch {
		if slices.Contains(cr.UsableRoutes(), route) {
			return routes.Launch{Route: route}
		}
		return routes.Launch{}
	})
	return ok && l.Route == route
}

// checkDefaultFixed is checkRoute's hold on a fixed-route default (applyDefaultRoute): the launch
// the policy judged must be on the default route. Any other answer, a refusal included, becomes the
// default's route_required, so a message naming the current login's route never reaches the
// member; only a policy that cannot be confirmed keeps its own refusal.
func (c *createRun) checkDefaultFixed(ref *agents.RouteRefusal, judged *routes.Launch) *agents.RouteRefusal {
	def := c.defaultFixed
	if def == nil || ref != nil && ref.Code == routes.CodePolicyUnconfirmed {
		return ref
	}
	// A saved reference must be the binding the launch resolved to: a replaced endpoint or key is
	// the same route on another credential, refused rather than run on (D4).
	if ref != nil || judged == nil || judged.Route != def.Route || def.Connection != "" && judged.Binding != def.Connection {
		return defaultRouteRefusal(c.req.Kind)
	}
	return nil
}

// fromDefaultRefusal is the policy's refusal of a create running on an explicit default route
// (applyDefaultRoute), made generic: a policy tightened since the default was accepted would
// otherwise name the now-hidden default. A policy that cannot be confirmed keeps its own refusal.
func (c *createRun) fromDefaultRefusal(ref *agents.RouteRefusal) *agents.RouteRefusal {
	if !c.fromDefault || ref == nil || ref.Code == routes.CodePolicyUnconfirmed {
		return ref
	}
	return defaultRouteRefusal(c.req.Kind)
}

// refuseDefault writes route_required for a default route that cannot be used.
func (c *createRun) refuseDefault() bool {
	ref := defaultRouteRefusal(c.req.Kind)
	httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
	return false
}

// defaultRouteRefusal is route_required for a default route that cannot be used. It names neither
// the default nor why it fails: the saved default may be a route the policy now hides, and a live
// check's message may name one too, while a member's screens never name a hidden item (D10a).
// requiredRefusal lists only the routes the policy allows.
func defaultRouteRefusal(kind string) *agents.RouteRefusal {
	p, _ := restrictingPolicy()
	return requiredRefusal(kind, p, "your default route for "+kind+" cannot be used now")
}
