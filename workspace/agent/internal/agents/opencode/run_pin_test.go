package opencode

import (
	"errors"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var movedHost = &secrets.CustomAPIConn{Provider: "mine", URL: "https://llm.internal.example/v2", APIKey: "k"}

func pinnedBaseURL(t *testing.T, env []string) string {
	t.Helper()
	_, cfg := runPin(t, env)
	return cfg["provider"].(map[string]any)["mine"].(map[string]any)["options"].(map[string]any)["baseURL"].(string)
}

// The recorded binding and the pinned URL come from one reading: a connection updated after the
// reading changes neither, so a call recorded under URL A never goes to the approved URL B.
func TestRunPinKeepsBindingAndURLOnOneReading(t *testing.T) {
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
// launch was judged on, and let through when they agree.
func TestServePinMatchesTheJudgedView(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	judged := readView()
	s := &Supervisor{up: true, credGen: credentialGenFrom(judged)}
	if err := s.servePinMatches(judged); err != nil {
		t.Errorf("same pin refused: %v", err)
	}
	useCustom(t, movedHost)
	if err := s.servePinMatches(readView()); !errors.Is(err, ErrServePolicyMoved) {
		t.Errorf("serve on the old pin: err = %v, want ErrServePolicyMoved", err)
	}
	s.credGen = ""
	if err := s.servePinMatches(readView()); err != nil {
		t.Errorf("adopted daemon judged: %v", err)
	}
}

// A run that already has a v2 `providers` block for the provider gets the pin there too (that
// block takes the route over from the v1 one); a run without one gets none added.
func TestRunPinCarriesTheURLInAnExistingV2Block(t *testing.T) {
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
