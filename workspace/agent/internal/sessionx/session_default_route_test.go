package sessionx

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
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
	// onProbe runs inside ResolveDefaultLaunch, before it answers (a policy arriving meanwhile).
	onProbe func()
	// hang, when set, blocks HoldsDefaultLaunch until it is closed.
	hang chan struct{}
	// alive and dropped are the Managed runtime of a slot: whether a handle is live, and the
	// slots whose handle was dropped.
	alive   bool
	dropped *[]string
}

func (a defaultPerCallAgent) ManagedRuntime() agents.ManagedRuntimeOps {
	return agents.ManagedRuntimeOps{Alive: func(string) bool { return a.alive }, Busy: func(string) bool { return false },
		InFlight: func(string) bool { return false }, RemoveLedger: func(string) {},
		Drop: func(n string) {
			if a.dropped != nil {
				*a.dropped = append(*a.dropped, n)
			}
		}}
}

func (a defaultPerCallAgent) ResolveDefaultLaunch(m session.Meta, _ time.Time) routes.Launch {
	*a.dirs = append(*a.dirs, m.CWD())
	if a.onProbe != nil {
		a.onProbe()
	}
	return *a.def
}

// The capabilities the wrapper would otherwise hide from the endpoints under test.
func (defaultPerCallAgent) MoveTarget() agents.MoveTargetState        { return agents.MoveTargetMeasured }
func (a defaultPerCallAgent) ForkSource(session.Meta) (string, error) { return "fork-source", nil }

func (a defaultPerCallAgent) HoldsDefaultLaunch(session.Meta, routes.Launch) bool {
	if a.hang != nil {
		<-a.hang
	}
	return a.holds
}

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

// A stored session created on the proven record starts, and starting it (storedLaunchPolicyRefusal, the
// judgement behind startRefusalOf and so behind a Managed input revalidation) never probes; one whose record the kind no longer holds is
// refused.
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

// The create endpoint under a restriction that allows Zen: a directory the serve proves free
// reaches the launch with the free record; an unproven one stays route_required; a create that would
// make its directory first (worktree, clone, new space) is refused without asking the serve and
// without making anything; a Terminal launch and a policy without Zen stay refused.
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
		probe bool // the serve is asked
	}{
		"an unproven folder":       {routes.Launch{}, nil, true},
		"a forbidden proven route": {routes.Launch{Route: routes.RouteOpenAIAPI, Binding: "OPENAI_API_KEY@k"}, nil, true},
		// A copy this create would have to make first stays refused, and is never made: another
		// request could be using it by the time the serve answered.
		"a fresh worktree":              {freeLaunch, map[string]any{"worktree": true}, false},
		"a fresh worktree on a branch":  {freeLaunch, map[string]any{"worktree": true, "new_branch": "topic/x"}, false},
		"an existing branch's worktree": {freeLaunch, map[string]any{"worktree": true, "use_existing": true, "branch": "feature"}, false},
		"a clone":                       {freeLaunch, map[string]any{"remote_url": "file://" + p.repo}, false},
		"a new space":                   {freeLaunch, map[string]any{"space": map[string]any{"name": "fresh"}, "dir": ""}, false},
	} {
		*def, *got = tc.def, session.Meta{}
		asked := len(*dirs)
		before, refs, spaces := p.sideEffects(), gitRefs(t, p.repo), dirNames(filepath.Join(p.home, "spaces"))
		rec := p.post(body(tc.extra), "")
		if rec.Code == http.StatusOK || rec.Code == http.StatusCreated || got.Name != "" {
			t.Errorf("%s: %d %s, launched %+v", name, rec.Code, rec.Body, *got)
			continue
		}
		if (len(*dirs) > asked) != tc.probe {
			t.Errorf("%s: probed=%v, want %v", name, len(*dirs) > asked, tc.probe)
		}
		want, wantStatus := routes.CodePolicyRequired, http.StatusConflict
		if tc.def.Route == routes.RouteOpenAIAPI {
			want, wantStatus = routes.CodePolicyForbidden, http.StatusForbidden
		}
		if rec.Code != wantStatus || routeCode(t, rec.Body.String()) != want {
			t.Errorf("%s: %d code %s, want %d %s", name, rec.Code, routeCode(t, rec.Body.String()), wantStatus, want)
		}
		if d := before.diff(p.sideEffects()); d != "" || refs != gitRefs(t, p.repo) || spaces != dirNames(filepath.Join(p.home, "spaces")) {
			t.Errorf("%s: a refused create left %s (refs changed: %v)", name, d, refs != gitRefs(t, p.repo))
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

func dirNames(dir string) string {
	ents, _ := os.ReadDir(dir)
	var n []string
	for _, e := range ents {
		n = append(n, e.Name())
	}
	return strings.Join(n, ",")
}

func gitRefs(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "for-each-ref").CombinedOutput()
	if err != nil {
		t.Fatalf("git for-each-ref: %v: %s", err, out)
	}
	return string(out)
}

func swapDefaultAgent(t *testing.T, a defaultPerCallAgent) {
	t.Helper()
	a.perCallAgent = perCallAgent{Agent: AgentOf(session.KindOpencode), launch: &routes.Launch{}}
	t.Cleanup(agentreg.SwapForTest(session.KindOpencode, a))
}

// A policy that arrives while the serve is being asked, or a confirmation that goes stale in the
// meantime, is the one the answer is judged on, and the recreate it refuses has dropped neither the
// old slot's live Managed handle nor archived it.
func TestRecreateJudgesTheProbeOnTheCurrentPolicy(t *testing.T) {
	stale := func(t *testing.T) {
		s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
		if _, err := s.Seed(zenOnly, time.Now().Add(-time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
		routePolicyStore = func() *routepolicy.Store { return s }
	}
	for name, tc := range map[string]struct {
		change func(*testing.T)
		status int
		code   string
	}{
		"Zen dropped": {func(t *testing.T) {
			usePolicy(t, routepolicy.Policy{Revision: 9, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}})
		}, http.StatusForbidden, routes.CodePolicyForbidden},
		"confirmation went stale": {stale, http.StatusServiceUnavailable, routes.CodePolicyUnconfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			usePolicy(t, zenOnly)
			prev := routePolicyStore
			t.Cleanup(func() { routePolicyStore = prev })
			home := writebackEnv(t)
			var dropped []string
			swapDefaultAgent(t, defaultPerCallAgent{def: &freeLaunch, dirs: &[]string{}, holds: true, alive: true, dropped: &dropped,
				onProbe: func() { tc.change(t) }})
			useLaunchProbe(t)
			session.WriteMeta(session.Meta{Name: "dfrec3", Dir: home, Kind: session.KindOpencode, Driver: session.DriverManaged})
			rec := callHandler(HandleRecreateSession, http.MethodPost, "dfrec3", "")
			cur, _ := session.ReadMeta("dfrec3")
			if rec.Code != tc.status || routeCode(t, rec.Body.String()) != tc.code || cur.Archived || len(dropped) != 0 {
				t.Errorf("recreate = %d %s archived=%v dropped=%v, want %d %s with the old slot untouched", rec.Code, rec.Body, cur.Archived, dropped, tc.status, tc.code)
			}
		})
	}
}

// The refusal itself is made before anything is stopped: the answer is judged on the policy as it
// is when the probe returns, not the one read before it.
func TestDeferredDefaultRefusalReadsThePolicyAgainAfterTheProbe(t *testing.T) {
	usePolicy(t, zenOnly)
	swapDefaultAgent(t, defaultPerCallAgent{def: &freeLaunch, dirs: &[]string{}, holds: true, onProbe: func() {
		usePolicy(t, routepolicy.Policy{Revision: 9, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}})
	}})
	ref, l := deferredDefaultRefusal(session.KindOpencode, session.Meta{Kind: session.KindOpencode, Driver: session.DriverManaged, Dir: t.TempDir()})
	wantPolicyRefusal(t, "a policy that dropped Zen during the probe", ref, http.StatusForbidden, routes.CodePolicyForbidden)
	if l != nil {
		t.Error("a refusal handed back a launch to record")
	}
}

// A holder that never returns refuses the start within the launch budget instead of holding the
// Managed input gate, and a proven record starts through the real Managed resume, stopped or live,
// with no probe.
func TestManagedResumeOfTheDefaultRecord(t *testing.T) {
	isolateAgentState(t)
	usePolicy(t, zenOnly)
	prevStart := KindStartRefusal
	KindStartRefusal = nil
	t.Cleanup(func() { KindStartRefusal = prevStart })
	old := launchResolveBudget
	launchResolveBudget = 100 * time.Millisecond
	t.Cleanup(func() { launchResolveBudget = old })
	rec := freeLaunch
	m := session.Meta{Name: "dfres1", Kind: session.KindOpencode, Driver: session.DriverManaged, Dir: t.TempDir(), LaunchRoute: &rec}
	for _, alive := range []bool{false, true} {
		dirs := []string{}
		var got session.Meta
		d := launchProbeDriver{got: &got}
		swapDefaultAgent(t, defaultPerCallAgent{def: &freeLaunch, dirs: &dirs, holds: true, alive: alive})
		_, err := resumeManagedAs(d, m, true, false)
		if got.Name != m.Name || len(dirs) != 0 {
			t.Errorf("alive=%v, a held record: reached the driver=%v after %d probes (%v)", alive, got.Name == m.Name, len(dirs), err)
		}
		got = session.Meta{}
		swapDefaultAgent(t, defaultPerCallAgent{def: &freeLaunch, dirs: &dirs, holds: false, alive: alive})
		_, err = resumeManagedAs(d, m, true, false)
		var sre *StartRefusalError
		if !errors.As(err, &sre) || got.Name != "" || len(dirs) != 0 {
			t.Errorf("alive=%v, a record the kind no longer holds: err=%v reached the driver=%v", alive, err, got.Name != "")
		}
		// The hold is released by a timer too, so a lost bound fails here instead of hanging the run.
		hang := make(chan struct{})
		release := time.AfterFunc(3*time.Second, func() { close(hang) })
		swapDefaultAgent(t, defaultPerCallAgent{def: &freeLaunch, dirs: &dirs, holds: true, alive: alive, hang: hang})
		got = session.Meta{}
		begin := time.Now()
		_, err = resumeManagedAs(d, m, true, false)
		release.Stop()
		if !errors.As(err, &sre) || got.Name != "" || time.Since(begin) > 2*time.Second {
			t.Errorf("alive=%v, a hung holder: err=%v reached the driver=%v after %v, want a refusal within the budget", alive, err, got.Name != "", time.Since(begin))
		}
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
	t.Run("recreate on a proven folder", func(t *testing.T) {
		home := writebackEnv(t)
		*def = freeLaunch
		got := useLaunchProbe(t)
		session.WriteMeta(session.Meta{Name: "dfrec2", Dir: home, Kind: session.KindOpencode, Driver: session.DriverManaged})
		callHandler(HandleRecreateSession, http.MethodPost, "dfrec2", "")
		if got.LaunchRoute == nil || *got.LaunchRoute != freeLaunch {
			t.Errorf("the new slot launched with %+v, want the free record", got.LaunchRoute)
		}
	})
	t.Run("move", func(t *testing.T) {
		// A move's target stays on the early check: its create runs under the move's lock.
		isolateAgentState(t)
		src := session.Meta{Name: "s", Kind: session.KindClaude, Dir: t.TempDir()}
		*def = freeLaunch
		err := checkMoveRequest(t.Context(), src, moveRequest{Kind: session.KindOpencode, Driver: session.DriverManaged})
		var me *moveError
		if !errors.As(err, &me) || me.Code != routes.CodePolicyRequired {
			t.Errorf("a proven folder: %v, want route_required", err)
		}
		if got := moveCreateReq(src, moveRecord{Kind: session.KindOpencode}); !got.noProbe {
			t.Error("a move's create may probe the serve under the move's lock")
		}
	})
	t.Run("fork on a proven folder", func(t *testing.T) {
		home := writebackEnv(t)
		*def = freeLaunch
		got := useLaunchProbe(t)
		session.WriteMeta(session.Meta{Name: "dffork2", Dir: home, Kind: session.KindOpencode, Driver: session.DriverManaged})
		callHandler(HandleForkSession, http.MethodPost, "dffork2", "")
		if got.LaunchRoute == nil || *got.LaunchRoute != freeLaunch {
			t.Errorf("the fork launched with %+v, want the free record", got.LaunchRoute)
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
