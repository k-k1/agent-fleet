package chatx

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/agy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/cursor"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
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

// ErrSwitchedOff is the refusal of a call on a kind the member has switched off (ui-prefs
// agentEnabled, and lcppEnabled for lcpp). It is wrapped in ErrRoutePolicy, so every fallback that
// moves past a policy refusal moves past it too.
var ErrSwitchedOff = errors.New("the member has switched this agent off")

// kindSwitchedOff reports the member's own off switch for kind.
func kindSwitchedOff(kind string) bool {
	return !uiprefs.AgentEnabled(kind) || kind == lcppKind && !uiprefs.LcppEnabled()
}

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
	return route, binding, routeChecked(kind, route)
}

// opencodePinRun is opencode.PinRun; a seam for tests.
var opencodePinRun = opencode.PinRun

// opencodeLaunchChecked is chatLaunchChecked for an opencode run, and the pin the run is isolated
// with: the route, the binding and the pin come from one reading (opencode.PinRun), so what the
// call is judged and recorded on is what it is sent to. A reading that does not answer within
// chatObserveBudget leaves the route unresolved and the pin refusing, like chatLaunch.
func opencodeLaunchChecked(model string) (pin opencode.RunPin, route, binding string, err error) {
	ch := make(chan opencode.RunPin, 1)
	go func() { ch <- opencodePinRun(model) }()
	t := time.NewTimer(chatObserveBudget)
	defer t.Stop()
	select {
	case pin = <-ch:
	case <-t.C:
		log.Printf("route observation: opencode chat launch did not answer within %v; recorded as unresolved", chatObserveBudget)
	}
	route, binding = pin.Launch.Route, pin.Launch.Binding
	return pin, route, binding, routeChecked(session.KindOpencode, route)
}

// routeChecked is the credential policy's answer for a call on kind about to run on route: nil
// while nothing is restricted and the policy can be confirmed, or the route is allowed. A refusal
// wraps ErrRoutePolicy, and the store's Gate error when that is the reason.
func routeChecked(kind, route string) error {
	// The member's own switch for the kind (ADR 0138 D7 layer 4) refuses every call on it, a stored
	// conversation, a feature pin and a fallback alike; selection skips it through chatKindAllowed.
	if kindSwitchedOff(kind) {
		return fmt.Errorf("%w: %w: %s", ErrRoutePolicy, ErrSwitchedOff, kind)
	}
	if gerr := routepolicy.Active().Gate(); gerr != nil {
		return fmt.Errorf("%w: %w", ErrRoutePolicy, gerr)
	}
	p, restricted := routepolicy.Hiding()
	switch {
	case !restricted:
	case route == "":
		return fmt.Errorf("%w: the route %s would run on cannot be told", ErrRoutePolicy, kind)
	case !p.Allows(route):
		return fmt.Errorf("%w: %s would run on %s", ErrRoutePolicy, kind, route)
	}
	return nil
}

// trackCall keeps a running call cancellable by the credential policy (routepolicy.Track): when a
// tighter policy forbids call's route, its context is cancelled, which stops its CLI or request.
// done, deferred with the call's error, finishes it in an order no cancel can slip into: the
// tracking ends first, so the outcome is settled; a cancel then becomes the ErrRoutePolicy refusal
// its caller reports, whatever the stopped call returned, and leaves no ledger row, like a call
// refused before it ran; any other outcome is recorded (started is its start). A nil call records
// nothing, for a caller that records its own.
func trackCall(ctx context.Context, call *usagex.Call, started time.Time) (context.Context, func(err *error)) {
	route := ""
	if call != nil {
		route = call.Route
	}
	return trackRoute(ctx, route, call, started)
}

func trackRoute(ctx context.Context, route string, call *usagex.Call, started time.Time) (context.Context, func(err *error)) {
	ctx, c := routepolicy.Track(ctx, route)
	return ctx, func(err *error) {
		c.Done()
		if routepolicy.Cancelled(ctx) {
			*err = fmt.Errorf("%w: %w", ErrRoutePolicy, routepolicy.ErrCancelled)
			return
		}
		if call != nil {
			usagex.RecordCall(ctx, call, started)
		}
	}
}

// writeChatErr answers a failed chat call: the credential policy's refusal or cancel with its
// code (policy_unconfirmed while it cannot be confirmed, route_forbidden otherwise), any other
// failure as the provider's.
func writeChatErr(w http.ResponseWriter, err error) {
	if status, code := policyErrCode(err); code != "" {
		httpx.WriteErr(w, status, code, err.Error())
		return
	}
	httpx.WriteErr(w, http.StatusBadGateway, "provider", err.Error())
}

// streamErrEvent is a streamed chat's error event: the message, and the policy's code for a
// credential policy refusal or cancel.
func streamErrEvent(err error) map[string]any {
	ev := map[string]any{"error": err.Error()}
	if _, code := policyErrCode(err); code != "" {
		ev["code"] = code
	}
	return ev
}

// policyErrCode is the status and code of a credential policy refusal or cancel, "" for any other
// error.
func policyErrCode(err error) (int, string) {
	switch {
	case errors.Is(err, ErrSwitchedOff):
		return http.StatusForbidden, "agent_disabled"
	case errors.Is(err, routepolicy.ErrUnconfirmed), errors.Is(err, routepolicy.ErrUnrecorded):
		return http.StatusServiceUnavailable, routes.CodePolicyUnconfirmed
	case errors.Is(err, ErrRoutePolicy):
		return http.StatusForbidden, routes.CodePolicyForbidden
	}
	return 0, ""
}

func chatLaunch(kind, model string) (route, binding string) {
	f, ok := chatLaunches[kind]
	if !ok {
		return "", ""
	}
	l, _ := routes.Observe(chatObserveBudget, kind+" chat launch", func() routes.Launch { return f(model) })
	return l.Route, l.Binding
}
