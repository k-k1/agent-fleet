package sessionx

import (
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// launchResolveBudget bounds how long a create, fork or recreate waits for its launch record.
// The record is evidence, not a condition of the launch (ADR 0138 M0 changes no behaviour), so a
// resolver that is slow or hangs (a cold CLI probe, a locked store) costs the record, never the
// launch: past the budget the session is recorded as unresolved and starts as it always did.
var launchResolveBudget = 500 * time.Millisecond

// withinBudget runs f, returning its answer, or ok=false when it has not answered within
// launchResolveBudget. A late answer is dropped.
func withinBudget(what string, f func() routes.Launch) (routes.Launch, bool) {
	ch := make(chan routes.Launch, 1)
	go func() { ch <- f() }()
	t := time.NewTimer(launchResolveBudget)
	defer t.Stop()
	select {
	case l := <-ch:
		return l, true
	case <-t.C:
		log.Printf("launch route: %s did not answer within %v; recorded as unresolved", what, launchResolveBudget)
		return routes.Launch{}, false
	}
}

// launchRouteOf is the launch-time record of a session being created now (ADR 0138 D11): the
// route it runs on and the binding of the credential behind it. An explicit selection is its own
// record (launchOfSelection); with none, the kind resolves its current login mode
// (agents.CredentialResolver). nil for a kind that runs no model (shell, ssm).
//
// Called at create, fork and recreate, and moved by a consented route switch. A resume never
// re-resolves: re-reading today's credential for an existing session could change who pays
// (D11), so a session created before the record existed keeps none.
func launchRouteOf(m session.Meta) *routes.Launch {
	a, ok := RegisteredAgent(NormalizeKind(m.Kind))
	if m.Route != nil {
		return launchOfSelection(a, *m.Route.Normalize())
	}
	if !ok {
		return nil
	}
	if cr, ok := a.(agents.CredentialResolver); ok {
		l, _ := withinBudget(m.Kind+" ResolveLaunch", func() routes.Launch { return cr.ResolveLaunch(m) })
		return &l
	}
	return nil
}

// launchOfSelection is the record of an explicit selection: its route, and the binding the
// kind's agents.LaunchBinder gives it, else the bound connection BindRoute stored. A binder past
// the budget leaves the binding unproven.
func launchOfSelection(a agents.Agent, sel routes.Selection) *routes.Launch {
	l := &routes.Launch{Route: sel.Route, Binding: sel.Connection}
	if lb, ok := a.(agents.LaunchBinder); ok {
		got, ok := withinBudget(sel.Route+" LaunchBinding", func() routes.Launch {
			return routes.Launch{Binding: lb.LaunchBinding(sel)}
		})
		switch {
		case !ok:
			// The stored connection may be a form the binder exists to replace; unproven is
			// honest, a guess at which form is fit to travel is not.
			l.Binding = ""
		case got.Binding != "":
			l.Binding = got.Binding
		}
	}
	return l
}
