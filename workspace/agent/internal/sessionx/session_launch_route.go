package sessionx

import (
	"log"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// launchResolveBudget bounds how long a create, fork or recreate waits for its launch record
// (routes.Observe): past it the session is recorded as unresolved and starts as it always did.
var launchResolveBudget = routes.ObserveBudget

// withinBudget is routes.Observe under launchResolveBudget.
func withinBudget(what string, f func() routes.Launch) (routes.Launch, bool) {
	return routes.Observe(launchResolveBudget, what, f)
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
// kind's agents.LaunchBinder gives it when it handles the route (authoritative, "" included),
// else the bound connection BindRoute stored. A binder past the budget leaves the binding
// unproven: the stored connection may be the very form it exists to replace.
func launchOfSelection(a agents.Agent, sel routes.Selection) *routes.Launch {
	l := &routes.Launch{Route: sel.Route, Binding: sel.Connection}
	lb, ok := a.(agents.LaunchBinder)
	if !ok {
		return l
	}
	type answer struct {
		binding string
		handled bool
	}
	ch := make(chan answer, 1)
	go func() {
		b, h := lb.LaunchBinding(sel)
		ch <- answer{b, h}
	}()
	t := time.NewTimer(launchResolveBudget)
	defer t.Stop()
	select {
	case got := <-ch:
		if got.handled {
			l.Binding = got.binding
		}
	case <-t.C:
		log.Printf("launch route: %s LaunchBinding did not answer within %v; binding recorded as unproven", sel.Route, launchResolveBudget)
		l.Binding = ""
	}
	return l
}
