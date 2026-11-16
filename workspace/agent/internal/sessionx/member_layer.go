package sessionx

import (
	"net/http"
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

// switchedOffRefusal is the create refusal for a kind the member switched off with agentEnabled.
func switchedOffRefusal(kind string) (int, string, string) {
	return http.StatusForbidden, "agent_disabled",
		kind + " は設定でオフになっています。設定 > エージェントでオンにするか、「自分の設定をリセット」で戻してください。"
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
	if ref := checkRouteStatic(kind, def); ref != nil {
		return c.refuseDefault(def, ref.Message)
	}
	if !p.Allows(def.Route) {
		return c.refuseDefault(def, "the credential policy does not allow it")
	}
	c.req.Route = def
	c.fromDefault = true
	return true
}

// refuseDefault writes route_required for a default route that cannot be used.
func (c *createRun) refuseDefault(def *routes.Selection, why string) bool {
	ref := defaultRouteRefusal(c.req.Kind, def, why)
	httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
	return false
}

// defaultRouteRefusal is route_required naming the member's own default route and why it cannot
// be used. The route is the member's setting, so naming it reveals nothing; which layer forbids
// it is not said (D10a).
func defaultRouteRefusal(kind string, def *routes.Selection, why string) *agents.RouteRefusal {
	p, _ := restrictingPolicy()
	return requiredRefusal(kind, p, "your default route "+def.Route+" cannot be used: "+why)
}
