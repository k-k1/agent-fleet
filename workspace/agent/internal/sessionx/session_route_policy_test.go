package sessionx

import (
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
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
	oc := swapPerCall(t, routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "a@1"})
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

	judged := swapOneRoute(t, routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"})
	rec = p.post(map[string]any{"kind": "codex", "dir": p.repo}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("codex on an allowed key: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].LaunchRoute == nil || *launched[0].LaunchRoute != *judged {
		t.Fatalf("launched %+v, want the judged launch recorded", launched)
	}
	// The login is replaced by the forbidden one after the policy judged the create: the handoff
	// refuses it, nothing is launched, and no session is left behind.
	calls := 0
	t.Cleanup(agentreg.SwapForTest(session.KindCodex, oneRouteAgent{Agent: AgentOf(session.KindCodex), launch: judged,
		then: &routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a1"}, calls: &calls}))
	rec = p.post(map[string]any{"kind": "codex", "dir": p.repo}, "")
	if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyForbidden || len(launched) != 1 {
		t.Fatalf("a login replaced after the check: %d %s, launched %d", rec.Code, rec.Body.String(), len(launched))
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

// A kind that runs no model is outside the policy, its Gate included: a restriction, or a policy
// that cannot be confirmed, never stops a shell or an SSM session.
func TestNonModelKindsAreOutsideThePolicy(t *testing.T) {
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Seed(noSubscriptions, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err) // restricting and unconfirmed: every model launch is refused
	}
	old := routePolicyStore
	routePolicyStore = func() *routepolicy.Store { return s }
	t.Cleanup(func() { routePolicyStore = old })
	for _, kind := range []string{session.KindShell, session.KindSSM} {
		m := session.Meta{Name: "nm", Kind: kind}
		if ref, l := newLaunchPolicyRefusal(kind, nil, m); ref != nil || l != nil {
			t.Errorf("%s create: %+v %+v", kind, ref, l)
		}
		if ref := storedLaunchPolicyRefusal(m); ref != nil {
			t.Errorf("%s start: %+v", kind, ref)
		}
	}
	if ref, _ := newLaunchPolicyRefusal(session.KindClaude, &routes.Selection{Route: routes.RouteAnthropicAPI}, session.Meta{}); ref == nil {
		t.Error("control: a model launch was not refused under the unconfirmed policy")
	}
}

// opencode with a named model is judged on the route its resolver proves for that model, and the
// judgement is the record; an unnamed default stays route_required.
func TestNamedOpencodeModelIsJudgedOnItsRoute(t *testing.T) {
	usePolicy(t, noSubscriptions)
	l := swapPerCall(t, routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@a"})
	ref, judged := newLaunchPolicyRefusal(session.KindOpencode, nil, session.Meta{Kind: session.KindOpencode, Model: "anthropic/claude-x"})
	if ref != nil || judged == nil || *judged != *l {
		t.Errorf("an allowed named model: %+v judged %+v", ref, judged)
	}
	*l = routes.Launch{Route: routes.RouteOpencodeZen, Binding: "opencode-zen@free"}
	ref, _ = newLaunchPolicyRefusal(session.KindOpencode, nil, session.Meta{Kind: session.KindOpencode, Model: "opencode/free"})
	wantPolicyRefusal(t, "a forbidden named model", ref, http.StatusForbidden, routes.CodePolicyForbidden)
	*l = routes.Launch{}
	ref, _ = newLaunchPolicyRefusal(session.KindOpencode, nil, session.Meta{Kind: session.KindOpencode})
	wantPolicyRefusal(t, "the default model", ref, http.StatusConflict, routes.CodePolicyRequired)
}

// modelRoutesAgent is perCallAgent that also lists the routes a named model might run on.
type modelRoutesAgent struct {
	perCallAgent
	cands []string
}

func (a modelRoutesAgent) ModelRoutes(model string) []string {
	if model == "" {
		return nil
	}
	return a.cands
}

// A named opencode model whose payer cannot be proven (resolver answers nothing) is refused as
// forbidden when every route it might run on is forbidden, and stays route_required when any one
// of them is allowed or none can be listed.
func TestNamedOpencodeModelWithUnprovenPayer(t *testing.T) {
	zenAcct := []string{routes.RouteOpencodeZen, routes.RouteOpencodeAccount}
	goAcct := []string{routes.RouteOpencodeGo, routes.RouteOpencodeAccount}
	for name, c := range map[string]struct {
		allowed []string
		cands   []string
		model   string
		status  int
		code    string
	}{
		"every candidate forbidden": {nil, zenAcct, "opencode/iceman-free", 403, routes.CodePolicyForbidden},
		"go provider forbidden":     {nil, goAcct, "opencode-go/glm", 403, routes.CodePolicyForbidden},
		"one candidate allowed":     {[]string{routes.RouteOpencodeAccount}, zenAcct, "opencode/iceman-free", 409, routes.CodePolicyRequired},
		"no candidates (ambiguous)": {nil, nil, "mystery/model", 409, routes.CodePolicyRequired},
		"no model named":            {nil, zenAcct, "", 409, routes.CodePolicyRequired},
	} {
		t.Run(name, func(t *testing.T) {
			usePolicy(t, routepolicy.Policy{Revision: 3, Restricted: true, Allowed: append([]string{routes.RouteAnthropicAPI}, c.allowed...)})
			l := routes.Launch{}
			t.Cleanup(agentreg.SwapForTest(session.KindOpencode, modelRoutesAgent{
				perCallAgent: perCallAgent{Agent: AgentOf(session.KindOpencode), launch: &l}, cands: c.cands}))
			ref, judged := newLaunchPolicyRefusal(session.KindOpencode, nil, session.Meta{Kind: session.KindOpencode, Model: c.model})
			wantPolicyRefusal(t, name, ref, c.status, c.code)
			if judged != nil {
				t.Errorf("a refused create was handed back to be recorded: %+v", judged)
			}
		})
	}
}

// The recorded credential identity is compared in every branch: an explicit selection whose
// binding is now another account's, an opencode session on the same allowed route with another
// key, and an lcpp session whose connection moved (a one-route kind even though it is per-call).
func TestRecordedBindingsAreComparedInEveryBranch(t *testing.T) {
	p := noSubscriptions
	p.Allowed = append(slices.Clone(p.Allowed), routes.RouteAnthropicSubscription, routes.RouteAFEngineLLM, routes.RouteMemberLlamacpp)
	usePolicy(t, p)

	cur := routes.Launch{Route: routes.RouteAnthropicSubscription, Binding: "anthropic-subscription@B"}
	t.Cleanup(agentreg.SwapForTest(session.KindClaude, oneRouteAgent{Agent: AgentOf(session.KindClaude), launch: &cur}))
	explicit := session.Meta{Name: "e1", Kind: session.KindClaude, Route: &routes.Selection{Route: routes.RouteAnthropicSubscription},
		LaunchRoute: &routes.Launch{Route: routes.RouteAnthropicSubscription, Binding: "anthropic-subscription@A"}}
	wantPolicyRefusal(t, "an explicit selection on another account", storedLaunchPolicyRefusal(explicit), http.StatusConflict, routes.CodePolicyForbidden)
	explicit.LaunchRoute.Binding = ""
	if ref := storedLaunchPolicyRefusal(explicit); ref != nil {
		t.Errorf("an explicit selection whose record has no binding: %+v", ref)
	}

	oc := swapPerCall(t, routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@B"})
	m := session.Meta{Name: "o1", Kind: session.KindOpencode, Model: "anthropic/claude-x",
		LaunchRoute: &routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@A"}}
	wantPolicyRefusal(t, "opencode on its route with another key", storedLaunchPolicyRefusal(m), http.StatusConflict, routes.CodePolicyForbidden)
	*oc = routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@x"}
	if ref := storedLaunchPolicyRefusal(m); ref != nil {
		t.Errorf("opencode whose model moved to another allowed route: %+v", ref)
	}

	lc := &routes.Launch{Route: routes.RouteAFEngineLLM, Binding: "af-engine-llm@llm"}
	t.Cleanup(agentreg.SwapForTest(session.KindLcpp, lcppLikeAgent{oneRouteAgent{Agent: AgentOf(session.KindLcpp), launch: lc}}))
	l := session.Meta{Name: "l1", Kind: session.KindLcpp, LaunchRoute: &routes.Launch{Route: routes.RouteMemberLlamacpp, Binding: "member-llamacpp@e1"}}
	wantPolicyRefusal(t, "lcpp moved from the member's endpoint to the engine", storedLaunchPolicyRefusal(l), http.StatusConflict, routes.CodePolicyForbidden)
}

// lcppLikeAgent is a one-route kind that is also per-call, as lcpp is.
type lcppLikeAgent struct{ oneRouteAgent }

func (lcppLikeAgent) RoutesPerCall() {}

// recallingAgent restores a model from the conversation, on another provider, and records what
// would be built.
type recallingAgent struct {
	agents.Agent
	built  *string
	recall string // the model the conversation last ran on; "" is opencode's free one
}

func (recallingAgent) UsableRoutes() []string { return nil }
func (recallingAgent) RoutesPerCall()         {}
func (recallingAgent) ResolveLaunch(m session.Meta) routes.Launch {
	if m.Model == "opencode/free" {
		return routes.Launch{Route: routes.RouteOpencodeZen, Binding: "opencode-zen@free"}
	}
	return routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@a"}
}
func (a recallingAgent) RecallSettings(session.Meta) agents.RecalledSettings {
	if a.recall != "" {
		return agents.RecalledSettings{Model: a.recall}
	}
	return agents.RecalledSettings{Model: "opencode/free"}
}
func (a recallingAgent) BuildLaunch(m session.Meta, _ agents.LaunchOpts) (agents.LaunchPlan, error) {
	*a.built = m.Model
	return agents.LaunchPlan{}, errors.New("test: no process")
}

// A start is judged on the settings it will launch with: a model the conversation last ran with,
// recalled after the stored one was judged, is judged too.
func TestRecalledModelIsJudgedBeforeTheStart(t *testing.T) {
	p := newCreateProbe(t)
	usePolicy(t, noSubscriptions)
	built := ""
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, recallingAgent{Agent: AgentOf(session.KindOpencode), built: &built}))
	m := session.Meta{Name: "recall1", Dir: p.repo, Kind: session.KindOpencode, Model: "anthropic/claude-x",
		LaunchRoute: &routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@a"}}
	session.WriteMeta(m)
	rec := callHandler(HandleStartSession, http.MethodPost, m.Name, "")
	if built != "" || routeCode(t, rec.Body.String()) != routes.CodePolicyForbidden {
		t.Errorf("start = %d %s, built %q: the recalled forbidden model must be refused before the build", rec.Code, rec.Body.String(), built)
	}
}

// A recreate whose credential changes between the policy's check and the handoff is refused at
// the handoff, and the old slot is put back on the active list.
func TestRecreateHandoffRefusesAChangedCredential(t *testing.T) {
	home := writebackEnv(t)
	usePolicy(t, noSubscriptions)
	calls := 0
	t.Cleanup(agentreg.SwapForTest(session.KindCodex, oneRouteAgent{Agent: AgentOf(session.KindCodex),
		launch: &routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k1"},
		then:   &routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a1"}, calls: &calls}))
	launched := false
	prev := launchTmuxFn
	launchTmuxFn = func(session.Meta, bool) error { launched = true; return nil }
	t.Cleanup(func() { launchTmuxFn = prev })
	session.WriteMeta(session.Meta{Name: "rhand1", Dir: home, Kind: session.KindCodex})
	rec := callHandler(HandleRecreateSession, http.MethodPost, "rhand1", "")
	cur, _ := session.ReadMeta("rhand1")
	if launched || cur.Archived || routeCode(t, rec.Body.String()) != routes.CodePolicyForbidden {
		t.Errorf("recreate = %d %s launched=%v old archived=%v", rec.Code, rec.Body.String(), launched, cur.Archived)
	}
}

// A route switch's candidate is judged on the launch record the switch would store, not on the
// old route's: an allowed target is offered under a restricting policy, a forbidden one is not.
func TestRouteSwitchCandidateIsJudgedOnItsOwnRecord(t *testing.T) {
	e := newSwitchEnv(t, subscriptionRoute)
	usePolicy(t, routepolicy.Policy{Revision: 4, Restricted: true, Allowed: []string{routes.RouteAnthropicSubscription, routes.RouteAnthropicAPI}})
	if _, err := checkRouteSwitch(t.Context(), e.meta(), ""); err != nil {
		t.Fatalf("an allowed target was refused: %v", err)
	}
	usePolicy(t, routepolicy.Policy{Revision: 5, Restricted: true, Allowed: []string{routes.RouteAnthropicSubscription}})
	if _, err := checkRouteSwitch(t.Context(), e.meta(), ""); err == nil || err.Code != routes.CodePolicyForbidden {
		t.Fatalf("a forbidden target: %v, want %s", err, routes.CodePolicyForbidden)
	}
}

// A start is judged on the settings it launches with, the recalled ones: a stored model the member
// has since switched away from neither stops nor stands in for the one launched.
func TestStartJudgesTheRecalledModelNotTheStoredOne(t *testing.T) {
	p := newCreateProbe(t)
	usePolicy(t, noSubscriptions)
	built := ""
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, recallingAgent{Agent: AgentOf(session.KindOpencode), built: &built, recall: "anthropic/claude-x"}))
	// Stored on the forbidden free model; the conversation last ran on the allowed Anthropic one.
	m := session.Meta{Name: "recall2", Dir: p.repo, Kind: session.KindOpencode, Model: "opencode/free",
		LaunchRoute: &routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@a"}}
	session.WriteMeta(m)
	rec := callHandler(HandleStartSession, http.MethodPost, m.Name, "")
	if built != "anthropic/claude-x" {
		t.Errorf("start = %d %s, built %q: the recalled allowed model must reach the build", rec.Code, rec.Body.String(), built)
	}
}

// opencode's payer changing under the same model is a replacement: an account session that would
// now run on Zen (the account signed out) is refused, and so is the reverse; Zen to Go on the same
// key is a change of model, and a move to another provider's allowed route is too.
func TestOpencodePayerChangeIsAReplacement(t *testing.T) {
	p := noSubscriptions
	p.Allowed = append(slices.Clone(p.Allowed), routes.RouteOpencodeZen, routes.RouteOpencodeGo, routes.RouteOpencodeAccount)
	usePolicy(t, p)
	cur := swapPerCall(t, routes.Launch{})
	m := func(rec routes.Launch) session.Meta {
		return session.Meta{Name: "oc", Kind: session.KindOpencode, Model: "opencode/m", LaunchRoute: &rec}
	}
	account := routes.Launch{Route: routes.RouteOpencodeAccount, Binding: "opencode-account@c1"}
	zenFree := routes.Launch{Route: routes.RouteOpencodeZen, Binding: "opencode-zen@free"}
	zenKey := routes.Launch{Route: routes.RouteOpencodeZen, Binding: "OPENCODE_API_KEY@k"}
	goKey := routes.Launch{Route: routes.RouteOpencodeGo, Binding: "OPENCODE_API_KEY@k"}
	anth := routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@a"}
	for name, tc := range map[string]struct {
		rec, now routes.Launch
		refused  bool
	}{
		"account signed out, now free Zen":  {account, zenFree, true},
		"a Zen key replaced by the account": {zenKey, account, true},
		"Zen to Go on the same key":         {zenKey, goKey, false},
		"to another provider's route":       {account, anth, false},
	} {
		*cur = tc.now
		ref := storedLaunchPolicyRefusal(m(tc.rec))
		if got := ref != nil; got != tc.refused {
			t.Errorf("%s: refusal %+v, want refused=%v", name, ref, tc.refused)
		}
	}
}

// The sweep judges every live session under the policy (D12): a session with no stored route on
// its launch record, one with no record at all, and one on a forbidden explicit route are halted,
// resumably, with a route-stopped notice; an allowed one, a shell, and a stopped session are
// left alone. While the policy cannot be confirmed, nothing is halted.
func TestSweepHaltsWhatThePolicyForbids(t *testing.T) {
	isolateAgentState(t)
	mk := func(name, kind string, sel *routes.Selection, rec *routes.Launch, live bool) session.Meta {
		m := session.Meta{Name: name, Kind: kind, Dir: t.TempDir(), Route: sel, LaunchRoute: rec, CreatedAt: "2026-11-15T10:00:00+09:00"}
		session.WriteMeta(m)
		if live {
			if out, err := tmuxx.Cmd("new-session", "-d", "-s", session.TmuxName(name), "sleep 120").CombinedOutput(); err != nil {
				t.Fatalf("tmux: %v %s", err, out)
			}
			t.Cleanup(func() { _ = tmuxx.Cmd("kill-session", "-t", session.TmuxName(name)).Run() })
		}
		return m
	}
	alive := func(m session.Meta) bool { return tmuxx.HasSession(session.TmuxName(m.Name)) }
	sub := &routes.Launch{Route: routes.RouteAnthropicSubscription, Binding: "anthropic-subscription@a"}
	api := &routes.Launch{Route: routes.RouteAnthropicAPI, Binding: "ANTHROPIC_API_KEY@k"}
	onSub := mk("pw-sub", session.KindClaude, nil, sub, true)
	noRec := mk("pw-norec", session.KindClaude, nil, nil, true)
	explicit := mk("pw-explicit", session.KindClaude, &routes.Selection{Route: routes.RouteAnthropicSubscription}, nil, true)
	allowed := mk("pw-api", session.KindClaude, nil, api, true)
	shell := mk("pw-shell", session.KindShell, nil, nil, true)

	// An unconfirmed restricting policy halts nothing: running sessions are left alone.
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Seed(noSubscriptions, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	old := routePolicyStore
	routePolicyStore = func() *routepolicy.Store { return s }
	SweepRouteGuards(time.Now())
	routePolicyStore = old
	for _, m := range []session.Meta{onSub, noRec, explicit, allowed, shell} {
		if !alive(m) {
			t.Fatalf("%s was halted while the policy could not be confirmed", m.Name)
		}
	}

	usePolicy(t, noSubscriptions)
	SweepRouteGuards(time.Now())
	for _, m := range []session.Meta{onSub, noRec, explicit} {
		if alive(m) {
			t.Errorf("%s is still running on a route the policy forbids", m.Name)
		}
	}
	for _, m := range []session.Meta{allowed, shell} {
		if !alive(m) {
			t.Errorf("%s was halted", m.Name)
		}
	}
	stopped := map[string]string{}
	for _, ev := range notice.List() {
		if ev.Kind == "route-stopped" {
			stopped[ev.SessionName], _ = ev.Payload["code"].(string)
		}
	}
	if stopped[onSub.Name] != routes.CodePolicyForbidden || stopped[noRec.Name] != routes.CodePolicyRequired ||
		stopped[explicit.Name] != routes.CodePolicyForbidden || len(stopped) != 3 {
		t.Errorf("route-stopped notices = %v", stopped)
	}
	// Halted means resumable, and the start is refused while the cause remains.
	m, _ := session.ReadMeta(onSub.Name)
	if m.Archived || startRefusalOf(m) == nil {
		t.Errorf("after the halt: archived=%v, start refusal %+v", m.Archived, startRefusalOf(m))
	}
}

// Input to a live Managed runtime is judged at every delivery: on a forbidden route it is refused
// with the reason, never delivered; an interrupt still goes through.
func TestLiveManagedInputIsJudgedAtEveryDelivery(t *testing.T) {
	home := writebackEnv(t)
	usePolicy(t, noSubscriptions)
	m := session.Meta{Name: "pin1", Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged,
		LaunchRoute: &routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a"}}
	session.WriteMeta(m)
	resumed := 0
	d := &lockingDriver{onResume: func() { resumed++ }}
	useLockingDriver(t, session.KindCodex, d)
	launch := &routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k"}
	t.Cleanup(agentreg.SwapForTest(session.KindCodex, liveAgent{oneRouteAgent{Agent: AgentOf(session.KindCodex), launch: launch}}))
	if _, err := resumeManaged(d, m); err == nil {
		t.Fatal("input to a live runtime on a forbidden route was delivered")
	} else {
		var re *StartRefusalError
		if !errors.As(err, &re) || re.Ref.Code != routes.CodePolicyForbidden {
			t.Errorf("refusal = %v, want %s", err, routes.CodePolicyForbidden)
		}
	}
	if _, err := resumeManagedFor(d, m, false); err != nil {
		t.Errorf("an interrupt was refused: %v", err)
	}
	if resumed != 1 {
		t.Errorf("the runtime was resumed %d times, want once (the interrupt)", resumed)
	}
	m.LaunchRoute = launch
	if _, err := resumeManaged(d, m); err != nil {
		t.Errorf("input on an allowed route: %v", err)
	}
}

// liveAgent is a one-route kind whose Managed runtime is alive.
type liveAgent struct{ oneRouteAgent }

func (liveAgent) ManagedRuntime() agents.ManagedRuntimeOps {
	return agents.ManagedRuntimeOps{Alive: func(string) bool { return true }, Busy: func(string) bool { return false },
		InFlight: func(string) bool { return false }, Drop: func(string) {}, RemoveLedger: func(string) {}}
}

// interruptDriver answers Resume with a handle that can be interrupted and records it.
type interruptDriver struct {
	agents.Driver
	interrupted *bool
}

func (d interruptDriver) Resume(session.Meta) (agents.ThreadHandle, error) {
	return interruptHandle{interrupted: d.interrupted}, nil
}

type interruptHandle struct {
	agents.ThreadHandle
	interrupted *bool
}

func (h interruptHandle) Interrupt(agents.InterruptOpts) (agents.InterruptResult, error) {
	*h.interrupted = true
	return agents.InterruptResult{}, nil
}

// Through /turn on a live Managed session on a forbidden route: a message is refused with the
// policy's code, an interrupt still stops the turn.
func TestTurnOnAForbiddenRouteRefusesMessagesNotInterrupts(t *testing.T) {
	home := writebackEnv(t)
	usePolicy(t, noSubscriptions)
	t.Cleanup(agentreg.SwapForTest(session.KindCodex, liveAgent{oneRouteAgent{Agent: AgentOf(session.KindCodex),
		launch: &routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a"}}}))
	interrupted := false
	prev, had := managedDrivers[session.KindCodex]
	managedDrivers[session.KindCodex] = interruptDriver{interrupted: &interrupted}
	t.Cleanup(func() {
		if had {
			managedDrivers[session.KindCodex] = prev
		} else {
			delete(managedDrivers, session.KindCodex)
		}
	})
	session.WriteMeta(session.Meta{Name: "pturn1", Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged,
		LaunchRoute: &routes.Launch{Route: routes.RouteChatGPTSubscription, Binding: "chatgpt-subscription@a"}})
	rec := callHandler(HandleSessionTurn, http.MethodPost, "pturn1", `{"op":"start","prompt":"hi"}`)
	if routeCode(t, rec.Body.String()) != routes.CodePolicyForbidden {
		t.Errorf("a message: %d %s, want %s", rec.Code, rec.Body.String(), routes.CodePolicyForbidden)
	}
	rec = callHandler(HandleSessionTurn, http.MethodPost, "pturn1", `{"op":"interrupt"}`)
	if rec.Code != http.StatusOK || !interrupted {
		t.Errorf("an interrupt: %d %s interrupted=%v", rec.Code, rec.Body.String(), interrupted)
	}
}

// A queued input becomes a turn only while the policy would leave the session running.
func TestQueuedTurnsAreHeldOnAForbiddenRoute(t *testing.T) {
	writebackEnv(t)
	usePolicy(t, noSubscriptions)
	session.WriteMeta(session.Meta{Name: "pq1", Kind: session.KindCodex, Driver: session.DriverManaged,
		LaunchRoute: &routes.Launch{Route: routes.RouteChatGPTSubscription}})
	session.WriteMeta(session.Meta{Name: "pq2", Kind: session.KindCodex, Driver: session.DriverManaged,
		LaunchRoute: &routes.Launch{Route: routes.RouteOpenAIAPI}})
	if queuedTurnAllowed("pq1") {
		t.Error("a queued input was delivered on a forbidden route")
	}
	if !queuedTurnAllowed("pq2") || !queuedTurnAllowed("no-such-session") {
		t.Error("an allowed session's queued input was held")
	}
	prevGate, prevDrop := agents.QueuedTurnGate, agents.OnHeldDropped
	t.Cleanup(func() { agents.QueuedTurnGate, agents.OnHeldDropped = prevGate, prevDrop })
	InstallHeldDropHook()
	if agents.QueuedTurnGate == nil || agents.QueuedTurnGate("pq1") {
		t.Error("the gate is not installed at boot")
	}
}
