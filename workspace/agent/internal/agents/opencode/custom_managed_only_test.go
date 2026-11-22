package opencode

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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
	custom := session.Meta{Model: "mine/m"}
	other := session.Meta{Model: "anthropic/m"}
	v := readView()

	if err := customManagedOnlyErrFrom(custom, true, v, ""); err != nil {
		t.Errorf("managed, pinned root: %v", err)
	}
	if err := customManagedOnlyErrFrom(custom, false, v, ""); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("terminal / private server: %v", err)
	}
	if err := customManagedOnlyErrFrom(custom, true, v, "/elsewhere"); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("foreign config root: %v", err)
	}
	if err := customManagedOnlyErrFrom(other, false, v, "/elsewhere"); err != nil {
		t.Errorf("another provider is not affected: %v", err)
	}
	if err := customManagedOnlyErrFrom(session.Meta{}, false, v, ""); err != nil {
		t.Errorf("no model named: %v", err)
	}
}

// foreignConfigRoot is only a root other than HOME/.config: unset, empty and the same root with a
// trailing slash are the pinned one. A foreign root's opencode files are checked with the global ones.
func TestForeignConfigRootFilesAreChecked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, x := range []string{"", home + "/.config", home + "/.config/"} {
		t.Setenv("XDG_CONFIG_HOME", x)
		if r := foreignConfigRoot(); r != "" {
			t.Errorf("XDG_CONFIG_HOME=%q reported foreign: %q", x, r)
		}
		if n := len(globalConfigFiles()); n != len(configFileNames) {
			t.Errorf("XDG_CONFIG_HOME=%q: %d global files", x, n)
		}
	}
	other := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", other)
	if foreignConfigRoot() != other {
		t.Fatalf("foreign root = %q", foreignConfigRoot())
	}
	want := filepath.Join(other, "opencode", "opencode.json")
	if !slices.Contains(globalConfigFiles(), want) {
		t.Fatalf("global files %v lack %s", globalConfigFiles(), want)
	}
	// A file there that would widen the pin refuses the serve, as one under HOME does.
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte(`{"enabled_providers":["mine","anthropic"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	if err := policyConfigErrFrom(globalConfigFiles(), readView()); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("foreign-root file not refused: %v", err)
	}
}

// The refusal is reached from each entry point, before any process starts: BuildLaunch (Terminal),
// resume of a Managed session with an explicit route, and the LaunchGuard an execution-method
// switch asks first. A Managed session without a route, and another provider, pass the guard.
func TestCustomManagedOnlyAtTheEntryPoints(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
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
	// Managed on the shared serve under a foreign config root: Resume refuses before any serve starts.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	foreign := managed
	foreign.Model = "mine/m"
	if _, err := (managedDriver{}).Resume(foreign); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("resume under a foreign config root: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	// Nothing restricted: the guard is silent.
	useIsolationPolicy(t, routepolicy.Policy{Revision: 9})
	terminal.Model = "mine/m"
	if err := (agentImpl{}).LaunchRefusal(terminal); err != nil {
		t.Errorf("unrestricted: %v", err)
	}
}

// The Bedrock route's process gets XDG_CONFIG_HOME pinned to the HOME root, so a conflicting file
// under an inherited root must not stop it: only the shared serve and headless runs check that root.
func TestBedrockChecksOnlyTheHomeRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	other := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", other)
	f := filepath.Join(other, "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(`{"enabled_providers":["anthropic"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ref := configConflict(homeConfigFiles()); ref != nil {
		t.Errorf("a foreign-root file stopped the Bedrock route: %+v", ref)
	}
	if !slices.Contains(globalConfigFiles(), f) {
		t.Errorf("the serve's file list lacks %s", f)
	}
}
