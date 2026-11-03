package sessionx

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
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
	// Stored in the form the adapter binds to the connection instance it was just checked on.
	if c.req.Route != nil {
		if a, _ := RegisteredAgent(NormalizeKind(c.req.Kind)); a != nil {
			if b, ok := a.(agents.RouteBinder); ok {
				bound, ref := b.BindRoute(*c.req.Route.Normalize())
				if ref != nil {
					httpx.WriteErr(c.w, ref.Status, ref.Code, ref.Message)
					return false
				}
				c.req.Route = &bound
			}
		}
	}
	return true
}

// startRefusalOf is startRefusal for a launch of a stored session: the kind's own refusal, then
// the session's stored route selection checked again (static list and live checks). A selection
// is persisted, so an adapter that has since stopped declaring the route, an Agent rolled back
// to a version without it, or a connection that expired must stop the start with the reason,
// never launch the session on its default route. A nil selection adds nothing. Used by every
// start that relaunches a stored session: the start endpoints, a Managed input that resumes
// the runtime, and an execution-method switch (before the old runtime is stopped).
func startRefusalOf(m session.Meta) *SpawnRefusal {
	if ref := startRefusal(m.Kind); ref != nil {
		return ref
	}
	if ref := checkRouteLive(context.Background(), m.Kind, m.Route, m.Model); ref != nil {
		return &SpawnRefusal{Status: ref.Status, Code: ref.Code, Message: ref.Message}
	}
	return nil
}

// SweepRouteGuards is one pass over every running session with an explicit route (the
// reconciler's tick). A route's credential is fixed at process start, but the CLI re-reads its
// settings while it runs; when the kind's RouteWatcher says the route no longer holds, the session
// is stopped (the same halt as the stop button, so it stays resumable) and the member is
// told why. Starting it again is refused while the cause remains (startRefusalOf). Fail closed:
// a session that could be spending on another account is not left running.
func SweepRouteGuards(now time.Time) {
	for _, m := range session.ListMetas() {
		if m.Route == nil || m.Archived || m.StoppedAt != "" || !session.ValidName(m.Name) {
			continue
		}
		a, _ := RegisteredAgent(NormalizeKind(m.Kind))
		w, ok := a.(agents.RouteWatcher)
		if !ok || !SessionAlive(m) {
			continue
		}
		ref := w.RouteHeld(m)
		if ref == nil {
			continue
		}
		halted, err := haltSessionMeta(m)
		if err != nil {
			log.Printf("route-guard: %s: halt failed, retrying next tick: %v", m.Name, err)
			continue
		}
		log.Printf("route-guard: stopped %s at %s: %s", m.Name, now.Format(time.RFC3339), ref.Message)
		ev := notice.New("route-stopped", halted.Name, halted.Kind, session.Display(halted))
		ev.Payload["route"] = m.Route.Route
		ev.Payload["reason"] = ref.Message
		_ = notice.Put(ev)
	}
}
