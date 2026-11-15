package codex

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var (
	unrestricted     = routepolicy.Policy{Revision: 1, Allowed: []string{}}
	subscriptionOnly = routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteChatGPTSubscription}}
	apiKeyOnly       = routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteOpenAIAPI}}
)

func usePolicy(t *testing.T, p routepolicy.Policy) {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	// The policy listeners (Accept starts them), the security restarts they or Ensure start, and
	// the reconciles those start read Active in the background.
	settle := func() {
		routepolicy.WaitListeners()
		policyWork.Wait()
	}
	settle()
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() {
		settle()
		routepolicy.Active = old
	})
}

func stubDaemonLogin(t *testing.T, route string) {
	t.Helper()
	prev := daemonLogin
	daemonLogin = func() string { return route }
	t.Cleanup(func() {
		routepolicy.WaitListeners()
		policyWork.Wait()
		daemonLogin = prev
	})
}

// busyHandle registers one managed handle with a turn running (no turn id yet, so interrupting
// it sends nothing over a connection).
func busyHandle(t *testing.T) *threadHandle {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	const name = "policy-busy"
	h := &threadHandle{name: name, alive: true, running: true, state: agents.TurnRunning, events: make(chan agents.Event, 16)}
	handlesMu.Lock()
	handles[name] = h
	handlesMu.Unlock()
	t.Cleanup(func() {
		handlesMu.Lock()
		delete(handles, name)
		handlesMu.Unlock()
	})
	return h
}

// interrupted reports whether the handle's turn was interrupted: runtimeLost overwrites the
// state afterwards, so the event the interrupt emitted is what tells.
func interrupted(h *threadHandle) bool {
	for {
		select {
		case e := <-h.events:
			if e.Kind == "turn_state" && e.TurnState == agents.TurnInterrupting {
				return true
			}
		default:
			return false
		}
	}
}

func stateOf(h *threadHandle) agents.TurnState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// ownedDaemon is a process standing in for a daemon this supervisor started.
func ownedDaemon(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// With no restriction nothing in the policy's path acts: an adopted daemon is used as before, a
// forbidden-looking login is not looked at, and a policy change interrupts nothing.
func TestUnrestrictedAppServerIsNeverTouched(t *testing.T) {
	h := busyHandle(t)
	usePolicy(t, unrestricted)
	stubDaemonLogin(t, "")
	s := &Supervisor{up: true} // adopted: no cmd, no credRoute
	if err := s.policyAdmitLocked("ws://127.0.0.1:1"); err != nil {
		t.Errorf("an unrestricted Ensure was refused: %v", err)
	}
	s.onPolicyChange(unrestricted)
	if st := stateOf(h); st != agents.TurnRunning || !s.up {
		t.Errorf("an unrestricted policy change acted: turn %v, up %v", st, s.up)
	}
}

// Under a restriction a daemon this Agent did not start is never used, while with no restriction
// it is adopted as before.
func TestAdoptedAppServerIsNotUsedUnderARestriction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	addr := "ws://" + strings.TrimPrefix(srv.URL, "http://")
	stubDaemonLogin(t, routes.RouteChatGPTSubscription)
	usePolicy(t, unrestricted)
	if err := (&Supervisor{}).policyAdmitLocked(addr); err != nil {
		t.Fatalf("control: an unrestricted Ensure would not adopt the running daemon: %v", err)
	}
	usePolicy(t, subscriptionOnly)
	t.Setenv(appServerAddrEnv, addr)
	if _, _, err := (&Supervisor{}).Ensure(); !errors.Is(err, ErrAppServerUnverified) {
		t.Errorf("a restricted Ensure adopted a running daemon: %v", err)
	}
	if err := (&Supervisor{up: true}).policyAdmitLocked(addr); !errors.Is(err, ErrAppServerUnverified) {
		t.Errorf("a restricted Ensure reused an adopted daemon: %v", err)
	}
}

// Under a restriction no daemon is started on a login the policy forbids, or on one whose route
// cannot be told; an allowed login passes.
func TestAppServerIsNotStartedOnAForbiddenLogin(t *testing.T) {
	stubLoggedIn(t, true)
	usePolicy(t, subscriptionOnly)
	for _, route := range []string{routes.RouteOpenAIAPI, ""} {
		stubDaemonLogin(t, route)
		if _, _, err := gateSupervisor(t).Ensure(); !errors.Is(err, ErrAppServerUnverified) {
			t.Errorf("login %q: Ensure = %v, want ErrAppServerUnverified", route, err)
		}
	}
	stubDaemonLogin(t, routes.RouteChatGPTSubscription)
	if err := gateSupervisor(t).policyAdmitLocked("ws://127.0.0.1:1"); err != nil {
		t.Errorf("an allowed login was refused: %v", err)
	}
}

// A policy that forbids the route the daemon runs on stops it at once: the running turn is
// interrupted without waiting for the drain, and the process is stopped. A policy that still
// allows the route forces nothing.
func TestTighterPolicyStopsTheAppServerWithoutADrain(t *testing.T) {
	h := busyHandle(t)
	t.Setenv("AF_CODEX_DRAIN_TIMEOUT_SEC", "60")
	stubDaemonLogin(t, routes.RouteOpenAIAPI)
	s := gateSupervisor(t)
	cmd := ownedDaemon(t)
	s.up, s.cmd, s.credRoute = true, cmd, routes.RouteOpenAIAPI

	usePolicy(t, apiKeyOnly)
	s.onPolicyChange(apiKeyOnly)
	if st := stateOf(h); st != agents.TurnRunning || !s.up {
		t.Fatalf("a policy that still allows the route acted: turn %v, up %v", st, s.up)
	}

	usePolicy(t, subscriptionOnly)
	start := time.Now()
	s.onPolicyChange(subscriptionOnly)
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("the security restart took %v: it waited for a drain", d)
	}
	if !interrupted(h) {
		t.Error("the running turn was not interrupted")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon process was not stopped")
	}
	s.mu.Lock()
	up, owned := s.up, s.cmd != nil
	s.mu.Unlock()
	if up || owned {
		t.Errorf("after the security restart: up %v, cmd kept %v", up, owned)
	}
	if _, _, err := s.Ensure(); !errors.Is(err, ErrAppServerUnverified) {
		t.Errorf("a daemon would be started again on the forbidden login: %v", err)
	}
}

// The listener stops the daemon when either the route it was started on or codex's current login
// is forbidden, and only then.
func TestPolicyChangeJudgesBothTheDaemonAndTheLogin(t *testing.T) {
	for _, c := range []struct {
		name, started, login string
		adopted, stop        bool
	}{
		{"both allowed", routes.RouteChatGPTSubscription, routes.RouteChatGPTSubscription, false, false},
		{"started on a forbidden route", routes.RouteOpenAIAPI, routes.RouteChatGPTSubscription, false, true},
		{"signed in on a forbidden route since", routes.RouteChatGPTSubscription, routes.RouteOpenAIAPI, false, true},
		{"adopted", routes.RouteChatGPTSubscription, routes.RouteChatGPTSubscription, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubDaemonLogin(t, c.login)
			usePolicy(t, subscriptionOnly)
			s := gateSupervisor(t)
			s.up, s.credRoute = true, c.started
			if !c.adopted {
				s.cmd = ownedDaemon(t)
			}
			s.onPolicyChange(subscriptionOnly)
			s.mu.Lock()
			stopped := !s.up && s.cmd == nil
			s.mu.Unlock()
			if stopped != c.stop {
				t.Errorf("stopped = %v, want %v", stopped, c.stop)
			}
		})
	}
}

// A daemon started on a route the policy no longer allows is not used even when codex's current
// login is allowed: Ensure refuses it and stops it.
func TestStaleRouteIsReplacedUnderARestriction(t *testing.T) {
	stubDaemonLogin(t, routes.RouteChatGPTSubscription)
	usePolicy(t, subscriptionOnly)
	s := gateSupervisor(t)
	cmd := ownedDaemon(t)
	s.up, s.cmd, s.credRoute = true, cmd, routes.RouteOpenAIAPI
	s.mu.Lock()
	err := s.policyAdmitLocked(s.Addr())
	s.mu.Unlock()
	if !errors.Is(err, ErrAppServerUnverified) {
		t.Fatalf("a daemon on a forbidden route was used: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stale daemon was not stopped")
	}
}
