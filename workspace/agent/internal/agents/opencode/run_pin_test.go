package opencode

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var movedHost = &secrets.CustomAPIConn{Provider: "mine", URL: "https://llm.internal.example/v2", APIKey: "k"}

// isolateConfig points the test process's HOME and XDG config root at an empty directory: Env
// inspects the global config files, and a developer's own enabled_providers must not fail it.
func isolateConfig(t *testing.T) {
	t.Helper()
	home := isolatedHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

func pinnedBaseURL(t *testing.T, env []string) string {
	t.Helper()
	_, cfg := runPin(t, env)
	return cfg["provider"].(map[string]any)["mine"].(map[string]any)["options"].(map[string]any)["baseURL"].(string)
}

// The recorded binding and the pinned URL come from one reading: a connection updated after the
// reading changes neither, so a call recorded under URL A never goes to the approved URL B.
func TestRunPinKeepsBindingAndURLOnOneReading(t *testing.T) {
	isolateConfig(t)
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	pin := PinRun("mine/m")
	useCustom(t, movedHost)

	if want := credbind.Endpoint(routes.RouteCustomAPI, selfHosted.URL, ""); pin.Launch.Binding != want {
		t.Fatalf("binding = %q, want the first connection's %q", pin.Launch.Binding, want)
	}
	env, err := pin.Env(nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := pinnedBaseURL(t, env); got != selfHosted.URL {
		t.Errorf("pinned %q after the connection moved, want %q", got, selfHosted.URL)
	}
	// A fresh reading follows the new connection, binding and URL together.
	next := PinRun("mine/m")
	env, err = next.Env(nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := pinnedBaseURL(t, env); got != movedHost.URL || next.Launch.Binding == pin.Launch.Binding {
		t.Errorf("fresh reading pinned %q with binding %q", got, next.Launch.Binding)
	}
}

// A launch recorded for one endpoint is refused against a pin of another, and a reading that was
// never taken pins nothing.
func TestRunPinRefusesAMismatchedBinding(t *testing.T) {
	isolateConfig(t)
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	pin := PinRun("mine/m")
	pin.Launch.Binding = credbind.Endpoint(routes.RouteCustomAPI, movedHost.URL, "")
	if _, err := pin.Env(nil, t.TempDir()); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("mismatched binding: err = %v, want ErrPolicyConfig", err)
	}
	if _, err := (RunPin{}).Env(nil, t.TempDir()); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("zero pin: err = %v, want ErrPolicyConfig", err)
	}
}

// A Managed turn is refused when the shared serve runs on another custom pin than the one its
// launch was judged on, in either direction; a different key generation with the same pin is not
// a reason, and an adopted daemon is not judged.
func TestServePinMatchesTheJudgedView(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	judged := readView()
	s := &Supervisor{up: true, credGen: "some-older-generation", customPin: customPinDigest(judged.p, judged.engines, judged.custom)}
	if err := s.servePinMatches(judged); err != nil {
		t.Errorf("same custom pin, other generation: refused: %v", err)
	}
	useCustom(t, movedHost)
	if err := s.servePinMatches(readView()); !errors.Is(err, ErrServePolicyMoved) {
		t.Errorf("serve on the old pin: err = %v, want ErrServePolicyMoved", err)
	}
	// The serve has a pin the judged view did not carry.
	useCustom(t, nil)
	if err := s.servePinMatches(readView()); !errors.Is(err, ErrServePolicyMoved) {
		t.Errorf("serve pinned, view not: err = %v, want ErrServePolicyMoved", err)
	}
	// The view carries a pin the serve lacks.
	useCustom(t, selfHosted)
	bare := &Supervisor{up: true, credGen: "g"}
	if err := bare.servePinMatches(readView()); !errors.Is(err, ErrServePolicyMoved) {
		t.Errorf("view pinned, serve not: err = %v, want ErrServePolicyMoved", err)
	}
	s.credGen = ""
	if err := s.servePinMatches(readView()); err != nil {
		t.Errorf("adopted daemon judged: %v", err)
	}
}

// While nothing is restricted the launch record is what ModelLaunch gives (stored keys, billing
// choice, engines), and the environment passes through unchanged.
func TestRunPinUnrestrictedLaunchIsModelLaunch(t *testing.T) {
	isolateConfig(t)
	useIsolationPolicy(t, routepolicy.Policy{})
	old := credentialView
	t.Cleanup(func() { credentialView = old })
	credentialView = func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, UsageZen, oauthState{known: true}, selfHosted
	}
	for _, model := range []string{"anthropic/claude-x", "mine/m", "opencode/free"} {
		if got, want := PinRun(model).Launch, ModelLaunch(model); got != want {
			t.Errorf("%s: pin launch %+v, ModelLaunch %+v", model, got, want)
		}
	}
	if l := PinRun("anthropic/claude-x").Launch; l.Route == "" || l.Binding == "" {
		t.Errorf("a stored key resolved to %+v", l)
	}
	env := []string{"A=1"}
	if out, err := PinRun("mine/m").Env(env, t.TempDir()); err != nil || len(out) != 1 {
		t.Errorf("env = %v, %v; want it unchanged", out, err)
	}
}

// A pin taken before a policy was accepted is refused afterwards: the stored endpoint's host may
// now be allowed while the run's own settings were never judged against the new restriction.
func TestRunPinRefusedWhenThePolicyMoved(t *testing.T) {
	isolateConfig(t)
	useIsolationPolicy(t, routepolicy.Policy{})
	useCustom(t, selfHosted)
	pin := PinRun("mine/m")
	useIsolationPolicy(t, approvedCustom())
	if _, err := pin.Env(nil, t.TempDir()); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("err = %v, want ErrPolicyConfig", err)
	}
	if _, err := PinRun("mine/m").Env(nil, t.TempDir()); err != nil {
		t.Errorf("a fresh pin refused: %v", err)
	}
}

// A run that already has a v2 `providers` block for the provider gets the pin there too (that
// block takes the route over from the v1 one); a run without one gets none added.
func TestRunPinCarriesTheURLInAnExistingV2Block(t *testing.T) {
	isolateConfig(t)
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	env, err := PinRun("mine/m").Env([]string{routeConfigKey + `={"providers":{"mine":{"settings":{"baseURL":"https://evil.example","apiKey":"x"}}}}`}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, cfg := runPin(t, env)
	set := cfg["providers"].(map[string]any)["mine"].(map[string]any)["settings"].(map[string]any)
	if set["baseURL"] != selfHosted.URL || set["apiKey"] != "x" {
		t.Errorf("v2 settings = %v", set)
	}
	env, _ = PinRun("mine/m").Env(nil, t.TempDir())
	if _, cfg = runPin(t, env); cfg["providers"] != nil {
		t.Errorf("a v2 block was added: %v", cfg["providers"])
	}
}

// The same revision with other effective values (a store completing a stale restriction's
// endpoints) is a moved policy too.
func TestRunPinRefusedWhenTheSameRevisionIsCompleted(t *testing.T) {
	isolateConfig(t)
	stale := routepolicy.Policy{Revision: 5, Restricted: true, Allowed: []string{}}
	useIsolationPolicy(t, stale)
	useCustom(t, selfHosted)
	pin := PinRun("mine/m")
	useIsolationPolicy(t, approvedCustom()) // revision 5, endpoints now present
	if _, err := pin.Env(nil, t.TempDir()); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("err = %v, want ErrPolicyConfig", err)
	}
}

// A serve that kept its pin past a lifted restriction serves every model on the next resume; a
// restriction that arrives between the judgement and Ensure is still caught.
func TestServePinSurvivesALiftedRestriction(t *testing.T) {
	isolateConfig(t)
	useCustom(t, selfHosted)
	pinned := &Supervisor{up: true, credGen: "g", customPin: "abc123"}

	useIsolationPolicy(t, routepolicy.Policy{})
	if err := pinned.servePinMatches(readView()); err != nil {
		t.Errorf("unrestricted resume on a pinned serve refused: %v", err)
	}
	judged := readView() // unrestricted
	useIsolationPolicy(t, approvedCustom())
	cur := readView()
	if err := pinned.servePinMatches(judged); !errors.Is(err, ErrServePolicyMoved) {
		t.Errorf("restriction arrived after the judgement: err = %v, want ErrServePolicyMoved", err)
	}
	_ = cur
}
