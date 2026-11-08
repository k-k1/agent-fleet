package sessionx

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// ocGateway stands in for the route gateway: every selection is accepted and bound.
type ocGateway struct{}

func (ocGateway) Issue(session.Meta) (agents.RouteGrant, *agents.RouteRefusal) {
	return agents.RouteGrant{}, &agents.RouteRefusal{Status: 500, Code: "unused", Message: "unused"}
}
func (ocGateway) IssueLoopback(session.Meta) (agents.RouteGrant, *agents.RouteRefusal) {
	return agents.RouteGrant{}, &agents.RouteRefusal{Status: 500, Code: "unused", Message: "unused"}
}
func (ocGateway) Bind(sel agents.RouteSelection) (agents.RouteSelection, *agents.RouteRefusal) {
	sel.Connection += "#00112233aabbccdd"
	return sel, nil
}
func (ocGateway) CheckRoute(context.Context, agents.RouteSelection, string) *agents.RouteRefusal {
	return nil
}

func openOpencodeBedrock(t *testing.T) {
	t.Helper()
	t.Setenv("AF_OPENCODE_BEDROCK_ROUTE", "1")
	prev := agents.CurrentRouteGateway()
	agents.SetRouteGateway(ocGateway{})
	t.Cleanup(func() { agents.SetRouteGateway(prev) })
}

// opencode's bedrock row is Terminal only (a Managed session shares one serve that sits in no
// session's tree): a Managed create naming it is refused before anything is made, and its model
// is checked by the route, not by the kind's catalogue, which lists no Bedrock model.
func TestOpencodeBedrockCreateIsTerminalOnlyAndOwnsItsModel(t *testing.T) {
	p := newCreateProbe(t)
	openOpencodeBedrock(t)
	opencodeModelsFn = func() []string { return []string{"opencode/big-pickle"} }
	var launched []session.Meta
	launchTmuxFn = func(m session.Meta, _ bool) error { launched = append(launched, m); return nil }
	route := map[string]any{"route": "bedrock", "connection": "prof1@eu-west-1"}
	const model = "amazon-bedrock/anthropic.claude-sonnet-4-6"

	before := p.sideEffects()
	rec := p.post(map[string]any{"kind": "opencode", "dir": p.repo, "driver": "managed", "model": model, "route": route}, "")
	if rec.Code != http.StatusBadRequest || routeCode(t, rec.Body.String()) != routes.CodeRouteUnsupported {
		t.Fatalf("managed: %d %s, want 400 %s", rec.Code, rec.Body.String(), routes.CodeRouteUnsupported)
	}
	if d := before.diff(p.sideEffects()); d != "" {
		t.Fatalf("a refused managed create left side effects: %s", d)
	}
	rec = p.post(map[string]any{"kind": "opencode", "dir": p.repo, "model": "opencode/big-pickle", "route": route}, "")
	if rec.Code != http.StatusBadRequest || routeCode(t, rec.Body.String()) != routes.CodeRouteModel {
		t.Fatalf("a model off the route: %d %s", rec.Code, rec.Body.String())
	}
	rec = p.post(map[string]any{"kind": "opencode", "dir": p.repo, "model": model, "route": route}, "")
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("terminal: %d %s", rec.Code, rec.Body.String())
	}
	if len(launched) != 1 || launched[0].Model != model || launched[0].Route == nil ||
		!strings.HasSuffix(launched[0].Route.Connection, "#00112233aabbccdd") || launched[0].DriverKind() != session.DriverTUI {
		t.Fatalf("launched %+v", launched)
	}
	// Without the route, the same model is still checked against the catalogue (unchanged).
	rec = p.post(map[string]any{"kind": "opencode", "dir": p.repo, "model": model}, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an amazon-bedrock id with no route skipped the catalogue: %d %s", rec.Code, rec.Body.String())
	}
}

// A stored bedrock selection refuses every start under Managed and the switch to it, before the
// running runtime is touched.
func TestOpencodeBedrockRefusesManagedOnStartAndSwitch(t *testing.T) {
	isolateAgentState(t)
	fakeTmux(t)
	openOpencodeBedrock(t)
	sel := routes.Selection{Route: "bedrock", Connection: "prof1@eu-west-1#00112233aabbccdd"}
	m := session.Meta{Name: "oc_bedrock", Dir: t.TempDir(), Kind: session.KindOpencode,
		Model: "amazon-bedrock/anthropic.claude-sonnet-4-6", Route: &sel, CreatedAt: "2026-11-08T10:00:00Z"}
	session.WriteMeta(m)
	if rec := postDriver(t, m.Name, `{"driver":"managed"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), routes.CodeRouteUnsupported) {
		t.Fatalf("switch to managed: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := session.ReadMeta(m.Name); got.DriverKind() != session.DriverTUI {
		t.Fatalf("the refused switch changed the driver to %q", got.Driver)
	}
	m.Driver = session.DriverManaged
	if ref := startRefusalOf(m); ref == nil || ref.Code != routes.CodeRouteUnsupported {
		t.Fatalf("a managed start of a bedrock session = %+v", ref)
	}
	m.Driver = ""
	if ref := startRefusalOf(m); ref != nil {
		t.Fatalf("a terminal start was refused: %+v", ref)
	}
}
