package claude

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

const testBearer = "afrt_test0bedrock0bearer0not0a0secret"

// fakeGateway stands in for internal/routegw behind the agents seam.
type fakeGateway struct {
	issued  []session.Meta
	checked []agents.RouteSelection
	refuse  *agents.RouteRefusal
}

func (f *fakeGateway) Issue(m session.Meta) (agents.RouteGrant, *agents.RouteRefusal) {
	f.issued = append(f.issued, m)
	if f.refuse != nil {
		return agents.RouteGrant{}, f.refuse
	}
	return agents.RouteGrant{Socket: "/state/route-gateway.sock", BaseURL: "http://af-route-gateway.invalid",
		Token: testBearer, Region: "eu-west-1"}, nil
}

func (f *fakeGateway) Bind(sel agents.RouteSelection) (agents.RouteSelection, *agents.RouteRefusal) {
	if f.refuse != nil {
		return sel, f.refuse
	}
	sel.Connection += "#00112233aabbccdd"
	return sel, nil
}

func (f *fakeGateway) CheckRoute(_ context.Context, sel agents.RouteSelection, _ string) *agents.RouteRefusal {
	f.checked = append(f.checked, sel)
	return f.refuse
}

func withGateway(t *testing.T, g agents.RouteGateway) {
	t.Helper()
	t.Setenv(bedrockGateEnv, "1") // the release gate has its own test
	prev := agents.CurrentRouteGateway()
	agents.SetRouteGateway(g)
	t.Cleanup(func() { agents.SetRouteGateway(prev) })
}

func bedrockMeta(t *testing.T, name string) session.Meta {
	return session.Meta{Kind: session.KindClaude, Name: name, Dir: t.TempDir(), CreatedAt: "2026-11-06T09:00:00Z",
		Route: sel(RouteBedrock, routes.AWSConnection("p1", "eu-west-1"))}
}

// The launch: the bearer rides this pane's environment only; the gateway's socket, base URL and
// region ride the environment AND the flag layer (the CLI reads the socket from the environment
// alone); nothing that would put the CLI on another provider or credential survives.
func TestBedrockLaunch(t *testing.T) {
	isolateSlot(t)
	isolateClaudeConfig(t)
	gw := &fakeGateway{}
	withGateway(t, gw)
	m := bedrockMeta(t, "bed-1")
	plan, err := launchOf(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(gw.issued) != 1 || gw.issued[0].Route.Connection != "p1@eu-west-1" {
		t.Fatalf("issued = %+v, want one bearer for the stored selection", gw.issued)
	}
	wantEnv := []string{"AWS_BEARER_TOKEN_BEDROCK=" + testBearer, "CLAUDE_CODE_USE_BEDROCK=1",
		"ANTHROPIC_BEDROCK_BASE_URL=http://af-route-gateway.invalid", "ANTHROPIC_UNIX_SOCKET=/state/route-gateway.sock", "AWS_REGION=eu-west-1"}
	if !slices.Equal(plan.Env, wantEnv) {
		t.Errorf("env = %v\nwant %v", plan.Env, wantEnv)
	}
	if strings.Contains(plan.Program, testBearer) {
		t.Errorf("the bearer is on the command line: %q", plan.Program)
	}
	for _, u := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
		"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_SKIP_BEDROCK_AUTH"} {
		if !strings.Contains(plan.Program, "-u "+u+" ") {
			t.Errorf("%s is not removed: %q", u, plan.Program)
		}
	}
	for _, kept := range []string{"CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_UNIX_SOCKET", "ANTHROPIC_BEDROCK_BASE_URL", "AWS_REGION", "AWS_BEARER_TOKEN_BEDROCK"} {
		if strings.Contains(plan.Program, "-u "+kept+" ") {
			t.Errorf("%s is set by the route and removed by the launch: %q", kept, plan.Program)
		}
	}
	doc := launchSettingsOf(t, plan.Program)
	env, _ := doc["env"].(map[string]any)
	for k, v := range map[string]string{
		"CLAUDE_CODE_USE_BEDROCK": "1", "ANTHROPIC_UNIX_SOCKET": "/state/route-gateway.sock",
		"ANTHROPIC_BEDROCK_BASE_URL": "http://af-route-gateway.invalid", "AWS_REGION": "eu-west-1",
		"ANTHROPIC_API_KEY": "", "CLAUDE_CODE_OAUTH_TOKEN": "", "ANTHROPIC_BASE_URL": "", "CLAUDE_CODE_SKIP_BEDROCK_AUTH": "",
	} {
		if got, ok := env[k]; !ok || got != v {
			t.Errorf("flag layer %s = %v (present %v), want %q", k, got, ok, v)
		}
	}
	if _, ok := env["AWS_BEARER_TOKEN_BEDROCK"]; ok {
		t.Error("the bearer is in the flag layer")
	}
	if h, ok := doc["apiKeyHelper"]; !ok || h != "" {
		t.Errorf("apiKeyHelper = %v, want pinned empty", h)
	}
}

// No gateway, or a gateway that refuses (an unmarked, deleted or foreign profile, no region):
// the launch is refused with the reason, never run another way.
func TestBedrockLaunchRefusals(t *testing.T) {
	isolateSlot(t)
	isolateClaudeConfig(t)
	withGateway(t, nil)
	m := bedrockMeta(t, "bed-2")
	_, err := launchOf(t, m)
	var ref *agents.RouteRefusal
	if !errors.As(err, &ref) || ref.Code != routes.CodeRouteUnavailable {
		t.Fatalf("no gateway: err = %v", err)
	}
	if ref := (agentImpl{}).CheckRoute(context.Background(), *m.Route, ""); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Fatalf("no gateway: CheckRoute = %v", ref)
	}
	unmarked := &agents.RouteRefusal{Status: http.StatusForbidden, Code: routes.CodeRouteForbidden, Message: "profile \"x\" is not marked as a model route"}
	withGateway(t, &fakeGateway{refuse: unmarked})
	if _, err := launchOf(t, m); !errors.As(err, &ref) || ref != unmarked {
		t.Fatalf("an unmarked profile: err = %v", err)
	}
	if ref := (agentImpl{}).CheckRoute(context.Background(), *m.Route, ""); ref != unmarked {
		t.Fatalf("CheckRoute of an unmarked profile = %v", ref)
	}
	// A malformed reference is refused by the adapter itself, before the gateway is asked.
	gw := &fakeGateway{}
	withGateway(t, gw)
	for _, conn := range []string{"", "p1", "p1@eu-west-1@x", "p/1@eu-west-1"} {
		if ref := (agentImpl{}).CheckRoute(context.Background(), *sel(RouteBedrock, conn), ""); ref == nil {
			t.Errorf("connection %q was accepted", conn)
		}
	}
	if len(gw.checked) != 0 {
		t.Errorf("the gateway was asked about a malformed reference: %+v", gw.checked)
	}
	if ref := (agentImpl{}).CheckRoute(context.Background(), *sel(RouteBedrock, "p1@"), ""); ref != nil {
		t.Errorf("the profile's own region (empty) was refused: %v", ref)
	}
	if len(gw.checked) != 1 || gw.checked[0].Connection != "p1@" {
		t.Errorf("the gateway did not check the selection: %+v", gw.checked)
	}
}

// A settings file that would put the CLI on a bearer or AWS identity of its own is refused for
// bedrock; the same file does not concern the other routes.
func TestBedrockSettingsConflicts(t *testing.T) {
	isolateSlot(t)
	cfg := isolateClaudeConfig(t)
	withGateway(t, &fakeGateway{})
	withAPIKey(t, "sk-ant-api03-test-0123456789abcdef", nil)
	user := filepath.Join(cfg, "settings.json")
	for _, body := range []string{
		`{"env":{"AWS_BEARER_TOKEN_BEDROCK":"other"}}`,
		`{"env":{"CLAUDE_CODE_SKIP_BEDROCK_AUTH":"1"}}`,
		`{"awsCredentialExport":"/bin/creds"}`,
		`{"awsAuthRefresh":"aws sso login"}`,
		`{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`,
	} {
		writeFile(t, user, body)
		m := bedrockMeta(t, "bed-sc")
		_, err := launchOf(t, m)
		var ref *agents.RouteRefusal
		if !errors.As(err, &ref) || ref.Code != routes.CodeRouteSettings {
			t.Errorf("%s: bedrock err = %v, want %s", body, err, routes.CodeRouteSettings)
		}
		if ref := (agentImpl{}).CheckRoute(context.Background(), *m.Route, ""); ref == nil || ref.Code != routes.CodeRouteSettings {
			t.Errorf("%s: CheckRoute = %v", body, ref)
		}
		// The env names concern every route; the AWS credential commands only bedrock.
		api := session.Meta{Kind: session.KindClaude, Name: "api-sc", Dir: t.TempDir(), Route: sel(RouteAPI, "")}
		_, apiErr := launchOf(t, api)
		if strings.Contains(body, `"aws`) != (apiErr == nil) {
			t.Errorf("%s: anthropic-api launch err = %v", body, apiErr)
		}
	}
}

// The guard, the recovery and the ledger see the bedrock route as a credential of its own,
// billed per call.
func TestBedrockIsItsOwnCredentialAndMetered(t *testing.T) {
	dir := isolateClaudeConfig(t)
	now := time.Now()
	writeCredsExpiry(t, dir, now.Add(-48*time.Hour), now.Add(-24*time.Hour)) // a dead login
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	if !AuthExpired() {
		t.Fatal("the fixture's login should read as expired")
	}
	m := bedrockMeta(t, "bed-3")
	if AuthExpiredFor(m) {
		t.Error("an expired OAuth login refuses a bedrock session")
	}
	if !OwnsCredential(m) || UsesAPIKey(m) {
		t.Error("bedrock is not its own credential, or is taken for the API key")
	}
	turns := stampRoute(m, []transcript.Turn{{TS: "2026-11-06T09:05:00Z"}})
	if turns[0].Route != RouteBedrock || turns[0].Billing != usagex.BillingMetered {
		t.Errorf("stamped %q %q", turns[0].Route, turns[0].Billing)
	}
	if _, ref := (agentImpl{}).SwitchTarget(m); ref == nil {
		t.Error("a bedrock session was offered a switch")
	}
}

// routeOverridesFrom is the CLI version routeOverrides was taken from.
const routeOverridesFrom = "2.1.318"

// The list was read off the pinned CLI's own lists of provider switches, credentials, base URLs
// and auth skips. A bump of the pin has to look again: run `strings` on the new binary and
// compare its arrays naming CLAUDE_CODE_USE_*, *_BASE_URL, *_API_KEY / *_AUTH_TOKEN and
// CLAUDE_CODE_SKIP_*_AUTH with routeOverrides, then update routeOverridesFrom.
func TestRouteOverridesCoverTheCLIsLists(t *testing.T) {
	df, err := os.ReadFile("../../../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^ARG CLAUDE_CODE_VERSION=(\S+)`).FindSubmatch(df)
	if m == nil {
		t.Fatal("workspace/Dockerfile no longer has `ARG CLAUDE_CODE_VERSION=`")
	}
	if string(m[1]) != routeOverridesFrom {
		t.Errorf("claude is pinned at %s but routeOverrides was read off %s: re-derive it from the new CLI (see this test's comment)", m[1], routeOverridesFrom)
	}
	for _, n := range []string{
		"CLAUDE_CODE_USE_MANTLE", "ANTHROPIC_BEDROCK_MANTLE_BASE_URL", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "ANTHROPIC_AWS_BASE_URL",
		"ANTHROPIC_AWS_API_KEY", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_GATEWAY", "CLAUDE_CODE_SKIP_MANTLE_AUTH",
		"AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "ANTHROPIC_UNIX_SOCKET", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
	} {
		if !slices.Contains(routeOverrides, n) {
			t.Errorf("%s is missing from routeOverrides", n)
		}
	}
}

// Every route removes and pins empty every provider, endpoint and credential variable except the
// ones it sets itself, so nothing the pane inherits (CLAUDE_CODE_USE_MANTLE with a Mantle base
// URL, an ANTHROPIC_UNIX_SOCKET of someone else's) can move its calls or its bearer elsewhere.
func TestEveryRouteRemovesAndPinsEveryOtherSelector(t *testing.T) {
	isolateSlot(t)
	isolateClaudeConfig(t)
	withGateway(t, &fakeGateway{})
	withAPIKey(t, "sk-ant-api03-test-0123456789abcdef", nil)
	own := map[string][]string{
		RouteBedrock:      {"CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_UNIX_SOCKET", "AWS_BEARER_TOKEN_BEDROCK"},
		RouteAPI:          {"ANTHROPIC_API_KEY"},
		RouteSubscription: nil,
	}
	for route, mine := range own {
		m := bedrockMeta(t, "sel-"+route)
		if route != RouteBedrock {
			m.Route = sel(route, "")
		}
		plan, err := launchOf(t, m)
		if err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		_, env := func() (map[string]any, map[string]any) {
			doc := launchSettingsOf(t, plan.Program)
			e, _ := doc["env"].(map[string]any)
			return doc, e
		}()
		for _, n := range routeOverrides {
			if slices.Contains(mine, n) {
				if strings.Contains(plan.Program, "-u "+n+" ") {
					t.Errorf("%s: its own %s is removed", route, n)
				}
				continue
			}
			if !strings.Contains(plan.Program, "-u "+n+" ") {
				t.Errorf("%s: %s is not removed from the pane", route, n)
			}
			if v, ok := env[n]; !ok || v != "" {
				t.Errorf("%s: %s is not pinned empty in the flag layer (%v)", route, n, v)
			}
		}
	}
	// A settings file that selects Mantle refuses every route.
	cfg := isolateClaudeConfig(t)
	writeFile(t, filepath.Join(cfg, "settings.json"), `{"env":{"CLAUDE_CODE_USE_MANTLE":"1"}}`)
	for route := range own {
		m := bedrockMeta(t, "selset-"+route)
		if route != RouteBedrock {
			m.Route = sel(route, "")
		}
		var ref *agents.RouteRefusal
		if _, err := launchOf(t, m); !errors.As(err, &ref) || ref.Code != routes.CodeRouteSettings {
			t.Errorf("%s with a Mantle settings file: %v", route, err)
		}
	}
}

// The stored form of a bedrock selection is the gateway's binding; without a gateway nothing is
// stored, and a gateway's refusal (an edited profile under a bound fork) is passed on.
func TestBedrockBindRouteIsTheGateways(t *testing.T) {
	withGateway(t, &fakeGateway{})
	got, ref := agentImpl{}.BindRoute(*sel(RouteBedrock, "p1@"))
	if ref != nil || got.Connection != "p1@#00112233aabbccdd" {
		t.Fatalf("BindRoute = %+v %v", got, ref)
	}
	changed := &agents.RouteRefusal{Status: http.StatusConflict, Code: "route_connection_changed", Message: "changed"}
	withGateway(t, &fakeGateway{refuse: changed})
	if _, ref := (agentImpl{}).BindRoute(*sel(RouteBedrock, "p1@eu-west-1#00112233aabbccdd")); ref != changed {
		t.Fatalf("a changed binding = %v", ref)
	}
	withGateway(t, nil)
	if _, ref := (agentImpl{}).BindRoute(*sel(RouteBedrock, "p1@")); ref == nil {
		t.Fatal("bound without a gateway")
	}
}

// Until decision 14's acceptance run, the bedrock route is declared but closed: refused at check,
// bind and launch, and not offered; AF_CLAUDE_BEDROCK_ROUTE=1 opens it.
func TestBedrockReleaseGate(t *testing.T) {
	isolateSlot(t)
	isolateClaudeConfig(t)
	withGateway(t, &fakeGateway{})
	t.Setenv(bedrockGateEnv, "")
	m := bedrockMeta(t, "gate-1")
	m.Route.Connection += "#00112233aabbccdd"
	gated := func(what string, ref *agents.RouteRefusal) {
		t.Helper()
		if ref == nil || !strings.Contains(ref.Message, bedrockGateEnv) {
			t.Errorf("%s with the gate closed = %v", what, ref)
		}
	}
	gated("CheckRoute", agentImpl{}.CheckRoute(context.Background(), *m.Route, ""))
	_, ref := agentImpl{}.BindRoute(*m.Route)
	gated("BindRoute", ref)
	_, err := launchOf(t, m)
	var lref *agents.RouteRefusal
	errors.As(err, &lref)
	gated("the launch", lref)
	if slices.Contains(agentImpl{}.OfferedRoutes(), RouteBedrock) || !slices.Contains(agentImpl{}.Routes(), RouteBedrock) {
		t.Errorf("closed: offered %v, declared %v", agentImpl{}.OfferedRoutes(), agentImpl{}.Routes())
	}
	t.Setenv(bedrockGateEnv, "1")
	if !slices.Contains(agentImpl{}.OfferedRoutes(), RouteBedrock) {
		t.Error("open, but not offered")
	}
	if ref := (agentImpl{}).CheckRoute(context.Background(), *m.Route, ""); ref != nil {
		t.Errorf("open: CheckRoute = %v", ref)
	}
}
