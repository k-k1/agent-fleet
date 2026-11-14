package sessionx

import (
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// resolvingAgent is the route agent with a CredentialResolver: what its sessions run on with no
// selection.
type resolvingAgent struct {
	routeAgent
	launch routes.Launch
}

func (a resolvingAgent) ResolveLaunch(session.Meta) routes.Launch { return a.launch }
func (resolvingAgent) UsableRoutes() []string                     { return nil }
func (resolvingAgent) LaunchBinding(agents.RouteSelection) string { return "bound-by-binder" }

// Every create records what it launched on (ADR 0138 D11): an explicit selection as stored, an
// empty one as the kind's resolver says, and the record rides the meta that is launched and
// published.
func TestCreateRecordsLaunchRoute(t *testing.T) {
	p := newCreateProbe(t)
	sub := routes.Launch{Route: routes.RouteAnthropicSubscription, Binding: "anthropic-subscription@00112233aabbccdd"}
	swapClaude(t, func(base agents.Agent) agents.Agent {
		return resolvingAgent{routeAgent: routeAgent{Agent: base, allowed: []string{"anthropic-api"}}, launch: sub}
	})
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }

	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec = p.post(map[string]any{"kind": "claude", "dir": p.repo, "route": map[string]any{"route": "anthropic-api", "connection": "ANTHROPIC_API_KEY@0011"}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create with a route: %d %s", rec.Code, rec.Body.String())
	}
	rec = p.post(map[string]any{"kind": "claude", "dir": p.repo, "route": map[string]any{"route": "anthropic-api"}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create with a route and no connection: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 3 {
		t.Fatalf("launched %d sessions, want 3", len(launched))
	}
	// A selection that names no connection takes the binding the kind's LaunchBinder gives it.
	if l := launched[2].LaunchRoute; l == nil || *l != (routes.Launch{Route: "anthropic-api", Binding: "bound-by-binder"}) {
		t.Errorf("selection without a connection recorded %+v", l)
	}
	if l := launched[0].LaunchRoute; l == nil || *l != sub {
		t.Errorf("empty selection recorded %+v, want %+v", l, sub)
	}
	if l := launched[1].LaunchRoute; l == nil || *l != (routes.Launch{Route: "anthropic-api", Binding: "ANTHROPIC_API_KEY@0011"}) {
		t.Errorf("explicit selection recorded %+v", l)
	}
	for _, m := range launched {
		if got, ok := session.ReadMeta(m.Name); !ok || got.LaunchRoute == nil || *got.LaunchRoute != *m.LaunchRoute {
			t.Errorf("%s: published meta has %+v, launched %+v", m.Name, got.LaunchRoute, m.LaunchRoute)
		}
	}
}

// A kind that runs no model records nothing, and a kind without a resolver too.
func TestLaunchRouteOfKindsWithoutResolver(t *testing.T) {
	for _, k := range []string{session.KindShell, "ssm"} {
		if l := launchRouteOf(session.Meta{Kind: k}); l != nil {
			t.Errorf("%s recorded %+v", k, l)
		}
	}
}
