package claude

import (
	"context"
	"errors"
	"net/http"
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

func withAPIKey(t *testing.T, key string, err error) {
	t.Helper()
	prev := loadAPIKey
	loadAPIKey = func() (string, error) { return key, err }
	t.Cleanup(func() { loadAPIKey = prev })
}

func sel(route, conn string) *routes.Selection {
	return &routes.Selection{Route: route, Connection: conn}
}

func TestClaudeDeclaresBothRoutesAndIsAnAdapter(t *testing.T) {
	var a agents.Agent = agentImpl{}
	ra, ok := a.(agents.RouteAdapter)
	if !ok {
		t.Fatal("claude is not a RouteAdapter")
	}
	if _, ok := a.(agents.RouteChecker); !ok {
		t.Fatal("claude is not a RouteChecker")
	}
	got := ra.Routes()
	slices.Sort(got)
	if !slices.Equal(got, []string{RouteAPI, RouteSubscription}) {
		t.Errorf("Routes = %v", got)
	}
}

// The credential of one route reaches its own session and the other route's credentials are
// removed from the process; nothing goes through the Agent's own environment.
func TestApplyRouteIsolatesCredentials(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-test", nil)
	api, err := agentImpl{}.ApplyRoute(session.Meta{}, *sel(RouteAPI, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.Env, []string{"ANTHROPIC_API_KEY=sk-ant-api03-test"}) {
		t.Errorf("api env = %v", api.Env)
	}
	if !slices.Contains(api.Unset, "CLAUDE_CODE_OAUTH_TOKEN") || slices.Contains(api.Unset, "ANTHROPIC_API_KEY") {
		t.Errorf("api unset = %v: the OAuth token must go, the key must stay", api.Unset)
	}
	sub, err := agentImpl{}.ApplyRoute(session.Meta{}, *sel(RouteSubscription, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Env) != 0 {
		t.Errorf("subscription env = %v, want none", sub.Env)
	}
	for _, v := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		if !slices.Contains(sub.Unset, v) {
			t.Errorf("subscription unset = %v, lacks %s", sub.Unset, v)
		}
	}
	for _, v := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"} {
		if !slices.Contains(api.Unset, v) {
			t.Errorf("api unset = %v, lacks %s", api.Unset, v)
		}
	}
}

func TestRouteChecksWhoseConnectionAndWhetherItIsThere(t *testing.T) {
	ctx := context.Background()
	withAPIKey(t, "sk-ant-api03-test", nil)
	ok := func(s *routes.Selection) *agents.RouteRefusal { return agentImpl{}.CheckRoute(ctx, *s, "") }
	if ref := ok(sel(RouteAPI, "")); ref != nil {
		t.Errorf("own key, empty ref: %+v", ref)
	}
	if ref := ok(sel(RouteAPI, "ANTHROPIC_API_KEY")); ref != nil {
		t.Errorf("own key, named ref: %+v", ref)
	}
	if ref := ok(sel(RouteAPI, "someone-elses-key")); ref == nil || ref.Code != routes.CodeRouteForbidden || ref.Status != http.StatusForbidden {
		t.Errorf("another connection: %+v, want forbidden", ref)
	}
	if ref := ok(sel(RouteSubscription, "")); ref != nil {
		t.Errorf("subscription: %+v", ref)
	}
	if ref := ok(sel(RouteSubscription, "x")); ref == nil || ref.Code != routes.CodeRouteForbidden {
		t.Errorf("subscription with a connection: %+v, want forbidden", ref)
	}
	for name, err := range map[string]error{"unreadable store": errors.New("sealed")} {
		withAPIKey(t, "", err)
		if ref := ok(sel(RouteAPI, "")); ref == nil || ref.Code != routes.CodeRouteUnavailable {
			t.Errorf("%s: %+v, want unavailable", name, ref)
		}
	}
	withAPIKey(t, "  ", nil)
	if ref := ok(sel(RouteAPI, "")); ref == nil || ref.Code != routes.CodeRouteUnavailable {
		t.Errorf("no key: %+v, want unavailable", ref)
	}
	// The subscription route needs no key.
	if ref := ok(sel(RouteSubscription, "")); ref != nil {
		t.Errorf("subscription without a key: %+v", ref)
	}
	if _, err := (agentImpl{}).ApplyRoute(session.Meta{}, *sel(RouteAPI, "")); err == nil {
		t.Error("ApplyRoute with no key must fail, not launch without a credential")
	}
}

func TestBuildLaunchRouteEnvAndProgram(t *testing.T) {
	withAPIKey(t, "sk-ant-api03-secret", nil)
	isolateSlot(t)
	base := session.Meta{Kind: session.KindClaude, Name: "r-none", Dir: t.TempDir()}
	none, err := launchOf(t, base)
	if err != nil {
		t.Fatal(err)
	}
	// An empty selection: no environment added, nothing removed beyond what it always removed.
	if len(none.Env) != 0 || strings.Contains(none.Program, "ANTHROPIC_API_KEY") || strings.Contains(none.Program, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("empty selection changed the launch: env=%v program=%q", none.Env, none.Program)
	}

	api := base
	api.Name, api.Route = "r-api", sel(RouteAPI, "")
	plan, err := launchOf(t, api)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Env, []string{"ANTHROPIC_API_KEY=sk-ant-api03-secret"}) {
		t.Errorf("api plan env = %v", plan.Env)
	}
	if strings.Contains(plan.Program, "sk-ant") {
		t.Errorf("the key is in the command string (visible in /proc cmdline): %q", plan.Program)
	}
	if !strings.HasPrefix(plan.Program, "env -u ") || !strings.Contains(plan.Program, "-u CLAUDE_CODE_OAUTH_TOKEN") ||
		strings.Contains(plan.Program, "-u ANTHROPIC_API_KEY") || !strings.Contains(plan.Program, "claude ") {
		t.Errorf("api program = %q", plan.Program)
	}

	sub := base
	sub.Name, sub.Route = "r-sub", sel(RouteSubscription, "")
	plan, err = launchOf(t, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Env) != 0 || !strings.Contains(plan.Program, "-u ANTHROPIC_API_KEY") || !strings.Contains(plan.Program, "-u CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("subscription plan = env %v program %q", plan.Env, plan.Program)
	}

	// A missing key stops the launch; it never falls through to a keyless launch.
	withAPIKey(t, "", nil)
	if _, err := launchOf(t, api); err == nil {
		t.Error("an api session launched with no key")
	}
}

// decision 9: the guard judges the session's own route.
func TestAuthExpiredForIsPerRoute(t *testing.T) {
	dir := isolateClaudeConfig(t)
	now := time.Now()
	writeCredsExpiry(t, dir, now.Add(-48*time.Hour), now.Add(-24*time.Hour)) // a dead login
	empty := session.Meta{Kind: session.KindClaude}
	api := session.Meta{Kind: session.KindClaude, Route: sel(RouteAPI, "")}
	sub := session.Meta{Kind: session.KindClaude, Route: sel(RouteSubscription, "")}

	if !AuthExpired() || !AuthExpiredFor(empty) {
		t.Fatal("the fixture's login should read as expired for an empty selection")
	}
	if AuthExpiredFor(api) {
		t.Error("an anthropic-api session is refused because the OAuth login expired")
	}
	if !AuthExpiredFor(sub) {
		t.Error("a subscription session must still see its login expired")
	}

	// An environment token in the Agent hides the expiry for an empty selection (today's rule)
	// but not for a subscription session, which runs without it.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-env")
	if AuthExpiredFor(empty) != AuthExpired() || AuthExpired() {
		t.Error("an empty selection must keep answering exactly AuthExpired")
	}
	if !AuthExpiredFor(sub) {
		t.Error("a token in the Agent's environment hid the subscription session's expiry")
	}

	// A live login: nothing is expired anywhere.
	writeCredsExpiry(t, dir, now.Add(time.Hour), now.Add(48*time.Hour))
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	for name, m := range map[string]session.Meta{"empty": empty, "api": api, "sub": sub} {
		if AuthExpiredFor(m) {
			t.Errorf("%s: expired with a live login", name)
		}
	}
}

// The ledger sees the session's route, and API turns as metered; no selection stays unresolved.
func TestStampRoute(t *testing.T) {
	turns := []transcript.Turn{{}, {}}
	if got := stampRoute(session.Meta{}, turns); got[0].Route != "" || got[0].Billing != "" {
		t.Errorf("no selection stamped %+v", got[0])
	}
	got := stampRoute(session.Meta{Route: sel(RouteAPI, "")}, turns)
	if got[1].Route != RouteAPI || got[1].Billing != usagex.BillingMetered {
		t.Errorf("api turn = %+v", got[1])
	}
	if turns[0].Route != "" {
		t.Error("stampRoute wrote into the shared slice")
	}
	got = stampRoute(session.Meta{Route: sel(RouteSubscription, "")}, turns)
	if got[0].Route != RouteSubscription || got[0].Billing != "" {
		t.Errorf("subscription turn = %+v, want the route and an unresolved billing", got[0])
	}
	r := agentImpl{}.ResolveRoute(session.Meta{Route: sel(RouteAPI, "c")}, agents.TurnInfo{})
	if r.Route != RouteAPI || r.Connection != "c" || (agentImpl{}.ResolveRoute(session.Meta{}, agents.TurnInfo{}) != agents.ResolvedRoute{}) {
		t.Errorf("ResolveRoute = %+v", r)
	}
}
