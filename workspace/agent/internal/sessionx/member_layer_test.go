package sessionx

import (
	"net/http"
	"strings"
	"testing"

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
			t.Errorf("%s: the refusal does not name the default route: %s", name, rec.Body.String())
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
