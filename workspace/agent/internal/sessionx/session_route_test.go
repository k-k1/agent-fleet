package sessionx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// routeAgent is claude with a RouteAdapter (and optionally a RouteChecker) bolted on, so the
// create, fork and move gates can be driven before any real harness implements the interface.
type routeAgent struct {
	agents.Agent
	allowed []string
}

func (a routeAgent) Routes() []string { return a.allowed }
func (routeAgent) ApplyRoute(session.Meta, agents.RouteSelection) (agents.RoutePlan, error) {
	return agents.RoutePlan{}, nil
}
func (routeAgent) ResolveRoute(session.Meta, agents.TurnInfo) agents.ResolvedRoute {
	return agents.ResolvedRoute{}
}

type checkedRouteAgent struct {
	routeAgent
	check func(sel agents.RouteSelection, model string) *agents.RouteRefusal
}

func (a checkedRouteAgent) CheckRoute(_ context.Context, sel agents.RouteSelection, model string) *agents.RouteRefusal {
	return a.check(sel, model)
}

func swapClaude(t *testing.T, mk func(base agents.Agent) agents.Agent) {
	t.Helper()
	base := AgentOf("claude")
	t.Cleanup(agentreg.SwapForTest("claude", mk(base)))
}

func routeCode(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &resp)
	return resp.Error.Code
}

// No harness implements RouteAdapter yet, so an explicit selection is refused for every
// registered kind, visibly, and the refusal leaves nothing behind — not even an idempotency entry.
func TestCreateWithRouteIsRefusedForKindsWithoutAdapter(t *testing.T) {
	p := newCreateProbe(t)
	for _, kind := range RegisteredKinds() {
		if len(routesOf(kind)) > 0 {
			continue // claude declares routes; session_route_claude_test.go covers it
		}
		before := p.sideEffects()
		body := map[string]any{"kind": kind, "dir": p.repo, "route": map[string]any{"route": "anthropic-api"},
			"idempotency_key": "route-" + kind}
		for i := 0; i < 2; i++ { // the second try must be refused the same way, not replayed
			rec := p.post(body, "")
			if rec.Code != http.StatusBadRequest || routeCode(t, rec.Body.String()) != routes.CodeRouteUnsupported {
				t.Fatalf("%s try %d: %d %s, want 400 %s", kind, i, rec.Code, rec.Body.String(), routes.CodeRouteUnsupported)
			}
		}
		if d := before.diff(p.sideEffects()); d != "" {
			t.Errorf("%s: a refused route left side effects: %s", kind, d)
		}
	}
}

func TestCreateRouteBadSelection(t *testing.T) {
	p := newCreateProbe(t)
	for name, route := range map[string]any{
		"empty object": map[string]any{},
		"blank id":     map[string]any{"route": "  "},
		"unknown id":   map[string]any{"route": "anthropic-apj"},
		"wrong case":   map[string]any{"route": "Anthropic-API"},
	} {
		rec := p.post(map[string]any{"kind": "claude", "dir": p.repo, "route": route}, "")
		if rec.Code != http.StatusBadRequest || routeCode(t, rec.Body.String()) != routes.CodeBadRoute {
			t.Errorf("%s: %d %s, want 400 %s", name, rec.Code, rec.Body.String(), routes.CodeBadRoute)
		}
	}
}

// The invariant: a create without a selection behaves as it always did, and its meta has no
// route key at all.
func TestCreateWithoutRouteIsUnchanged(t *testing.T) {
	p := newCreateProbe(t)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].Route != nil {
		t.Fatalf("launched %+v, want one meta with no route", launched)
	}
	b, _ := json.Marshal(launched[0])
	if strings.Contains(string(b), `"route"`) {
		t.Errorf("meta JSON carries a route key: %s", b)
	}
}

func TestCreateWithAdapterStoresSelectionAndRunsLiveChecks(t *testing.T) {
	p := newCreateProbe(t)
	var gotModel string
	swapClaude(t, func(base agents.Agent) agents.Agent {
		return checkedRouteAgent{
			routeAgent: routeAgent{Agent: base, allowed: []string{"anthropic-api"}},
			check: func(sel agents.RouteSelection, model string) *agents.RouteRefusal {
				gotModel = model
				switch sel.Connection {
				case "other-members-key":
					return &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodeRouteForbidden, Message: "not yours"}
				case "expired":
					return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteUnavailable, Message: "expired"}
				}
				if model == "wrong-model" {
					return &agents.RouteRefusal{Status: http.StatusBadRequest, Code: routes.CodeRouteModel, Message: "no"}
				}
				return nil
			},
		}
	})
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }

	for name, tc := range map[string]struct {
		route  map[string]any
		model  string
		status int
		code   string
	}{
		"not on the allow-list": {map[string]any{"route": "bedrock"}, "", 400, routes.CodeRouteUnsupported},
		"model mismatch":        {map[string]any{"route": "anthropic-api"}, "wrong-model", 400, routes.CodeRouteModel},
		"not the caller's":      {map[string]any{"route": "anthropic-api", "connection": "other-members-key"}, "", 403, routes.CodeRouteForbidden},
		"expired":               {map[string]any{"route": "anthropic-api", "connection": "expired"}, "", 409, routes.CodeRouteUnavailable},
	} {
		before := p.sideEffects()
		rec := p.post(map[string]any{"kind": "claude", "dir": p.repo, "model": tc.model, "route": tc.route}, "")
		if rec.Code != tc.status || routeCode(t, rec.Body.String()) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, rec.Code, rec.Body.String(), tc.status, tc.code)
		}
		if d := before.diff(p.sideEffects()); d != "" {
			t.Errorf("%s: left side effects: %s", name, d)
		}
	}
	if len(launched) != 0 {
		t.Fatalf("a refused selection still launched: %+v", launched)
	}

	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo, "model": "sonnet",
		"route": map[string]any{"route": " anthropic-api ", "connection": "mine"}}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("valid selection: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].Route == nil || *launched[0].Route != (routes.Selection{Route: "anthropic-api", Connection: "mine"}) {
		t.Fatalf("launched %+v, want the trimmed selection on the meta", launched)
	}
	if gotModel != "sonnet" {
		t.Errorf("the live check saw model %q, want the resolved one", gotModel)
	}
}

// Fork and recreate keep the selection and check it again; a fork must not share the pointer.
func TestForkInheritsAndRevalidatesSelection(t *testing.T) {
	src := session.Meta{Name: "src", Kind: "claude", Dir: t.TempDir(),
		Route: &routes.Selection{Route: "anthropic-api", Connection: "mine"}}
	f := forkMeta(src, "fork", "t", "from", "")
	if f.Route == nil || *f.Route != *src.Route {
		t.Fatalf("fork route = %+v, want %+v", f.Route, src.Route)
	}
	f.Route.Connection = "changed"
	if src.Route.Connection != "mine" {
		t.Error("the fork aliases its source's selection")
	}
	if forkMeta(session.Meta{Name: "s", Kind: "claude", Dir: t.TempDir()}, "f", "t", "x", "").Route != nil {
		t.Error("a fork of a session with no selection gained one")
	}

	// Re-validated: with no adapter an inherited selection is refused, with an adapter that now
	// reports the connection gone it is refused, and with neither it passes.
	if ref := checkRouteLive(context.Background(), "codex", &routes.Selection{Route: "openai-api"}, ""); ref == nil || ref.Code != routes.CodeRouteUnsupported {
		t.Errorf("no adapter: %+v", ref)
	}
	swapClaude(t, func(base agents.Agent) agents.Agent {
		return checkedRouteAgent{routeAgent: routeAgent{Agent: base, allowed: []string{"anthropic-api"}},
			check: func(agents.RouteSelection, string) *agents.RouteRefusal {
				return &agents.RouteRefusal{Status: http.StatusConflict, Code: routes.CodeRouteUnavailable, Message: "gone"}
			}}
	})
	if ref := checkRouteLive(context.Background(), "claude", src.Route, ""); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("revoked connection: %+v", ref)
	}
	if ref := checkRouteLive(context.Background(), "claude", nil, ""); ref != nil {
		t.Errorf("nil selection refused: %+v", ref)
	}
}

// A move never carries the source's selection to the target: the target's is the one the move
// request chose, validated for the target's own kind.
func TestMoveDoesNotCarrySourceRoute(t *testing.T) {
	src := session.Meta{Name: "src", Kind: "claude", Dir: t.TempDir(),
		Route: &routes.Selection{Route: "anthropic-api", Connection: "mine"}}
	if got := moveCreateReq(src, moveRecord{Target: "t", Kind: "codex"}).Route; got != nil {
		t.Errorf("target create carries the source's selection %+v", got)
	}
	chosen := &routes.Selection{Route: "openai-api"}
	if got := moveCreateReq(src, moveRecord{Target: "t", Kind: "codex", Route: chosen}).Route; got != chosen {
		t.Errorf("target create route = %+v, want the one the request chose", got)
	}
	isolateAgentState(t)
	err := checkMoveRequest(context.Background(), session.Meta{Name: "s", Kind: "claude", Dir: t.TempDir()},
		moveRequest{Kind: "codex", Route: chosen})
	if me, ok := err.(*moveError); !ok || me.Code != routes.CodeRouteUnsupported {
		t.Errorf("a selection codex cannot honour: %v, want %s", err, routes.CodeRouteUnsupported)
	}
}

func TestIdempotencyFingerprintSeesRoute(t *testing.T) {
	base := CreateReq{ReportTo: "conv", Kind: "claude", Dir: "/x"}
	withRoute := base
	withRoute.Route = &routes.Selection{Route: "anthropic-api"}
	other := base
	other.Route = &routes.Selection{Route: "anthropic-api", Connection: "k2"}
	a, b, c := createIdempotencyKey(&base), createIdempotencyKey(&withRoute), createIdempotencyKey(&other)
	if a == b || b == c {
		t.Errorf("fingerprints collide: %s %s %s", a, b, c)
	}
	// Pinned as a literal computed by the implementation before the field existed: a create
	// without a selection must keep hashing to this, or retried creates stop de-duplicating.
	const wantPlain = "fp_dc91be9e1d59f3482ad9fb5067a97cf1e12b422966007199b12f974c9e37d573"
	if a != wantPlain {
		t.Errorf("fingerprint with no selection = %s, want the pre-route value %s", a, wantPlain)
	}
}

// A stored selection is checked again at every start of a stored session, not only at create:
// the start endpoint, a Managed resume, and an execution-method switch. A selection nothing can
// honour any more must stop the start with its own code, never launch on the default route.
func TestStoredRouteIsRevalidatedOnStart(t *testing.T) {
	sel := &routes.Selection{Route: "openai-api", Connection: "c1"}
	t.Run("start endpoint", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "rstart1", Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged,
			StoppedAt: "2026-09-25T10:00:00+09:00", Route: sel})
		launched := false
		useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { launched = true }})
		rec := callHandler(HandleStartSession, http.MethodPost, "rstart1", "")
		if rec.Code == http.StatusOK || launched || routeCode(t, rec.Body.String()) != routes.CodeRouteUnsupported {
			t.Errorf("start = %d launched=%v %s, want a route_unsupported refusal and no launch", rec.Code, launched, rec.Body.String())
		}
	})
	t.Run("managed resume", func(t *testing.T) {
		m := session.Meta{Name: "rres1", Kind: session.KindCodex, Driver: session.DriverManaged, Route: sel}
		_, err := resumeManaged(nil, m) // a nil driver: the refusal must come first
		var re *StartRefusalError
		if !errors.As(err, &re) || re.Ref.Code != routes.CodeRouteUnsupported {
			t.Errorf("resumeManaged err = %v, want a route_unsupported start refusal", err)
		}
	})
	t.Run("driver switch", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "rdrv1", Dir: home, Kind: session.KindCodex, Route: sel})
		launched := false
		useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { launched = true }})
		rec := callHandler(HandleSessionDriver, http.MethodPost, "rdrv1", `{"driver":"managed"}`)
		cur, _ := session.ReadMeta("rdrv1")
		if rec.Code == http.StatusOK || launched || cur.DriverKind() != session.DriverTUI ||
			routeCode(t, rec.Body.String()) != routes.CodeRouteUnsupported {
			t.Errorf("switch = %d launched=%v driver=%q %s, want a refusal before anything stops or starts",
				rec.Code, launched, cur.Driver, rec.Body.String())
		}
	})
	t.Run("no selection starts as before", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "rnone1", Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged,
			StoppedAt: "2026-09-25T10:00:00+09:00"})
		useLockingDriver(t, session.KindCodex, &lockingDriver{})
		if rec := callHandler(HandleStartSession, http.MethodPost, "rnone1", ""); rec.Code != http.StatusOK {
			t.Errorf("start without a selection = %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestMetaRouteWire(t *testing.T) {
	var old session.Meta
	if err := json.Unmarshal([]byte(`{"name":"a","dir":"/d","kind":"claude"}`), &old); err != nil || old.Route != nil {
		t.Fatalf("a meta written before the field: route=%+v err=%v", old.Route, err)
	}
	b, _ := json.Marshal(session.Meta{Name: "a", Route: &routes.Selection{Route: "bedrock", Connection: "p/us-east-1"}})
	if !strings.Contains(string(b), `"route":{"route":"bedrock","connection":"p/us-east-1"}`) {
		t.Errorf("meta JSON = %s", b)
	}
}
