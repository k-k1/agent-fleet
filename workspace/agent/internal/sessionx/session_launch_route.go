package sessionx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// launchRouteOf is the launch-time record of a session being created now (ADR 0138 D11): the
// route it runs on and the binding of the credential behind it. An explicit selection is its own
// record, in the bound form BindRoute stored (a selection that names no connection takes the
// binding agents.LaunchBinder gives it); with none, the kind resolves its current login mode
// (agents.CredentialResolver). nil for a kind that runs no model (shell, ssm).
//
// Called at create, fork and recreate only. A resume never re-resolves: re-reading today's
// credential for an existing session could change who pays (D11), so a session created before
// the record existed keeps none.
func launchRouteOf(m session.Meta) *routes.Launch {
	a, ok := RegisteredAgent(NormalizeKind(m.Kind))
	if m.Route != nil {
		sel := m.Route.Normalize()
		l := &routes.Launch{Route: sel.Route, Binding: sel.Connection}
		if lb, isBinder := a.(agents.LaunchBinder); ok && isBinder && l.Binding == "" {
			l.Binding = lb.LaunchBinding(*sel)
		}
		return l
	}
	if !ok {
		return nil
	}
	if cr, ok := a.(agents.CredentialResolver); ok {
		l := cr.ResolveLaunch(m)
		return &l
	}
	return nil
}
