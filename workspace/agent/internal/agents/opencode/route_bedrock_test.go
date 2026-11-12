package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	testBearer   = "afrt_test0opencode0bedrock0bearer"
	testLoopback = "http://127.0.0.1:43011"
	bedrockID    = "amazon-bedrock/anthropic.claude-sonnet-4-6"
)

// fakeGateway stands in for internal/routegw behind the agents seam.
type fakeGateway struct {
	loopback []session.Meta
	sockets  int
	refuse   *agents.RouteRefusal
	mu       sync.Mutex
	ended    []string
}

func (f *fakeGateway) EndSession(name string) int {
	f.mu.Lock()
	f.ended = append(f.ended, name)
	f.mu.Unlock()
	return 1
}

func (f *fakeGateway) endedFor(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.ended {
		if e == name {
			n++
		}
	}
	return n
}

func (f *fakeGateway) Issue(session.Meta) (agents.RouteGrant, *agents.RouteRefusal) {
	f.sockets++
	return agents.RouteGrant{Socket: "/state/route-gateway.sock", BaseURL: "http://af-route-gateway.invalid", Token: "socket-token", Region: "eu-west-1"}, nil
}

func (f *fakeGateway) IssueLoopback(m session.Meta) (agents.RouteGrant, *agents.RouteRefusal) {
	f.loopback = append(f.loopback, m)
	if f.refuse != nil {
		return agents.RouteGrant{}, f.refuse
	}
	return agents.RouteGrant{BaseURL: testLoopback, Token: testBearer, Region: "eu-west-1"}, nil
}

func (f *fakeGateway) Bind(sel agents.RouteSelection) (agents.RouteSelection, *agents.RouteRefusal) {
	if f.refuse != nil {
		return sel, f.refuse
	}
	sel.Connection += "#00112233aabbccdd"
	return sel, nil
}

func (f *fakeGateway) CheckRoute(context.Context, agents.RouteSelection, string) *agents.RouteRefusal {
	return f.refuse
}

// withGateway opens the gate and registers g for the test.
func withGateway(t *testing.T, g agents.RouteGateway) {
	t.Helper()
	t.Setenv(bedrockGateEnv, "1")
	prev := agents.CurrentRouteGateway()
	agents.SetRouteGateway(g)
	t.Cleanup(func() { agents.SetRouteGateway(prev) })
}

func bedrockSel() agents.RouteSelection {
	return agents.RouteSelection{Route: RouteBedrock, Connection: "prof1@eu-west-1#00112233aabbccdd"}
}

func bedrockMeta(t *testing.T, name string) session.Meta {
	t.Helper()
	sel := bedrockSel()
	m := terminalMeta(t, name)
	m.Model, m.Route = bedrockID, &sel
	return m
}

func TestBedrockRouteIsGatedUntilItsLiveAcceptance(t *testing.T) {
	isolatedHome(t)
	g := &fakeGateway{}
	withGateway(t, g)
	t.Setenv(bedrockGateEnv, "")
	a := agentImpl{}
	if !slices.Contains(a.Routes(), RouteBedrock) {
		t.Fatal("bedrock must stay declared (the CP snapshot and stored selections know it)")
	}
	if len(a.OfferedRoutes()) != 0 {
		t.Fatalf("offered with the gate shut: %v", a.OfferedRoutes())
	}
	for name, ref := range map[string]*agents.RouteRefusal{
		"check": a.CheckRoute(context.Background(), bedrockSel(), bedrockID),
		"bind":  func() *agents.RouteRefusal { _, r := a.BindRoute(bedrockSel()); return r }(),
	} {
		if ref == nil || !strings.Contains(ref.Message, bedrockGateEnv) || !strings.Contains(ref.Message, "AF_WS_OPENCODE_BEDROCK_ROUTE") {
			t.Errorf("%s with the gate shut = %+v", name, ref)
		}
	}
	if _, err := a.ApplyRoute(bedrockMeta(t, "s"), bedrockSel()); err == nil {
		t.Error("ApplyRoute with the gate shut was accepted")
	}
	if len(g.loopback) != 0 {
		t.Fatal("a token was issued with the gate shut")
	}
	// claude's gate does not open it.
	t.Setenv("AF_CLAUDE_BEDROCK_ROUTE", "1")
	if len(a.OfferedRoutes()) != 0 {
		t.Fatal("claude's gate opened opencode's row")
	}
	t.Setenv(bedrockGateEnv, "1")
	if !slices.Equal(a.OfferedRoutes(), []string{RouteBedrock}) {
		t.Fatalf("offered with the gate open: %v", a.OfferedRoutes())
	}
}

// A Managed session takes the route too: ApplyRoute gives it the same plan as a pane (#4004).
func TestBedrockRouteAppliesUnderManaged(t *testing.T) {
	isolatedHome(t)
	g := &fakeGateway{}
	withGateway(t, g)
	m := bedrockMeta(t, "managed")
	m.Driver = session.DriverManaged
	plan, err := agentImpl{}.ApplyRoute(m, bedrockSel())
	if err != nil {
		t.Fatalf("ApplyRoute for a managed session: %v", err)
	}
	if len(g.loopback) != 1 || !slices.ContainsFunc(plan.Env, func(kv string) bool { return strings.HasPrefix(kv, "AWS_BEARER_TOKEN_BEDROCK=") }) {
		t.Fatalf("no loopback bearer for the managed session: issued %d, env %v", len(g.loopback), plan.Env)
	}
}

// A session on the route stays on the route's models (ADR 0128 decision 6): opencode's default
// model and another provider's are refused at create, not on the first message.
func TestBedrockRouteChecksTheModel(t *testing.T) {
	isolatedHome(t)
	withGateway(t, &fakeGateway{})
	a := agentImpl{}
	for _, model := range []string{"", "opencode/big-pickle", "anthropic/claude-sonnet-4-6", "amazon-bedrock/", "amazon-bedrock/bad model"} {
		if ref := a.CheckRoute(context.Background(), bedrockSel(), model); ref == nil || ref.Code != routes.CodeRouteModel {
			t.Errorf("model %q = %+v, want %s", model, ref, routes.CodeRouteModel)
		}
	}
	for _, model := range []string{bedrockID, "amazon-bedrock/us.anthropic.claude-opus-4-7", "amazon-bedrock/global.openai.gpt-6-luna#high"} {
		if ref := a.CheckRoute(context.Background(), bedrockSel(), model); ref != nil {
			t.Errorf("model %q refused: %+v", model, ref)
		}
	}
	if !a.RouteOwnsModels(RouteBedrock) {
		t.Error("the route must own its models: the kind's catalogue lists no Bedrock model for the default resolution")
	}
	// A connection that is not a profile reference, and a route opencode does not run on.
	if ref := a.CheckRoute(context.Background(), agents.RouteSelection{Route: RouteBedrock, Connection: "nope"}, bedrockID); ref == nil {
		t.Error("a non-profile connection was accepted")
	}
	if ref := a.CheckRoute(context.Background(), agents.RouteSelection{Route: "anthropic-api"}, bedrockID); ref == nil || ref.Code != routes.CodeRouteUnsupported {
		t.Errorf("anthropic-api = %+v", ref)
	}
}

func TestBedrockRouteNeedsTheGateway(t *testing.T) {
	isolatedHome(t)
	withGateway(t, nil)
	a := agentImpl{}
	if ref := a.CheckRoute(context.Background(), bedrockSel(), bedrockID); ref == nil || ref.Status != http.StatusServiceUnavailable {
		t.Fatalf("no gateway = %+v", ref)
	}
	if _, ref := a.BindRoute(bedrockSel()); ref == nil {
		t.Fatal("bound without a gateway")
	}
}

// launchConfig reads the inline config a launch line carries (OPENCODE_CONFIG_CONTENT='…').
func launchConfig(t *testing.T, program string) map[string]any {
	t.Helper()
	m := regexp.MustCompile(`OPENCODE_CONFIG_CONTENT='([^']*)'`).FindStringSubmatch(program)
	if m == nil {
		t.Fatalf("no inline config in %q", program)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(m[1]), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func envHas(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// The launch: a private server in the pane (never af's shared serve), the bearer and region in the
// pane's environment only, none of the workspace's provider keys or engine token, and the inline
// config that pins the one provider to the gateway's loopback port with bearer auth.
func TestBedrockLaunchIsAPrivateServerPinnedToTheGateway(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 2)
	g := &fakeGateway{}
	withGateway(t, g)
	// What a plain launch would carry: a provider key and the engine token.
	prevPref, prevEngine := UsagePref, EngineEnv
	UsagePref = func() string { return UsageZen }
	EngineEnv = func(string) []string { return []string{EngineProviderKeyEnv + "=engine-token"} }
	t.Cleanup(func() { UsagePref, EngineEnv = prevPref, prevEngine })
	if err := secrets.Update(func(d *secrets.Data) error {
		d.Opencode = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-workspace-key"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := envHas(CurrentLaunch().Env(), "ANTHROPIC_API_KEY"); !ok {
		t.Fatal("positive control: the plain launch environment should carry the stored key")
	}
	m := bedrockMeta(t, "oc-bedrock")
	plan, err := agentImpl{}.BuildLaunch(m, agents.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.loopback) != 1 || g.sockets != 0 {
		t.Fatalf("grants: loopback %d, socket %d; want one loopback grant", len(g.loopback), g.sockets)
	}
	if !strings.Contains(plan.Program, "--standalone") || strings.Contains(plan.Program, "--server") {
		t.Fatalf("not a private server: %q", plan.Program)
	}
	if strings.Contains(plan.Program, testBearer) {
		t.Fatal("the bearer is on the command line")
	}
	if !strings.HasPrefix(plan.Program, "unset OPENCODE_CONFIG OPENCODE_CONFIG_DIR ") {
		t.Fatalf("the other config sources are not removed: %q", plan.Program)
	}
	if v, _ := envHas(plan.Env, "AWS_BEARER_TOKEN_BEDROCK"); v != testBearer {
		t.Fatalf("bearer = %q", v)
	}
	if v, _ := envHas(plan.Env, "AWS_REGION"); v != "eu-west-1" {
		t.Fatalf("region = %q", v)
	}
	// The config root opencode reads is the one the conflict check reads, whatever the tmux
	// server's environment carries.
	if v, _ := envHas(plan.Env, "XDG_CONFIG_HOME"); v != filepath.Dir(paths.OpencodeConfigDir()) {
		t.Fatalf("XDG_CONFIG_HOME = %q, want %q", v, filepath.Dir(paths.OpencodeConfigDir()))
	}
	if v, _ := envHas(plan.Env, "AF_SESSION_SID"); v != session.UUID(m.Dir, m.Name) {
		t.Fatalf("AF_SESSION_SID = %q", v)
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", EngineProviderKeyEnv} {
		if _, ok := envHas(plan.Env, k); ok {
			t.Errorf("the route pane carries %s", k)
		}
	}
	cfg := launchConfig(t, plan.Program)
	if cfg["model"] != bedrockID || cfg["small_model"] != bedrockID {
		t.Errorf("model %v small_model %v", cfg["model"], cfg["small_model"])
	}
	if ep, _ := cfg["enabled_providers"].([]any); len(ep) != 1 || ep[0] != "amazon-bedrock" {
		t.Errorf("enabled_providers = %v", cfg["enabled_providers"])
	}
	settings := cfg["providers"].(map[string]any)["amazon-bedrock"].(map[string]any)["settings"].(map[string]any)
	if settings["baseURL"] != testLoopback || settings["auth"] != "bearer" {
		t.Errorf("provider settings = %v", settings)
	}
	if mode := slotModes.Read(session.UUID(m.Dir, m.Name)); mode != modeStandalone {
		t.Errorf("slot mode = %q, want standalone", mode)
	}
}

// A resume sets the model through the API before the TUI attaches; the route's config is
// exported in front of the whole line so those steps and the TUI read the same pin.
func TestBedrockResumeExportsTheRouteConfig(t *testing.T) {
	extra := routeConfig(testLoopback, bedrockID)
	line := buildProgramV2With(CurrentLaunch(), bedrockID, "", "ses_abc", false, extra)
	if !strings.HasPrefix(line, "export OPENCODE_CONFIG_CONTENT='") {
		t.Fatalf("resume line = %q", line)
	}
	cfg := launchConfig(t, line)
	if cfg["small_model"] != bedrockID || cfg["providers"] == nil {
		t.Fatalf("exported config = %v", cfg)
	}
	if !strings.Contains(line, "/model -d") || !strings.Contains(line, `"providerID":"amazon-bedrock"`) {
		t.Fatalf("the model is not applied before the TUI: %q", line)
	}
	// Without a route nothing is exported: the plain launch line is unchanged.
	if plain := buildProgramV2(CurrentLaunch(), bedrockID, "", "ses_abc", false); strings.Contains(plain, "export ") {
		t.Fatalf("plain resume exports: %q", plain)
	}
}

// A config file whose enabled_providers would win over the launch's pin refuses the route: the
// member's own file at create and every start, the working copy's at launch and while it runs.
func TestBedrockRouteRefusesAConfigThatWidensTheProviders(t *testing.T) {
	isolatedHome(t)
	useMajor(t, 2)
	withGateway(t, &fakeGateway{})
	a := agentImpl{}
	global := filepath.Join(paths.OpencodeConfigDir(), "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	// A JSONC file with comments and a trailing comma that does NOT set it: no conflict.
	_ = os.WriteFile(global, []byte("{\n  // mine\n  \"model\": \"opencode/big-pickle\", /* x */\n  \"note\": \"a // b, }\",\n}\n"), 0o644)
	if ref := a.CheckRoute(context.Background(), bedrockSel(), bedrockID); ref != nil {
		t.Fatalf("a harmless JSONC file refused the route: %+v", ref)
	}
	_ = os.WriteFile(global, []byte("{\n  // mine\n  \"enabled_providers\": [\"opencode\"],\n}\n"), 0o644)
	if ref := a.CheckRoute(context.Background(), bedrockSel(), bedrockID); ref == nil || ref.Code != routes.CodeRouteSettings || !strings.Contains(ref.Message, global) {
		t.Fatalf("global enabled_providers = %+v", ref)
	}
	_ = os.Remove(global)

	m := bedrockMeta(t, "oc-proj")
	if ref := a.RouteHeld(m); ref != nil {
		t.Fatalf("held with no config: %+v", ref)
	}
	proj := filepath.Join(m.Dir, ".opencode", "opencode.json")
	_ = os.MkdirAll(filepath.Dir(proj), 0o755)
	_ = os.WriteFile(proj, []byte(`{"enabled_providers":["openai"]}`), 0o644)
	if ref := a.RouteHeld(m); ref == nil || !strings.Contains(ref.Message, proj) {
		t.Fatalf("project enabled_providers while running = %+v", ref)
	}
	if _, err := a.BuildLaunch(m, agents.LaunchOpts{}); err == nil {
		t.Fatal("launched with a project file that widens the providers")
	}
	// An unparsable file is judged by the key's presence.
	_ = os.WriteFile(proj, []byte(`{"enabled_providers": [ broken`), 0o644)
	if ref := a.RouteHeld(m); ref == nil {
		t.Fatal("an unparsable file naming enabled_providers was let through")
	}
}

func TestStripJSONC(t *testing.T) {
	in := "{ \"a\": \"x // not a comment\", // c\n \"b\": [1, 2, /* d */ ], \"c\": \"/* s */,}\", }"
	var v map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(in)), &v); err != nil {
		t.Fatalf("%v: %s", err, stripJSONC([]byte(in)))
	}
	if v["a"] != "x // not a comment" || v["c"] != "/* s */,}" || len(v["b"].([]any)) != 2 {
		t.Fatalf("parsed %v", v)
	}
}

// The route's menu: opencode's amazon-bedrock models while the route is offered, nothing while it
// is not; the listing activates the provider with a placeholder and never the AWS chain.
func TestBedrockRouteModelsFollowTheRoutesAvailability(t *testing.T) {
	isolatedHome(t)
	calls := 0
	prev := listBedrockModels
	listBedrockModels = func() ([]string, error) {
		calls++
		return []string{bedrockID, "opencode/big-pickle", "amazon-bedrock/global.openai.gpt-6-luna"}, nil
	}
	t.Cleanup(func() {
		listBedrockModels = prev
		routeModels.mu.Lock()
		routeModels.list, routeModels.at = nil, routeModels.at.AddDate(-1, 0, 0)
		routeModels.mu.Unlock()
	})
	routeModels.list = nil
	t.Setenv(bedrockGateEnv, "")
	if got := BedrockRouteModels(); got != nil || calls != 0 {
		t.Fatalf("gate shut: %v (%d listings)", got, calls)
	}
	withGateway(t, &fakeGateway{})
	got := BedrockRouteModels()
	if !slices.Equal(got, []string{bedrockID, "amazon-bedrock/global.openai.gpt-6-luna"}) {
		t.Fatalf("route models = %v", got)
	}
	BedrockRouteModels()
	if calls != 1 {
		t.Fatalf("listed %d times; the menu is cached", calls)
	}
	// A failed listing keeps the last good one.
	routeModels.at = routeModels.at.AddDate(0, 0, -1)
	listBedrockModels = func() ([]string, error) { return nil, errors.New("boom") }
	if got := BedrockRouteModels(); len(got) != 2 {
		t.Fatalf("after a failed listing = %v", got)
	}

	env := bedrockListEnv([]string{"AWS_PROFILE=leak", "AWS_ACCESS_KEY_ID=AKIA", "PATH=/bin"})
	for _, k := range []string{"AWS_PROFILE", "AWS_ACCESS_KEY_ID"} {
		if _, ok := envHas(env, k); ok {
			t.Errorf("the listing sees %s", k)
		}
	}
	if v, _ := envHas(env, "AWS_BEARER_TOKEN_BEDROCK"); v == "" || strings.HasPrefix(v, "afrt_") {
		t.Errorf("listing bearer = %q, want a placeholder", v)
	}
}

// What else in a config file outranks the inline pin (measured on 2.2.6 by review): a per-model
// endpoint under the amazon-bedrock provider sent the bearer past the gateway, and a provider.use
// policy brought other providers back. Each refuses the route; unrelated settings do not.
func TestBedrockRouteRefusesConfigThatOutranksThePin(t *testing.T) {
	refused := map[string]string{
		"v2 per-model endpoint": `{"providers":{"amazon-bedrock":{"models":{"anthropic.claude-sonnet-4-6":{"settings":{"baseURL":"http://elsewhere"}}}}}}`,
		"v1 per-model endpoint": `{"provider":{"amazon-bedrock":{"models":{"x":{"options":{"baseURL":"http://elsewhere"}}}}}}`,
		"provider settings":     `{"providers":{"amazon-bedrock":{"settings":{"apiKey":"k"}}}}`,
		"policies allow-all":    `{"policies":[{"action":"provider.use","resource":"*","effect":"allow"}]}`,
		"experimental policies": `{"experimental":{"policies":[{"action":"provider.use","resource":"*","effect":"allow"}]}}`,
		"wildcard action":       `{"policies":[{"action":"*","resource":"*","effect":"allow"}]}`,
		"action not a string":   `{"policies":[{"resource":"*","effect":"allow"}]}`,
		"unparsable, names it":  `{"providers": {"amazon-bedrock": broken`,
	}
	for name, body := range refused {
		f := filepath.Join(t.TempDir(), "opencode.json")
		_ = os.WriteFile(f, []byte(body), 0o644)
		if ref := configConflict([]string{f}); ref == nil || ref.Code != routes.CodeRouteSettings {
			t.Errorf("%s: not refused (%+v)", name, ref)
		}
	}
	allowed := map[string]string{
		"another provider":     `{"provider":{"llamacpp":{"options":{"baseURL":"http://engine"}}}}`,
		"unrelated policy":     `{"policies":[{"action":"tool.bash","resource":"*","effect":"deny"}]}`,
		"mcp and instructions": `{"mcp":{"af":{"type":"local"}},"instructions":["AGENTS.md"]}`,
		"empty policies":       `{"policies":[]}`,
	}
	for name, body := range allowed {
		f := filepath.Join(t.TempDir(), "opencode.json")
		_ = os.WriteFile(f, []byte(body), 0o644)
		if ref := configConflict([]string{f}); ref != nil {
			t.Errorf("%s: refused: %+v", name, ref)
		}
	}
}
