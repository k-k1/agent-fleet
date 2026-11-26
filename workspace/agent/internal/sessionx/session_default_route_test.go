package sessionx

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agentreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// zenOnly is a restricted member's policy that allows opencode.ai's Zen route alone.
var zenOnly = routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteOpencodeZen}}

var freeLaunch = routes.Launch{Route: routes.RouteOpencodeZen, Binding: "opencode-zen@free"}

// defaultPerCallAgent is perCallAgent that also proves a default model from the working folder
// (agents.DefaultLaunchResolver) and holds that proof (agents.DefaultLaunchHolder).
type defaultPerCallAgent struct {
	perCallAgent
	def   *routes.Launch
	dirs  *[]string
	holds bool
}

func (a defaultPerCallAgent) ResolveDefaultLaunch(m session.Meta, _ time.Time) routes.Launch {
	*a.dirs = append(*a.dirs, m.CWD())
	return *a.def
}

// The capabilities the wrapper would otherwise hide from the endpoints under test.
func (defaultPerCallAgent) MoveTarget() agents.MoveTargetState { return agents.MoveTargetMeasured }
func (a defaultPerCallAgent) ForkSource(m session.Meta) (string, error) {
	return a.Agent.(agents.Forker).ForkSource(m)
}

func (a defaultPerCallAgent) HoldsDefaultLaunch(session.Meta, routes.Launch) bool { return a.holds }

// swapDefaultOpencode installs the stand-in; def is what the "serve" proves, dirs the folders it
// was asked about.
func swapDefaultOpencode(t *testing.T, def routes.Launch) (*routes.Launch, *[]string) {
	t.Helper()
	cur, dirs := &def, &[]string{}
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, defaultPerCallAgent{
		perCallAgent: perCallAgent{Agent: AgentOf(session.KindOpencode), launch: &routes.Launch{}},
		def:          cur, dirs: dirs, holds: true}))
	return cur, dirs
}

// A Managed launch with no model is refused only for want of its folder: judgeNewLaunch says so,
// newLaunchPolicyRefusal still refuses, and every other shape is neither deferred nor allowed.
func TestDefaultModelLaunchIsDeferredOnlyWhereItCanBeProven(t *testing.T) {
	usePolicy(t, zenOnly)
	_, dirs := swapDefaultOpencode(t, freeLaunch)
	managed := session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged}

	ref, _, deferred := judgeNewLaunch(session.KindOpencode, nil, managed)
	if !deferred {
		t.Fatal("a Managed launch with no model was not deferred")
	}
	wantPolicyRefusal(t, "the early judgement", ref, http.StatusConflict, routes.CodePolicyRequired)
	if ref, l := newLaunchPolicyRefusal(session.KindOpencode, nil, managed); ref == nil || l != nil {
		t.Errorf("the early check let it through: %+v %+v", ref, l)
	}

	terminal := managed
	terminal.Driver = ""
	named := managed
	named.Model = "anthropic/claude-x"
	for name, tc := range map[string]struct {
		sel *routes.Selection
		m   session.Meta
	}{
		"a Terminal (CLI) launch": {nil, terminal},
		"a named model":           {nil, named},
	} {
		ref, _, deferred := judgeNewLaunch(session.KindOpencode, tc.sel, tc.m)
		if deferred || ref == nil {
			t.Errorf("%s: deferred=%v ref=%+v, want a plain refusal", name, deferred, ref)
		}
	}

	// A policy that keeps Zen out has nothing for the folder to prove.
	usePolicy(t, routepolicy.Policy{Revision: 4, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}})
	if _, _, deferred := judgeNewLaunch(session.KindOpencode, nil, managed); deferred {
		t.Error("deferred under a policy that does not allow Zen")
	}
	if len(*dirs) != 0 {
		t.Errorf("the early judgement probed %v", *dirs)
	}
}

// The deferred judgement asks about the folder the session runs in, judges only the route that
// comes back, and refuses everything the serve did not prove.
func TestDeferredDefaultRefusal(t *testing.T) {
	usePolicy(t, zenOnly)
	def, dirs := swapDefaultOpencode(t, freeLaunch)
	dir := t.TempDir()
	m := session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged, Dir: dir}

	ref, l := deferredDefaultRefusal(session.KindOpencode, m)
	if ref != nil || l == nil || *l != freeLaunch {
		t.Fatalf("a proven free folder: %+v %+v", ref, l)
	}
	if len(*dirs) != 1 || (*dirs)[0] != dir {
		t.Errorf("asked about %v, want [%s]", *dirs, dir)
	}
	*def = routes.Launch{}
	ref, l = deferredDefaultRefusal(session.KindOpencode, m)
	wantPolicyRefusal(t, "an unproven folder", ref, http.StatusConflict, routes.CodePolicyRequired)
	if l != nil {
		t.Error("a refusal handed back a launch to record")
	}
	*def = routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k"}
	ref, _ = deferredDefaultRefusal(session.KindOpencode, m)
	wantPolicyRefusal(t, "a route the policy does not allow", ref, http.StatusForbidden, routes.CodePolicyForbidden)
}

// A stored session created on the proven record starts, and starting it never probes; one whose
// record the kind no longer holds is refused.
func TestStoredDefaultRecordStartsWithoutAProbe(t *testing.T) {
	usePolicy(t, zenOnly)
	_, dirs := swapDefaultOpencode(t, freeLaunch)
	rec := freeLaunch
	m := session.Meta{Name: "dflt1", Kind: session.KindOpencode, Driver: session.DriverManaged, Dir: t.TempDir(), LaunchRoute: &rec}
	if ref := storedLaunchPolicyRefusal(m); ref != nil {
		t.Errorf("a session on the proven record: %+v", ref)
	}
	if len(*dirs) != 0 {
		t.Errorf("a start probed %v", *dirs)
	}
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, defaultPerCallAgent{
		perCallAgent: perCallAgent{Agent: AgentOf(session.KindOpencode), launch: &routes.Launch{}},
		def:          &freeLaunch, dirs: dirs, holds: false}))
	wantPolicyRefusal(t, "a record the kind no longer holds", storedLaunchPolicyRefusal(m), http.StatusConflict, routes.CodePolicyRequired)
}

// launchProbeDriver records the meta a Managed launch carried, and fails the launch.
type launchProbeDriver struct {
	agents.Driver
	got *session.Meta
}

func (d launchProbeDriver) Resume(m session.Meta) (agents.ThreadHandle, error) {
	*d.got = m
	return nil, errors.New("probe")
}

func useLaunchProbe(t *testing.T) *session.Meta {
	t.Helper()
	var got session.Meta
	prev := managedDrivers[session.KindOpencode]
	managedDrivers[session.KindOpencode] = launchProbeDriver{got: &got}
	t.Cleanup(func() { managedDrivers[session.KindOpencode] = prev })
	return &got
}

// The create endpoint under a restriction that allows Zen: a folder the serve proves free reaches
// the launch with the free record; an unproven one stays route_required and, even with a worktree
// made for it, leaves nothing behind; a Terminal launch and a policy without Zen stay refused.
func TestRestrictedDefaultModelCreate(t *testing.T) {
	p := newCreateProbe(t)
	usePolicy(t, zenOnly)
	def, dirs := swapDefaultOpencode(t, freeLaunch)
	got := useLaunchProbe(t)
	body := func(extra map[string]any) map[string]any {
		b := map[string]any{"kind": "opencode", "driver": "managed", "dir": p.repo}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	p.post(body(nil), "")
	if got.LaunchRoute == nil || *got.LaunchRoute != freeLaunch || len(*dirs) == 0 || (*dirs)[0] != p.repo {
		t.Fatalf("a proven folder launched %+v after probing %v, want the free record on %s", got.LaunchRoute, *dirs, p.repo)
	}

	for name, tc := range map[string]struct {
		def   routes.Launch
		extra map[string]any
	}{
		"an unproven folder":           {routes.Launch{}, nil},
		"an unproven fresh worktree":   {routes.Launch{}, map[string]any{"worktree": true}},
		"a forbidden proven route":     {routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k"}, map[string]any{"worktree": true}},
		"an unproven new-branch clone": {routes.Launch{}, map[string]any{"worktree": true, "new_branch": "topic/x"}},
	} {
		*def, *got = tc.def, session.Meta{}
		before := p.sideEffects()
		rec := p.post(body(tc.extra), "")
		if rec.Code == http.StatusOK || rec.Code == http.StatusCreated || got.Name != "" {
			t.Errorf("%s: %d %s, launched %+v", name, rec.Code, rec.Body, *got)
			continue
		}
		if want := routes.CodePolicyRequired; routeCode(t, rec.Body.String()) != want && tc.def.Route == "" {
			t.Errorf("%s: code %s, want %s", name, routeCode(t, rec.Body.String()), want)
		}
		if d := before.diff(p.sideEffects()); d != "" {
			t.Errorf("%s: a refused create left %s", name, d)
		}
	}

	// Terminal (CLI): the serve says nothing about a pane's own opencode.
	*def, *got = freeLaunch, session.Meta{}
	rec := p.post(map[string]any{"kind": "opencode", "dir": p.repo}, "")
	if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
		t.Errorf("a Terminal launch: %d %s", rec.Code, rec.Body)
	}
	// A policy without Zen.
	usePolicy(t, routepolicy.Policy{Revision: 5, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}})
	before := len(*dirs)
	rec = p.post(body(nil), "")
	if rec.Code != http.StatusConflict || len(*dirs) != before {
		t.Errorf("a policy without Zen: %d %s, probed %v", rec.Code, rec.Body, (*dirs)[before:])
	}
}

// Fork, recreate and move judge the folder they already have before they change anything: an
// unproven one refuses with the source session untouched.
func TestRestrictedDefaultModelFollowOns(t *testing.T) {
	usePolicy(t, zenOnly)
	def, _ := swapDefaultOpencode(t, routes.Launch{})
	t.Run("recreate", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "dfrec1", Dir: home, Kind: session.KindOpencode, Driver: session.DriverManaged})
		rec := callHandler(HandleRecreateSession, http.MethodPost, "dfrec1", "")
		cur, _ := session.ReadMeta("dfrec1")
		if rec.Code != http.StatusConflict || cur.Archived || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
			t.Errorf("recreate = %d archived=%v %s, want route_required before the old slot is archived", rec.Code, cur.Archived, rec.Body)
		}
	})
	t.Run("move", func(t *testing.T) {
		isolateAgentState(t)
		src := session.Meta{Name: "s", Kind: session.KindClaude, Dir: t.TempDir()}
		for name, tc := range map[string]struct {
			launch routes.Launch
			ok     bool
		}{"an unproven folder": {routes.Launch{}, false}, "a proven folder": {freeLaunch, true}} {
			*def = tc.launch
			err := checkMoveRequest(t.Context(), src, moveRequest{Kind: session.KindOpencode, Driver: session.DriverManaged})
			var me *moveError
			if tc.ok && err != nil || !tc.ok && (!errors.As(err, &me) || me.Code != routes.CodePolicyRequired) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("fork", func(t *testing.T) {
		home := writebackEnv(t)
		session.WriteMeta(session.Meta{Name: "dffork1", Dir: home, Kind: session.KindOpencode, Driver: session.DriverManaged})
		*def = routes.Launch{}
		rec := callHandler(HandleForkSession, http.MethodPost, "dffork1", "")
		if rec.Code != http.StatusConflict || routeCode(t, rec.Body.String()) != routes.CodePolicyRequired {
			t.Errorf("fork = %d %s, want route_required", rec.Code, rec.Body)
		}
	})
}
