package opencode

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// The approved custom endpoint is served to Managed sessions on the shared serve only: a Terminal
// launch, a private route server and a foreign config root are refused for that provider, and
// nothing else changes.
func TestCustomEndpointIsManagedOnly(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	home := filepath.Dir(paths.OpencodeConfigDir())
	custom := session.Meta{Model: "mine/m"}
	other := session.Meta{Model: "anthropic/m"}
	at := func(xdg string) policyView { return readViewAt(xdg) }

	for _, xdg := range []string{"", home, home + "/"} {
		if err := customManagedOnlyErrFrom(custom, true, at(xdg)); err != nil {
			t.Errorf("managed, config root %q: %v", xdg, err)
		}
	}
	if err := customManagedOnlyErrFrom(custom, false, at("")); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("terminal / private server: %v", err)
	}
	if err := customManagedOnlyErrFrom(custom, true, at("/elsewhere")); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("foreign config root: %v", err)
	}
	if err := customManagedOnlyErrFrom(other, false, at("/elsewhere")); err != nil {
		t.Errorf("another provider is not affected: %v", err)
	}
	if err := customManagedOnlyErrFrom(session.Meta{}, false, at("")); err != nil {
		t.Errorf("no model named: %v", err)
	}
}

// A foreign config root withholds the custom pin from the shared serve whatever model the launch
// names, so an auxiliary model cannot reach an endpoint whose URL files outside the check could move.
func TestForeignConfigRootWithholdsTheCustomPin(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	if v := readViewAt(""); v.custom == nil {
		t.Fatal("pinned root: no custom connection")
	}
	v := readViewAt("/elsewhere")
	if v.custom != nil || v.foreign == nil {
		t.Fatalf("foreign root kept the pin: custom=%v foreign=%v", v.custom, v.foreign)
	}
	env, ok := servePolicyEnvFrom(nil, v)
	if !ok {
		t.Fatal("not restricted")
	}
	enabled, cfg := runPin(t, env)
	if len(enabled) != 0 {
		t.Errorf("enabled_providers = %v", enabled)
	}
	if _, has := cfg["provider"]; has {
		t.Errorf("provider block pinned: %v", cfg["provider"])
	}
}

// The refusal is reached from each entry point, before any process starts: BuildLaunch (Terminal),
// resume of a Managed session with an explicit route, and the LaunchGuard an execution-method
// switch asks first. A Managed session without a route, and another provider, pass the guard.
func TestCustomManagedOnlyAtTheEntryPoints(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	dir := t.TempDir()
	terminal := session.Meta{Name: "t1", Dir: dir, Kind: session.KindOpencode, Model: "mine/m"}

	if _, err := (agentImpl{}).BuildLaunch(terminal, agents.LaunchOpts{}); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("BuildLaunch: %v", err)
	}
	routed := terminal
	routed.Driver = session.DriverManaged
	routed.Route = &routes.Selection{Route: routes.RouteBedrock}
	if _, err := (managedDriver{}).Resume(routed); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("resume with an explicit route: %v", err)
	}
	if err := (agentImpl{}).LaunchRefusal(terminal); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("guard, terminal target: %v", err)
	}
	managed := terminal
	managed.Driver = session.DriverManaged
	if err := (agentImpl{}).LaunchRefusal(managed); err != nil {
		t.Errorf("guard, managed target: %v", err)
	}
	managed.Model = "anthropic/m"
	terminal.Model = "anthropic/m"
	if err := (agentImpl{}).LaunchRefusal(terminal); err != nil {
		t.Errorf("guard, another provider: %v", err)
	}
	// Nothing restricted: the guard is silent.
	useIsolationPolicy(t, routepolicy.Policy{Revision: 9})
	terminal.Model = "mine/m"
	if err := (agentImpl{}).LaunchRefusal(terminal); err != nil {
		t.Errorf("unrestricted: %v", err)
	}
}
