package chatx

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// useUtilityPolicy makes the utilities judge by a store holding p, confirmed now.
func useUtilityPolicy(t *testing.T, p routepolicy.Policy) *routepolicy.Store {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	// The listeners Accept started (the in-flight cancel among them) finish before the test goes
	// on, so they act on what was running before, never on the call the test starts next.
	routepolicy.WaitListeners()
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() {
		routepolicy.WaitListeners()
		routepolicy.Active = old
	})
	return s
}

// launchesOn makes every chat backend resolve to the route the map names for its kind ("" for
// one that cannot tell).
func launchesOn(t *testing.T, routeOf map[string]string) {
	t.Helper()
	saved := map[string]func(string) routes.Launch{}
	for k, f := range chatLaunches {
		saved[k] = f
		r := routeOf[k]
		chatLaunches[k] = func(string) routes.Launch { return routes.Launch{Route: r, Binding: r + "@x"} }
	}
	t.Cleanup(func() {
		for k, f := range saved {
			chatLaunches[k] = f
		}
	})
}

// allAvailable makes every backend's CLI read as signed in.
func allAvailable(t *testing.T) {
	t.Helper()
	prev := headlessAvailCheck
	headlessAvailCheck = func(string) bool { return true }
	t.Cleanup(func() { headlessAvailCheck = prev })
	for k := range chatLaunches {
		t.Cleanup(ClearHeadlessAvailableForTest(k))
	}
}

var apiOnly = routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI, routes.RouteOpenAIAPI}}

// A utility call is judged where it runs: nothing is restricted, the route is allowed, it
// passes; a forbidden route, or one that cannot be told under a restriction, is refused; and
// while the policy cannot be confirmed every call waits.
func TestChatLaunchChecked(t *testing.T) {
	launchesOn(t, map[string]string{session.KindClaude: routes.RouteAnthropicSubscription, session.KindCodex: routes.RouteOpenAIAPI})
	useUtilityPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if _, _, err := chatLaunchChecked(session.KindClaude, ""); err != nil {
		t.Errorf("an open policy refused: %v", err)
	}
	useUtilityPolicy(t, apiOnly)
	if _, _, err := chatLaunchChecked(session.KindCodex, ""); err != nil {
		t.Errorf("an allowed route was refused: %v", err)
	}
	if _, _, err := chatLaunchChecked(session.KindClaude, ""); !errors.Is(err, ErrRoutePolicy) {
		t.Errorf("a forbidden route: %v", err)
	}
	if _, _, err := chatLaunchChecked(session.KindOpencode, ""); !errors.Is(err, ErrRoutePolicy) || !strings.Contains(err.Error(), "cannot be told") {
		t.Errorf("a route that cannot be told: %v", err)
	}
	s := routepolicy.Open(filepath.Join(t.TempDir(), "stale.json"), "")
	if _, err := s.Seed(apiOnly, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	routepolicy.Active = func() *routepolicy.Store { return s }
	if _, _, err := chatLaunchChecked(session.KindCodex, ""); !errors.Is(err, ErrRoutePolicy) {
		t.Errorf("an unconfirmed restricting policy let a call through: %v", err)
	}
}

// Every chat backend refuses a turn on a forbidden route before anything runs.
func TestEveryChatBackendRefusesAForbiddenRoute(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	launchesOn(t, map[string]string{})
	useUtilityPolicy(t, apiOnly)
	for kind, prov := range chatBackends {
		if _, ok := chatLaunches[kind]; !ok {
			continue // a backend with no resolver here (lcpp) is judged on its own connection
		}
		_, err := prov.Send(context.Background(), &ChatConversation{Agent: kind}, "hi")
		if kind == session.KindMuse && err != nil {
			continue // muse resolves its model first, and refuses here (no catalog), sending nothing
		}
		if !errors.Is(err, ErrRoutePolicy) {
			t.Errorf("%s: %v, want a policy refusal before the CLI runs", kind, err)
		}
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("refused turns left ledger rows: %+v", rows)
	}
}

// Selection moves only to an allowed backend: a conversation pinned to a forbidden one is routed
// to an allowed one, the preferred one-shot backend skips a forbidden one, and with none allowed
// the call still reaches a backend, which refuses it with the reason.
func TestSelectionMovesOnlyToAllowedBackends(t *testing.T) {
	allAvailable(t)
	launchesOn(t, map[string]string{session.KindClaude: routes.RouteAnthropicSubscription, session.KindCodex: routes.RouteOpenAIAPI})
	useUtilityPolicy(t, apiOnly)
	if _, kind := chatRoute(&ChatConversation{Agent: session.KindClaude}); kind != session.KindCodex {
		t.Errorf("a turn pinned to claude on its subscription was routed to %s, want codex", kind)
	}
	if k := preferredFrom([]string{session.KindClaude, session.KindCodex}); k != session.KindCodex {
		t.Errorf("preferred = %s, want codex", k)
	}
	useUtilityPolicy(t, routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{}})
	prov, _ := chatRoute(&ChatConversation{Agent: session.KindClaude})
	if _, err := prov.Send(context.Background(), &ChatConversation{Agent: session.KindClaude}, "hi"); !errors.Is(err, ErrRoutePolicy) {
		t.Errorf("with nothing allowed the turn was not refused with the reason: %v", err)
	}
}

// A one-shot is judged before its runner starts, and a deny-all one-shot never chooses a backend
// the policy would refuse: with claude (the only deny-all backend) on a forbidden route, none is
// left.
func TestOneShotsAreJudgedBeforeTheyRun(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	allAvailable(t)
	launchesOn(t, map[string]string{session.KindClaude: routes.RouteAnthropicSubscription})
	useUtilityPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if k, err := resolveDenyAllKind("title", OneShotShort); err != nil || k != session.KindClaude {
		t.Fatalf("control: unrestricted deny-all = %q %v", k, err)
	}
	useUtilityPolicy(t, apiOnly)
	if k, err := resolveDenyAllKind("title", OneShotShort); !errors.Is(err, ErrDenyAllUnavailable) {
		t.Errorf("deny-all chose %q (%v) on a forbidden route", k, err)
	}
	if _, _, _, err := oneShotRunOn(context.Background(), session.KindClaude, "title", OneShotShort, "", "hi", ""); !errors.Is(err, ErrRoutePolicy) {
		t.Errorf("a one-shot on a forbidden route ran: %v", err)
	}
	// opencode resolves its model, and with it its route, inside its runner: judged there.
	if _, _, _, err := oneShotRunOn(context.Background(), session.KindOpencode, "title", OneShotShort, "", "hi", ""); !errors.Is(err, ErrRoutePolicy) {
		t.Errorf("an opencode one-shot whose route cannot be told ran: %v", err)
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("refused one-shots left ledger rows: %+v", rows)
	}
}

// A kind the member switched off (ui-prefs agentEnabled) is refused at every call with
// agent_disabled, whatever the policy: a conversation pinned to it moves to another backend, the
// preferred one-shot skips it, a feature pin and an explicit one-shot on it are refused before
// they run, and with the switch on the same calls go through.
func TestSwitchedOffKindIsNeverCalled(t *testing.T) {
	t.Setenv("AF_USAGE_DIR", t.TempDir())
	allAvailable(t)
	launchesOn(t, map[string]string{session.KindClaude: routes.RouteAnthropicSubscription, session.KindCodex: routes.RouteOpenAIAPI})
	useUtilityPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})

	writePrefs(t, `{"agentEnabled":{"claude":true}}`)
	if _, kind := chatRoute(&ChatConversation{Agent: session.KindClaude}); kind != session.KindClaude {
		t.Fatalf("control: switch on, the pinned claude turn went to %s", kind)
	}
	if _, _, err := chatLaunchChecked(session.KindClaude, ""); err != nil {
		t.Fatalf("control: switch on, refused: %v", err)
	}

	writePrefs(t, `{"agentEnabled":{"claude":false},"aiFeatureAgents":{"title":"claude"}}`)
	_, _, err := chatLaunchChecked(session.KindClaude, "")
	if !errors.Is(err, ErrSwitchedOff) || !errors.Is(err, ErrRoutePolicy) {
		t.Fatalf("a call on a switched-off kind: %v", err)
	}
	if status, code := policyErrCode(err); status != 403 || code != "agent_disabled" {
		t.Errorf("its answer is %d %q, want 403 agent_disabled", status, code)
	}
	if _, kind := chatRoute(&ChatConversation{Agent: session.KindClaude}); kind == session.KindClaude {
		t.Error("a stored conversation pinned to the switched-off claude was routed to it")
	}
	if k := preferredFrom([]string{session.KindClaude, session.KindCodex}); k != session.KindCodex {
		t.Errorf("preferred = %s, want codex past the switched-off claude", k)
	}
	if _, _, _, err := oneShotRunOn(context.Background(), session.KindClaude, "title", OneShotShort, "", "hi", ""); !errors.Is(err, ErrSwitchedOff) {
		t.Errorf("a one-shot on the switched-off claude ran: %v", err)
	}
	prov := chatBackends[session.KindClaude]
	if _, err := prov.Send(context.Background(), &ChatConversation{Agent: session.KindClaude}, "hi"); !errors.Is(err, ErrSwitchedOff) {
		t.Errorf("a send straight to the switched-off backend: %v", err)
	}
	if rows := usagex.ReadRows(); len(rows) != 0 {
		t.Errorf("refused calls left ledger rows: %+v", rows)
	}
}
