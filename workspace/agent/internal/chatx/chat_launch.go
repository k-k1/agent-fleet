package chatx

import (
	"errors"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// chatLaunches resolves, per backend, what a chat or one-shot call runs on and the binding of
// its credential (ADR 0138 D2, D5b): the same credential the kind's sessions use with no
// selection, read when the call starts. Each resolver reads files and cached views only, never
// a CLI or a daemon. lcpp is not here: its call resolves the connection itself (EngineToken)
// and records what that answered.
var chatLaunches = map[string]func(model string) routes.Launch{
	session.KindClaude:   func(string) routes.Launch { return claude.DefaultLaunch() },
	session.KindCodex:    func(string) routes.Launch { return codex.Login() },
	session.KindOpencode: opencode.ModelLaunch,
	session.KindAgy:      func(string) routes.Launch { return agy.Login() },
	session.KindCursor:   func(string) routes.Launch { return cursor.Login() },
	session.KindMuse:     func(string) routes.Launch { return muse.Login() },
}

// ErrRoutePolicy marks a utility call the credential policy refuses before it runs (ADR 0138 D12):
// the route its backend would run on is forbidden, cannot be told under a restriction, or the
// policy cannot be confirmed now. Nothing was sent to a model.
var ErrRoutePolicy = errors.New("the credential policy does not allow this call")

// chatLaunchLater are the kinds whose one-shot resolves its model after oneShotRunOn, and with it
// its route (opencode's recommendation, opencodeOneShotModel): they are judged there.
var chatLaunchLater = map[string]bool{session.KindOpencode: true}

// chatObserveBudget bounds chatLaunch (routes.Observe). A var for tests.
var chatObserveBudget = routes.ObserveBudget

// chatLaunch is the route and binding a call on kind's backend runs on, resolved before it runs,
// within chatObserveBudget: a resolver that is slow or hangs leaves the row unresolved and never
// holds the call. "" for a kind with no resolver here.
// chatLaunchChecked is chatLaunch judged by the credential policy, every utility call's check at
// the point it runs (D12: resolve, then refuse a forbidden route). Under a restriction a call
// whose route cannot be told is refused too: it could be on any route. While nothing is
// restricted only the store's Gate can refuse (a start that could record no floor). A refused
// call is the error its caller returns, so a fallback order never moves to a forbidden backend.
func chatLaunchChecked(kind, model string) (route, binding string, err error) {
	route, binding = chatLaunch(kind, model)
	if gerr := routepolicy.Active().Gate(); gerr != nil {
		return route, binding, fmt.Errorf("%w: %v", ErrRoutePolicy, gerr)
	}
	p, restricted := routepolicy.Hiding()
	switch {
	case !restricted:
	case route == "":
		return route, binding, fmt.Errorf("%w: the route %s would run on cannot be told", ErrRoutePolicy, kind)
	case !p.Allows(route):
		return route, binding, fmt.Errorf("%w: %s would run on %s", ErrRoutePolicy, kind, route)
	}
	return route, binding, nil
}

func chatLaunch(kind, model string) (route, binding string) {
	f, ok := chatLaunches[kind]
	if !ok {
		return "", ""
	}
	l, _ := routes.Observe(chatObserveBudget, kind+" chat launch", func() routes.Launch { return f(model) })
	return l.Route, l.Binding
}
