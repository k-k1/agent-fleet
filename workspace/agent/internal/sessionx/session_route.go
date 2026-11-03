package sessionx

import (
	"context"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

// Route selection (ADR 0128 decisions 3, 4, 12, 13). A session may carry an explicit selection
// (session.Meta.Route). Nil means the kind's own resolution rules, unchanged, and no code here
// runs for it. A non-nil selection is refused, with a reason, wherever it is not provably valid:
// there is no path on which it is dropped or replaced by the default route.

// routesOf is the allow-list kind declares, by exact registry key; nil for a kind with no
// RouteAdapter, which therefore accepts no explicit selection.
func routesOf(kind string) []string {
	a, _ := RegisteredAgent(NormalizeKind(kind))
	if ra, ok := a.(agents.RouteAdapter); ok {
		return ra.Routes()
	}
	return nil
}

// checkRouteStatic is the part of the validation that needs no state. The Control Plane has
// already run it over its snapshot; it runs again here because the snapshot may be older than
// this Agent.
func checkRouteStatic(kind string, sel *routes.Selection) *agents.RouteRefusal {
	code, err := routes.CheckStatic(NormalizeKind(kind), sel, routesOf(kind))
	if err == nil {
		return nil
	}
	return &agents.RouteRefusal{Status: http.StatusBadRequest, Code: code, Message: err.Error()}
}

// checkRouteLive is what needs live state: the model against the route, whether the caller may
// use the connection, and whether it is available. It runs the static list first, then the
// adapter's RouteChecker. An adapter without a checker has no live condition of its own.
func checkRouteLive(ctx context.Context, kind string, sel *routes.Selection, model string) *agents.RouteRefusal {
	if sel == nil {
		return nil
	}
	if ref := checkRouteStatic(kind, sel); ref != nil {
		return ref
	}
	a, _ := RegisteredAgent(NormalizeKind(kind))
	if rc, ok := a.(agents.RouteChecker); ok {
		return rc.CheckRoute(ctx, *sel.Normalize(), model)
	}
	return nil
}

// validateRoute is stage 1's second half: the static selection check, ahead of the idempotency
// claim for the same reason the kind check is, since a refused create must leave no ledger entry.
func (c *createRun) validateRoute() bool {
	if ref := checkRouteStatic(c.req.Kind, c.req.Route); ref != nil {
		httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
		return false
	}
	return true
}

// checkRoute is stage 5's second half: the live checks, once the model is resolved against the
// kind's catalogue and before any side effect.
func (c *createRun) checkRoute() bool {
	if ref := checkRouteLive(c.r.Context(), c.req.Kind, c.req.Route, c.req.Model); ref != nil {
		httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
		return false
	}
	return true
}
