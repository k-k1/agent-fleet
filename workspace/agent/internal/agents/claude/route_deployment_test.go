package claude

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// offeringGateway is fakeGateway that also answers the launch dialog's offer.
type offeringGateway struct {
	fakeGateway
	offered bool
}

func (g *offeringGateway) Offered(string) bool { return g.offered }

func deploymentMeta(t *testing.T, name string) session.Meta {
	return session.Meta{Kind: session.KindClaude, Name: name, Dir: t.TempDir(), CreatedAt: "2026-11-08T09:00:00Z",
		Model: testBedrockModel, Route: sel(RouteBedrockDeployment, "src1#0123456789abcdef")}
}

// The deployment's route has a gate of its own: closed, it is refused at check, bind and launch
// and not offered, whatever the member route's gate says; open, it is offered only while the
// gateway (the CP) says the member may use it.
func TestBedrockDeploymentGate(t *testing.T) {
	isolateSlot(t)
	isolateClaudeConfig(t)
	gw := &offeringGateway{offered: true}
	withGateway(t, gw) // opens the member route's gate, not this one
	t.Setenv(deploymentGateEnv, "")
	m := deploymentMeta(t, "dep-1")
	gated := func(what string, ref *agents.RouteRefusal) {
		t.Helper()
		if ref == nil || !strings.Contains(ref.Message, "AF_PROVIDED_ROUTES=1") {
			t.Errorf("%s with the gate closed = %v", what, ref)
		}
	}
	gated("CheckRoute", agentImpl{}.CheckRoute(context.Background(), *m.Route, testBedrockModel))
	_, ref := agentImpl{}.BindRoute(*m.Route)
	gated("BindRoute", ref)
	_, err := launchOf(t, m)
	var lref *agents.RouteRefusal
	errors.As(err, &lref)
	gated("the launch", lref)
	if slices.Contains(agentImpl{}.OfferedRoutes(), RouteBedrockDeployment) {
		t.Errorf("closed but offered: %v", agentImpl{}.OfferedRoutes())
	}
	if len(gw.issued)+len(gw.checked) != 0 {
		t.Errorf("the gateway was asked with the gate closed: %d issued, %d checked", len(gw.issued), len(gw.checked))
	}
	t.Setenv(deploymentGateEnv, "1")
	if !slices.Contains(agentImpl{}.OfferedRoutes(), RouteBedrockDeployment) {
		t.Error("open and usable, but not offered")
	}
	gw.offered = false
	if slices.Contains(agentImpl{}.OfferedRoutes(), RouteBedrockDeployment) {
		t.Error("offered while the CP refuses it")
	}
	// The member route's gate does not open this one, and this one does not open that.
	t.Setenv(bedrockGateEnv, "")
	if ref := (agentImpl{}).CheckRoute(context.Background(), *m.Route, testBedrockModel); ref != nil {
		t.Errorf("open: CheckRoute = %v", ref)
	}
	if ref := (agentImpl{}).CheckRoute(context.Background(), *bedrockMeta(t, "b").Route, testBedrockModel); ref == nil {
		t.Error("the deployment gate opened the member's bedrock route")
	}
}

// The launch is the bedrock launch: a gateway bearer on the socket, the model pinned everywhere,
// nothing that would put the CLI on another credential.
func TestBedrockDeploymentLaunch(t *testing.T) {
	isolateSlot(t)
	isolateClaudeConfig(t)
	gw := &fakeGateway{}
	withGateway(t, gw)
	t.Setenv(deploymentGateEnv, "1")
	m := deploymentMeta(t, "dep-2")
	plan, err := launchOf(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(gw.issued) != 1 || gw.issued[0].Route.Route != RouteBedrockDeployment {
		t.Fatalf("issued = %+v", gw.issued)
	}
	for _, want := range []string{"AWS_BEARER_TOKEN_BEDROCK=" + testBearer, "CLAUDE_CODE_USE_BEDROCK=1",
		"ANTHROPIC_UNIX_SOCKET=/state/route-gateway.sock", "ANTHROPIC_MODEL=" + testBedrockModel, "CLAUDE_CODE_SUBAGENT_MODEL=" + testBedrockModel} {
		if !slices.Contains(plan.Env, want) {
			t.Errorf("env lacks %s: %v", want, plan.Env)
		}
	}
	for _, u := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		if !strings.Contains(plan.Program, "-u "+u+" ") {
			t.Errorf("%s is not removed: %q", u, plan.Program)
		}
	}
	// A tier alias is refused before the gateway is asked, as on the member route.
	m.Model = "opus"
	if ref := (agentImpl{}).CheckRoute(context.Background(), *m.Route, m.Model); ref == nil || ref.Code != routes.CodeRouteModel {
		t.Errorf("alias: %v", ref)
	}
	// A connection the CP did not bind is refused by the adapter itself.
	if ref := (agentImpl{}).CheckRoute(context.Background(), *sel(RouteBedrockDeployment, "p1@us-east-1"), testBedrockModel); ref == nil {
		t.Error("a profile reference was accepted on the deployment's route")
	}
	if ref := (agentImpl{}).CheckRoute(context.Background(), *sel(RouteBedrockDeployment, ""), testBedrockModel); ref != nil {
		t.Errorf("an empty connection (the CP binds it) was refused: %v", ref)
	}
	refused := &agents.RouteRefusal{Status: http.StatusServiceUnavailable, Code: "route_budget_exhausted", Message: "spend limit"}
	withGateway(t, &fakeGateway{refuse: refused})
	if _, err := launchOf(t, deploymentMeta(t, "dep-3")); !errors.As(err, &refused) {
		t.Errorf("a CP refusal at launch: %v", err)
	}
	if !OwnsCredential(m) || AuthExpiredFor(m) {
		t.Errorf("the deployment route runs on the gateway bearer, not the login")
	}
}
