package claude

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
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

func (f *fakeGateway) CheckRoute(_ context.Context, sel agents.RouteSelection, _ string) *agents.RouteRefusal {
	f.checked = append(f.checked, sel)
	return f.refuse
}

func withGateway(t *testing.T, g agents.RouteGateway) {
	t.Helper()
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
		if strings.Contains(body, "BEDROCK") && !strings.Contains(body, "USE_BEDROCK") || strings.Contains(body, "aws") {
			api := session.Meta{Kind: session.KindClaude, Name: "api-sc", Dir: t.TempDir(), Route: sel(RouteAPI, "")}
			if _, err := launchOf(t, api); err != nil {
				t.Errorf("%s: refused the anthropic-api route too: %v", body, err)
			}
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
