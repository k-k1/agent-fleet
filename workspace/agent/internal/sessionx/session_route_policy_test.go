package sessionx

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// usePolicy makes the enforcement points judge by a store that holds p, confirmed now.
func usePolicy(t *testing.T, p routepolicy.Policy) *routepolicy.Store {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "route-policy.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	old := routePolicyStore
	routePolicyStore = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routePolicyStore = old })
	return s
}

// noSubscriptions is a restricted member's policy: the API routes, no subscription.
var noSubscriptions = routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI, routes.RouteOpenAIAPI}}

// oneRouteAgent stands in for a kind that resolves to exactly one route per login (codex). With
// then set, every resolution after the first answers then: a login changed between the policy's
// check and the record.
type oneRouteAgent struct {
	agents.Agent
	launch *routes.Launch
	then   *routes.Launch
	calls  *int
}

func (a oneRouteAgent) ResolveLaunch(session.Meta) routes.Launch {
	if a.calls != nil {
		*a.calls++
		if *a.calls > 1 && a.then != nil {
			return *a.then
		}
	}
	return *a.launch
}
func (oneRouteAgent) UsableRoutes() []string { return nil }
func (oneRouteAgent) ResolvesOneRoute()      {}
func (oneRouteAgent) FixedRoutes() []string {
	return []string{routes.RouteChatGPTSubscription, routes.RouteOpenAIAPI}
}

// perCallAgent stands in for a kind whose route follows its model (opencode).
type perCallAgent struct {
	agents.Agent
	launch *routes.Launch
}

func (a perCallAgent) ResolveLaunch(session.Meta) routes.Launch { return *a.launch }
func (perCallAgent) UsableRoutes() []string                     { return nil }
func (perCallAgent) RoutesPerCall()                             {}

func swapOneRoute(t *testing.T, l routes.Launch) *routes.Launch {
	t.Helper()
	cur := &l
	t.Cleanup(agentreg.SwapForTest(session.KindCodex, oneRouteAgent{Agent: AgentOf(session.KindCodex), launch: cur}))
	return cur
}

func swapPerCall(t *testing.T, l routes.Launch) *routes.Launch {
	t.Helper()
	cur := &l
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, perCallAgent{Agent: AgentOf(session.KindOpencode), launch: cur}))
	return cur
}

func wantPolicyRefusal(t *testing.T, name string, ref *agents.RouteRefusal, status int, code string) {
	t.Helper()
	if ref == nil || ref.Status != status || ref.Code != code {
		t.Errorf("%s: %+v, want %d %s", name, ref, status, code)
	}
}

// With no restriction held, nothing is judged: an empty claude create, a stored session with no
// launch record and an explicit subscription all pass, exactly as before the policy existed.
func TestPolicyChangesNothingWithoutARestriction(t *testing.T) {
	for name, p := range map[string]*routepolicy.Policy{
		"nothing held":        nil,
		"an open policy held": {Revision: 1, Allowed: []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			if p != nil {
				usePolicy(t, *p)
			} else {
				s := routepolicy.Open(filepath.Join(t.TempDir(), "none.json"), "")
				old := routePolicyStore
				routePolicyStore = func() *routepolicy.Store { return s }
				t.Cleanup(func() { routePolicyStore = old })
			}
			if ref, l := newLaunchPolicyRefusal(session.KindClaude, nil, session.Meta{Kind: session.KindClaude}); ref != nil || l != nil {
				t.Errorf("empty claude create: %+v %+v", ref, l)
			}
			if ref, _ := newLaunchPolicyRefusal(session.KindClaude, &routes.Selection{Route: routes.RouteAnthropicSubscription},
				session.Meta{}); ref != nil {
				t.Errorf("explicit subscription: %+v", ref)
			}
			if ref := storedLaunchPolicyRefusal(session.Meta{Name: "old", Kind: session.KindCodex}); ref != nil {
				t.Errorf("a session with no launch record: %+v", ref)
			}
		})
	}
}

// A new launch under a restriction (D11b): an explicit selection on its route; an empty one on
// the single route a one-route kind resolves to, never moved to another; any other kind must name
// its route, and is told how.
func TestNewLaunchUnderARestriction(t *testing.T) {
	usePolicy(t, noSubscriptions)
	if ref, _ := newLaunchPolicyRefusal(session.KindClaude, &routes.Selection{Route: routes.RouteAnthropicAPI}, session.Meta{}); ref != nil {
		t.Errorf("an allowed explicit route: %+v", ref)
	}
	ref, _ := newLaunchPolicyRefusal(session.KindClaude, &routes.Selection{Route: routes.RouteAnthropicSubscription}, session.Meta{})
	wantPolicyRefusal(t, "a forbidden explicit route", ref, http.StatusForbidden, routes.CodePolicyForbidden)

	ref, _ = newLaunchPolicyRefusal(session.KindClaude, nil, session.Meta{Kind: session.KindClaude})
	wantPolicyRefusal(t, "an empty claude create", ref, http.StatusConflict, routes.CodePolicyRequired)
	if ref != nil && (!strings.Contains(ref.Message, "choose one of its allowed routes") || !strings.Contains(ref.Message, routes.RouteAnthropicAPI)) {
		t.Errorf("the refusal does not say what to do: %q", ref.Message)
	}

	swapPerCall(t, routes.Launch{})
	ref, _ = newLaunchPolicyRefusal(session.KindOpencode, nil, session.Meta{Kind: session.KindOpencode})
	wantPolicyRefusal(t, "an empty opencode create", ref, http.StatusConflict, routes.CodePolicyRequired)
	if ref != nil && (!strings.Contains(ref.Message, "name a model") || !strings.Contains(ref.Message, "default route")) {
		t.Errorf("opencode's refusal does not say to name a model or set a default route: %q", ref.Message)
	}

	launch := swapOneRoute(t, routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"})
	ref, judged := newLaunchPolicyRefusal(session.KindCodex, nil, session.Meta{Kind: session.KindCodex})
	if ref != nil || judged == nil || *judged != *launch {
		t.Errorf("codex on an allowed API key: %+v judged %+v", ref, judged)
	}
	*launch = routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a1"}
	ref, judged = newLaunchPolicyRefusal(session.KindCodex, nil, session.Meta{Kind: session.KindCodex})
	wantPolicyRefusal(t, "codex on its ChatGPT login", ref, http.StatusForbidden, routes.CodePolicyForbidden)
	if judged != nil {
		t.Error("a refused launch was handed back to be recorded")
	}
	*launch = routes.Launch{}
	ref, _ = newLaunchPolicyRefusal(session.KindCodex, nil, session.Meta{Kind: session.KindCodex})
	wantPolicyRefusal(t, "codex whose login cannot be told", ref, http.StatusConflict, routes.CodePolicyRequired)
}

// A stored session is judged on its launch-time evidence, never by running the create rule again.
func TestStoredLaunchUnderARestriction(t *testing.T) {
	usePolicy(t, noSubscriptions)
	api := routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"}
	cur := swapOneRoute(t, api)
	codex := func(rec *routes.Launch, sel *routes.Selection) session.Meta {
		return session.Meta{Name: "c1", Kind: session.KindCodex, LaunchRoute: rec, Route: sel}
	}
	for name, tc := range map[string]struct {
		m      session.Meta
		now    routes.Launch
		status int
		code   string
	}{
		"explicit, allowed":          {codex(nil, &routes.Selection{Route: routes.RouteOpenAIAPI}), api, 0, ""},
		"explicit, forbidden":        {codex(nil, &routes.Selection{Route: routes.RouteChatGPTSubscription}), api, 403, routes.CodePolicyForbidden},
		"no record (before M0)":      {codex(nil, nil), api, 409, routes.CodePolicyRequired},
		"an unresolved record":       {codex(&routes.Launch{}, nil), api, 409, routes.CodePolicyRequired},
		"a forbidden record":         {codex(&routes.Launch{Route: routes.RouteChatGPTSubscription}, nil), api, 403, routes.CodePolicyForbidden},
		"the same credential":        {codex(&api, nil), api, 0, ""},
		"the login replaced (route)": {codex(&routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"}, nil), routes.Launch{Route: routes.RouteChatGPTSubscription}, 409, routes.CodePolicyForbidden},
		"the key replaced (binding)": {codex(&api, nil), routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k2"}, 409, routes.CodePolicyForbidden},
		"a record with no binding":   {codex(&routes.Launch{Route: routes.RouteOpenAIAPI}, nil), routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "x"}, 0, ""},
		"a replaced login, now gone": {codex(&api, nil), routes.Launch{}, 409, routes.CodePolicyForbidden},
	} {
		*cur = tc.now
		ref := storedLaunchPolicyRefusal(tc.m)
		if tc.code == "" {
			if ref != nil {
				t.Errorf("%s: refused %+v", name, ref)
			}
			continue
		}
		wantPolicyRefusal(t, name, ref, tc.status, tc.code)
	}
	// A kind whose route follows its model: the record must be allowed, and so must the route of
	// the model it would run on now.
	oc := swapPerCall(t, routes.Launch{Route: routes.RouteAnthropicAPI})
	m := session.Meta{Name: "o1", Kind: session.KindOpencode, LaunchRoute: &routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "a@1"}}
	if ref := storedLaunchPolicyRefusal(m); ref != nil {
		t.Errorf("opencode on an allowed model: %+v", ref)
	}
	*oc = routes.Launch{Route: routes.RouteOpencodeZen}
	wantPolicyRefusal(t, "opencode's current model on a forbidden route", storedLaunchPolicyRefusal(m), 403, routes.CodePolicyForbidden)
	*oc = routes.Launch{}
	wantPolicyRefusal(t, "opencode's current model unresolved", storedLaunchPolicyRefusal(m), 409, routes.CodePolicyRequired)
}

// A restricting policy nothing has confirmed for over FreshnessLimit refuses new work at every
// point; so does a start that could record no floor (the store's Gate), restricted or not.
func TestUnconfirmedPolicyRefusesNewWork(t *testing.T) {
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Seed(noSubscriptions, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	old := routePolicyStore
	routePolicyStore = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routePolicyStore = old })
	ref, _ := newLaunchPolicyRefusal(session.KindClaude, &routes.Selection{Route: routes.RouteAnthropicAPI}, session.Meta{})
	wantPolicyRefusal(t, "a create", ref, http.StatusServiceUnavailable, routes.CodePolicyUnconfirmed)
	wantPolicyRefusal(t, "a start", storedLaunchPolicyRefusal(session.Meta{Name: "s", Kind: session.KindClaude,
		Route: &routes.Selection{Route: routes.RouteAnthropicAPI}}), http.StatusServiceUnavailable, routes.CodePolicyUnconfirmed)
}

// Through the endpoints: a refused create leaves nothing behind and launches nothing; an allowed
// one-route create records the launch the policy judged; a refused start, recreate and fork stop
// before anything is stopped or started.
func TestPolicyAtTheEndpoints(t *testing.T) {
	p := newCreateProbe(t)
	usePolicy(t, noSubscriptions)
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }

	before := p.sideEffects()
	rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, "")
	if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
		t.Fatalf("empty claude create: %d %s", rec.Code, rec.Body.String())
	}
	if d := before.diff(p.sideEffects()); d != "" || len(launched) != 0 {
		t.Fatalf("a refused create left side effects %q or launched %+v", d, launched)
	}

	// The login changes after the policy judged the create: the record keeps what was judged.
	judged := &routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"}
	calls := 0
	t.Cleanup(agentreg.SwapForTest(session.KindCodex, oneRouteAgent{Agent: AgentOf(session.KindCodex), launch: judged,
		then: &routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a1"}, calls: &calls}))
	rec = p.post(map[string]any{"kind": "codex", "dir": p.repo}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("codex on an allowed key: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].LaunchRoute == nil || *launched[0].LaunchRoute != *judged {
		t.Fatalf("launched %+v, want the judged launch recorded", launched)
	}

	t.Run("start", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "pstart1", Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged,
			StoppedAt: "2026-11-15T10:00:00+09:00"})
		started := false
		useLockingDriver(t, session.KindCodex, &lockingDriver{onResume: func() { started = true }})
		rec := callHandler(HandleStartSession, http.MethodPost, "pstart1", "")
		if rec.Code == http.StatusOK || started || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
			t.Errorf("start of a session with no launch record = %d started=%v %s", rec.Code, started, rec.Body.String())
		}
	})
	t.Run("recreate", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "precr1", Dir: home, Kind: session.KindClaude})
		rec := callHandler(HandleRecreateSession, http.MethodPost, "precr1", "")
		cur, _ := session.ReadMeta("precr1")
		if rec.Code == http.StatusOK || cur.Archived || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
			t.Errorf("recreate = %d archived=%v %s, want route_required before the old slot is archived", rec.Code, cur.Archived, rec.Body.String())
		}
	})
	t.Run("fork", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "pfork1", Dir: home, Kind: session.KindClaude,
			Route: &routes.Selection{Route: routes.RouteAnthropicSubscription}})
		rec := callHandler(HandleForkSession, http.MethodPost, "pfork1", "")
		if rec.Code == http.StatusOK || routeCode(t, rec.Body.String()) != routes.CodePolicyForbidden {
			t.Errorf("fork of a session on a forbidden route = %d %s", rec.Code, rec.Body.String())
		}
	})
}

// A move is refused before its source is closed when the policy would refuse its target.
func TestMoveTargetIsJudgedBeforeTheSourceCloses(t *testing.T) {
	isolateAgentState(t)
	usePolicy(t, noSubscriptions)
	src := session.Meta{Name: "s", Kind: session.KindCodex, Dir: t.TempDir()}
	for name, tc := range map[string]struct {
		route *routes.Selection
		code  string
	}{
		"a forbidden route": {&routes.Selection{Route: routes.RouteAnthropicSubscription}, routes.CodePolicyForbidden},
		"no route":          {nil, routes.CodePolicyRequired},
	} {
		err := checkMoveRequest(t.Context(), src, moveRequest{Kind: session.KindClaude, Route: tc.route})
		if me, ok := err.(*moveError); !ok || me.Code != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
}
