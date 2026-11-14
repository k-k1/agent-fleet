package sessionx

import (
	"net/http"
	"testing"
	"time"

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
func (resolvingAgent) LaunchBinding(sel agents.RouteSelection) string {
	if sel.Connection == "" {
		return "bound-by-binder"
	}
	return "" // keeps the stored connection
}

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

// hungResolver blocks in ResolveLaunch and LaunchBinding until released, as a cold CLI probe or a
// locked store would.
type hungResolver struct {
	routeAgent
	entered chan struct{}
	release chan struct{}
}

func (h hungResolver) ResolveLaunch(session.Meta) routes.Launch {
	h.entered <- struct{}{}
	<-h.release
	return routes.Launch{Route: "late", Binding: "late"}
}
func (hungResolver) UsableRoutes() []string { return nil }
func (h hungResolver) LaunchBinding(agents.RouteSelection) string {
	h.entered <- struct{}{}
	<-h.release
	return "late"
}

func withHungResolver(t *testing.T) hungResolver {
	t.Helper()
	h := hungResolver{entered: make(chan struct{}, 8), release: make(chan struct{})}
	swapClaude(t, func(base agents.Agent) agents.Agent {
		h.routeAgent = routeAgent{Agent: base, allowed: []string{"anthropic-api"}}
		return h
	})
	old := launchResolveBudget
	launchResolveBudget = 100 * time.Millisecond
	t.Cleanup(func() { launchResolveBudget = old; close(h.release) })
	return h
}

// A resolver that hangs costs the record, never the launch (ADR 0138 M0 changes no behaviour): a
// create reaches its launcher within the budget while the resolver is still blocked, and the
// session is recorded as unresolved, with or without a selection.
func TestCreateIsNotHeldByAHungResolver(t *testing.T) {
	p := newCreateProbe(t)
	h := withHungResolver(t)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	for _, body := range []map[string]any{
		{"kind": "claude", "dir": p.repo},
		{"kind": "claude", "dir": p.repo, "route": map[string]any{"route": "anthropic-api", "connection": "ANTHROPIC_API_KEY@0011"}},
	} {
		start := time.Now()
		rec := p.post(body, "")
		if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Errorf("the create took %v behind a hung resolver", el)
		}
		select {
		case <-h.entered:
		default:
			t.Error("the resolver was never asked")
		}
	}
	if len(launched) != 2 {
		t.Fatalf("launched %d, want 2", len(launched))
	}
	if l := launched[0].LaunchRoute; l == nil || *l != (routes.Launch{}) {
		t.Errorf("no selection, hung resolver: recorded %+v, want unresolved", l)
	}
	if l := launched[1].LaunchRoute; l == nil || *l != (routes.Launch{Route: "anthropic-api"}) {
		t.Errorf("selection, hung binder: recorded %+v, want the route with an unproven binding", l)
	}
}

// A recreate (and a fork, which records through the same launchRouteOf) is bounded the same way.
func TestRecreateIsNotHeldByAHungResolver(t *testing.T) {
	home := writebackEnv(t)
	h := withHungResolver(t)
	const name = "lrrec1"
	session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindClaude})
	var launched []session.Meta
	orig := launchTmuxFn
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	t.Cleanup(func() { launchTmuxFn = orig })
	start := time.Now()
	callHandler(HandleRecreateSession, http.MethodPost, name, "")
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("the recreate took %v behind a hung resolver", el)
	}
	select {
	case <-h.entered:
	default:
		t.Error("the resolver was never asked")
	}
	if len(launched) != 1 || launched[0].LaunchRoute == nil || *launched[0].LaunchRoute != (routes.Launch{}) {
		t.Fatalf("launched %+v, want one launch recorded as unresolved", launched)
	}
}

// launchRouteOf itself, as fork calls it: bounded, and a resolver that answers fast is used.
func TestLaunchRouteOfIsBounded(t *testing.T) {
	isolateAgentState(t)
	withHungResolver(t)
	start := time.Now()
	if l := launchRouteOf(session.Meta{Kind: session.KindClaude}); l == nil || *l != (routes.Launch{}) {
		t.Errorf("hung: %+v", l)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("launchRouteOf took %v", el)
	}
}
