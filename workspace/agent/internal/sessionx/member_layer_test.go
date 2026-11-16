package sessionx

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The member's agentEnabled switch refuses a create of any kind that runs on a model, wherever
// the create comes from; a kind with no model is not a harness and keeps launching; lcpp's older
// switch keeps its own refusal.
func TestSwitchedOffKindIsRefused(t *testing.T) {
	isolateAgentState(t)
	writeCreatePrefs(t, map[string]any{"agentEnabled": map[string]any{"claude": false, "codex": true, "shell": false}})
	if s, code, _ := createRefusal(session.KindClaude, "m"); s != http.StatusForbidden || code != "agent_disabled" {
		t.Errorf("claude switched off: %d %q", s, code)
	}
	for _, k := range []string{session.KindCodex, session.KindShell, session.KindOpencode} {
		if s, code, _ := createRefusal(k, "m"); s != 0 {
			t.Errorf("%s: refused %d %q", k, s, code)
		}
	}
	if !KindSwitchedOff(session.KindClaude) || KindSwitchedOff(session.KindCodex) || KindSwitchedOff(session.KindShell) {
		t.Error("KindSwitchedOff disagrees with the switches")
	}

	writeCreatePrefs(t, map[string]any{"lcppEnabled": false, "agentEnabled": map[string]any{"lcpp": false}})
	if _, code, _ := createRefusal(session.KindLcpp, "m"); code != "lcpp_disabled" {
		t.Errorf("lcpp off by both switches: %q, want its own lcpp_disabled", code)
	}
	if !KindSwitchedOff(session.KindLcpp) {
		t.Error("lcpp off by lcppEnabled is not reported switched off")
	}
	writeCreatePrefs(t, map[string]any{"agentEnabled": map[string]any{"lcpp": false}})
	if _, code, _ := createRefusal(session.KindLcpp, "m"); code != "agent_disabled" {
		t.Errorf("lcpp off by agentEnabled: %q", code)
	}

	// A value that is not a boolean, or no map at all, switches nothing off.
	for _, prefs := range []map[string]any{{}, {"agentEnabled": map[string]any{"claude": "false"}}, {"agentEnabled": "claude"}} {
		writeCreatePrefs(t, prefs)
		if KindSwitchedOff(session.KindClaude) {
			t.Errorf("prefs %v switched claude off", prefs)
		}
	}
}

func TestSwitchedOffKindIsRefusedAtTheEndpoint(t *testing.T) {
	p := newCreateProbe(t)
	writeCreatePrefs(t, map[string]any{"agentEnabled": map[string]any{"claude": false}})
	launched := 0
	launchTmuxFn = func(session.Meta, bool) error { launched++; return nil }
	before := p.sideEffects()
	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, "")
	if rec.Code != http.StatusForbidden || routeCode(t, rec.Body.String()) != "agent_disabled" {
		t.Fatalf("create of a switched-off kind: %d %s", rec.Code, rec.Body.String())
	}
	if d := before.diff(p.sideEffects()); d != "" || launched != 0 {
		t.Errorf("a refused create left %q and launched %d", d, launched)
	}
}

// D11b's first step: under a restriction the member's default route names the route of a create
// that names none, and is recorded as its selection. A default that cannot be used is refused
// with route_required, never replaced by another route. A route the request names wins, and with
// no restriction the default changes nothing.
func TestDefaultRouteUnderARestriction(t *testing.T) {
	p := newCreateProbe(t)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	setDefault := func(route map[string]any) {
		writeCreatePrefs(t, map[string]any{"agentLaunchDefaults": map[string]any{"claude": map[string]any{"route": route}}})
	}
	empty := map[string]any{"kind": "claude", "dir": p.repo}

	usePolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	setDefault(map[string]any{"route": "anthropic-api"})
	setAnthropicKey(t, "sk-ant-api03-test")
	if rec := p.post(empty, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("no restriction: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].Route != nil {
		t.Fatalf("no restriction: launched %+v, want the empty selection kept", launched)
	}

	usePolicy(t, noSubscriptions)
	launched = nil
	if rec := p.post(empty, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("default anthropic-api, key held: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].Route == nil || launched[0].Route.Route != routes.RouteAnthropicAPI ||
		launched[0].LaunchRoute == nil || launched[0].LaunchRoute.Route != routes.RouteAnthropicAPI {
		t.Fatalf("default route not recorded as the selection: %+v", launched)
	}

	refused := func(name string, body map[string]any) {
		t.Helper()
		before := p.sideEffects()
		n := len(launched)
		rec := p.post(body, "")
		if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
			t.Errorf("%s: %d %s, want 409 %s", name, rec.Code, rec.Body.String(), routes.CodePolicyRequired)
		} else if !strings.Contains(rec.Body.String(), "default route") {
			t.Errorf("%s: the refusal does not say the default route is the problem: %s", name, rec.Body.String())
		}
		// The member's screen never names a hidden item (D10a): not the saved default, which the
		// policy may hide, nor a route a live check names.
		for _, hidden := range []string{"anthropic-subscription", "no-such-route", "other-member-conn"} {
			if strings.Contains(rec.Body.String(), hidden) {
				t.Errorf("%s: the refusal names %q: %s", name, hidden, rec.Body.String())
			}
		}
		if d := before.diff(p.sideEffects()); d != "" || len(launched) != n {
			t.Errorf("%s: a refused create left %q or launched", name, d)
		}
	}
	setAnthropicKey(t, "")
	refused("default whose key is gone", empty)
	setAnthropicKey(t, "sk-ant-api03-test")
	setDefault(map[string]any{"route": "anthropic-subscription"})
	refused("default the policy forbids", empty)
	setDefault(map[string]any{"route": "no-such-route"})
	refused("default that is not a route of claude", empty)
	setDefault(map[string]any{"route": "anthropic-api", "connection": "other-member-conn"})
	refused("default on a connection that is not the member's", empty)

	// The request's own route wins over the default.
	setDefault(map[string]any{"route": "anthropic-subscription"})
	launched = nil
	named := map[string]any{"kind": "claude", "dir": p.repo, "route": map[string]any{"route": "anthropic-api"}}
	if rec := p.post(named, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("named route with a forbidden default: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDefaultRouteOfReadsOnlyItsShape(t *testing.T) {
	isolateAgentState(t)
	for _, c := range []struct {
		prefs map[string]any
		want  *routes.Selection
	}{
		{map[string]any{}, nil},
		{map[string]any{"agentLaunchDefaults": map[string]any{"claude": map[string]any{"model": "x"}}}, nil},
		{map[string]any{"agentLaunchDefaults": map[string]any{"claude": map[string]any{"route": "anthropic-api"}}}, nil},
		{map[string]any{"agentLaunchDefaults": map[string]any{"claude": map[string]any{"route": map[string]any{"route": "  "}}}}, nil},
		{map[string]any{"agentLaunchDefaults": map[string]any{"claude": map[string]any{"route": map[string]any{"route": " bedrock ", "connection": "p@us-east-1"}}}},
			&routes.Selection{Route: "bedrock", Connection: "p@us-east-1"}},
	} {
		writeCreatePrefs(t, c.prefs)
		got := defaultRouteOf(session.KindClaude)
		if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("prefs %v: %+v, want %+v", c.prefs, got, c.want)
		}
	}
}

// fixedAgent stands in for a kind that resolves to one route per login or connection and reports
// which of its routes are usable now.
type fixedAgent struct {
	agents.Agent
	launch *routes.Launch
	fixed  []string
	usable *[]string
}

func (a fixedAgent) ResolveLaunch(session.Meta) routes.Launch { return *a.launch }
func (a fixedAgent) UsableRoutes() []string                   { return *a.usable }
func (fixedAgent) ResolvesOneRoute()                          {}
func (a fixedAgent) FixedRoutes() []string                    { return a.fixed }

func swapFixed(t *testing.T, kind string, fixed []string, l routes.Launch, usable []string) (*routes.Launch, *[]string) {
	t.Helper()
	cur, u := &l, &usable
	t.Cleanup(agentreg.SwapForTest(kind, fixedAgent{Agent: AgentOf(kind), launch: cur, fixed: fixed, usable: u}))
	return cur, u
}

// A default on a route the kind resolves by itself (codex's API-key login) is not a selection:
// the create stays empty and runs only while the current login resolves to that route and the
// route is usable. Another login, or one that cannot be told, is refused with the default's
// route_required, which names neither route.
func TestFixedRouteDefault(t *testing.T) {
	p := newCreateProbe(t)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	usePolicy(t, noSubscriptions)
	writeCreatePrefs(t, map[string]any{"agentLaunchDefaults": map[string]any{"codex": map[string]any{"route": map[string]any{"route": "openai-api"}}}})
	empty := map[string]any{"kind": "codex", "dir": p.repo}

	launch, usable := swapFixed(t, session.KindCodex, []string{routes.RouteChatGPTSubscription, routes.RouteOpenAIAPI},
		routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"}, []string{routes.RouteOpenAIAPI})
	if rec := p.post(empty, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("held: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].Route != nil || launched[0].LaunchRoute == nil || launched[0].LaunchRoute.Route != routes.RouteOpenAIAPI {
		t.Fatalf("held: launched %+v, want an empty selection recorded on openai-api", launched)
	}
	for name, l := range map[string]routes.Launch{
		"another login": {Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a1"},
		"no login":      {},
	} {
		*launch = l
		rec := p.post(empty, "")
		body := rec.Body.String()
		if rec.Code != http.StatusConflict || routeCode(t, body) != routes.CodePolicyRequired || !strings.Contains(body, "default route") {
			t.Errorf("%s: %d %s, want the default's route_required", name, rec.Code, body)
		}
		if strings.Contains(body, routes.RouteChatGPTSubscription) {
			t.Errorf("%s: the refusal names the hidden login's route: %s", name, body)
		}
	}
	// Resolving to the route is not enough: it must be usable now.
	*launch = routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"}
	*usable = nil
	if rec := p.post(empty, ""); rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
		t.Errorf("a default route not usable now: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 {
		t.Errorf("a refused create launched: %d", len(launched))
	}

	// Without the default the same API-key login launches as before (D11b's one-route step).
	*usable = []string{routes.RouteOpenAIAPI}
	writeCreatePrefs(t, map[string]any{})
	if rec := p.post(empty, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Errorf("control, no default: %d %s", rec.Code, rec.Body.String())
	}
	// A default that is no route of codex is refused.
	writeCreatePrefs(t, map[string]any{"agentLaunchDefaults": map[string]any{"codex": map[string]any{"route": map[string]any{"route": "anthropic-api"}}}})
	if rec := p.post(empty, ""); rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
		t.Errorf("a default codex cannot run on: %d %s", rec.Code, rec.Body.String())
	}
}

// A per-connection default (the member's llama.cpp endpoint) is held to the connection it saved:
// the same route on a replaced endpoint or key is refused, so is a default that saved no
// reference, and the deployment's engine is refused while it is not reachable, although lcpp
// resolves to it regardless.
func TestPerConnectionDefaultIsHeldToItsConnection(t *testing.T) {
	p := newCreateProbe(t)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	usePolicy(t, routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteMemberLlamacpp, routes.RouteAFEngineLLM}})
	setDefault := func(route map[string]any) {
		writeCreatePrefs(t, map[string]any{"agentLaunchDefaults": map[string]any{"lcpp": map[string]any{"route": route}}})
	}
	body := map[string]any{"kind": "lcpp", "dir": p.repo, "model": "m"}
	launch, usable := swapFixed(t, session.KindLcpp, []string{routes.RouteAFEngineLLM, routes.RouteMemberLlamacpp},
		routes.Launch{Route: routes.RouteMemberLlamacpp, Binding: "member-llamacpp@ep1"}, []string{routes.RouteMemberLlamacpp})

	setDefault(map[string]any{"route": "member-llamacpp", "connection": "member-llamacpp@ep1"})
	if rec := p.post(body, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("the saved endpoint: %d %s", rec.Code, rec.Body.String())
	}
	refused := func(name string) {
		t.Helper()
		n := len(launched)
		rec := p.post(body, "")
		if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired || len(launched) != n {
			t.Errorf("%s: %d %s, want the default's route_required", name, rec.Code, rec.Body.String())
		}
	}
	*launch = routes.Launch{Route: routes.RouteMemberLlamacpp, Binding: "member-llamacpp@ep2"}
	refused("the same route on a replaced endpoint")
	setDefault(map[string]any{"route": "member-llamacpp"})
	refused("a per-connection default with no reference")

	setDefault(map[string]any{"route": "af-engine-llm"})
	*launch = routes.Launch{Route: routes.RouteAFEngineLLM, Binding: "af-engine-llm#llm"}
	*usable = nil
	refused("the engine while it is not reachable")
	*usable = []string{routes.RouteAFEngineLLM}
	if rec := p.post(body, ""); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Errorf("control: the engine while it is reachable: %d %s", rec.Code, rec.Body.String())
	}
}

// A policy tightened after the default route was accepted, before the create is judged, is refused
// with the default's route_required, never a route_forbidden naming the now-hidden default.
func TestDefaultRouteForbiddenMidCreate(t *testing.T) {
	p := newCreateProbe(t)
	launchTmuxFn = func(session.Meta, bool) error { return nil }
	store := usePolicy(t, routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}})
	swapClaude(t, func(base agents.Agent) agents.Agent {
		return checkedRouteAgent{routeAgent: routeAgent{Agent: base, allowed: []string{routes.RouteAnthropicAPI}},
			check: func(agents.RouteSelection, string) *agents.RouteRefusal {
				if _, err := store.Accept(routepolicy.Policy{Revision: 4, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}}); err != nil {
					t.Error(err)
				}
				return nil
			}}
	})
	writeCreatePrefs(t, map[string]any{"agentLaunchDefaults": map[string]any{"claude": map[string]any{"route": map[string]any{"route": "anthropic-api"}}}})
	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, "")
	b := rec.Body.String()
	if rec.Code != http.StatusConflict || routeCode(t, b) != routes.CodePolicyRequired || strings.Contains(b, routes.RouteAnthropicAPI) {
		t.Errorf("tightened mid-create: %d %s, want route_required not naming anthropic-api", rec.Code, b)
	}
}

// Fork and recreate start a new session of the kind, so the member's switch refuses them before
// anything is made or archived; with the switch on they get past it.
func TestForkAndRecreateHonourTheSwitch(t *testing.T) {
	isolateAgentState(t)
	home := writebackEnv(t)
	session.WriteMeta(session.Meta{Name: "swfork1", Dir: home, Kind: session.KindClaude})
	session.WriteMeta(session.Meta{Name: "swrecr1", Dir: home, Kind: session.KindClaude})
	for _, on := range []bool{false, true} {
		writeCreatePrefs(t, map[string]any{"agentEnabled": map[string]any{"claude": on}})
		fork := callHandler(HandleForkSession, http.MethodPost, "swfork1", "")
		recr := callHandler(HandleRecreateSession, http.MethodPost, "swrecr1", "")
		cur, _ := session.ReadMeta("swrecr1")
		for name, rec := range map[string]int{"fork": fork.Code, "recreate": recr.Code} {
			body := map[string]string{"fork": fork.Body.String(), "recreate": recr.Body.String()}[name]
			disabled := strings.Contains(body, "agent_disabled")
			if !on && (!disabled || rec != http.StatusForbidden) {
				t.Errorf("%s of a switched-off kind: %d %s", name, rec, body)
			}
			if on && disabled {
				t.Errorf("%s with the switch on was refused as switched off: %s", name, body)
			}
		}
		if !on && cur.Archived {
			t.Error("a refused recreate archived the old slot")
		}
	}
}

// A move to a kind the member switched off is refused before the source is closed.
func TestMoveTargetHonoursTheSwitch(t *testing.T) {
	isolateAgentState(t)
	home := writebackEnv(t)
	writeCreatePrefs(t, map[string]any{"agentEnabled": map[string]any{"codex": false}})
	src := session.Meta{Name: "mv1", Dir: home, Kind: session.KindClaude}
	code := func() string {
		if err := checkMoveRequest(context.Background(), src, moveRequest{Kind: session.KindCodex}); err != nil {
			return asMoveError(err).Code
		}
		return ""
	}
	if got := code(); got != "agent_disabled" {
		t.Errorf("move to a switched-off kind: %q", got)
	}
	writeCreatePrefs(t, map[string]any{})
	if got := code(); got == "agent_disabled" {
		t.Error("control: move with the switch on was refused as switched off")
	}
}
