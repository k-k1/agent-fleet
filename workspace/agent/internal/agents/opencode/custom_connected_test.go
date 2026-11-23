package opencode

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

func storeCustom(t *testing.T, c *secrets.CustomAPIConn) {
	t.Helper()
	if err := secrets.Update(func(s *secrets.Data) error { s.CustomAPI = c; return nil }); err != nil {
		t.Fatal(err)
	}
}

// On "own" a workspace whose only provider is the approved custom endpoint is connected: the
// serve's pin gives it that endpoint, so there is something behind the launch. Not approved (host
// off the list, no restriction, no connection) it is not, and "off" wins over all of it.
func TestConnectedOnOwnCountsTheApprovedCustomEndpoint(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	_ = newFakeDaemon(t) // isolate from a developer's real Console login
	stubUsagePref(t, UsageOwn)

	useIsolationPolicy(t, approvedCustom())
	if Connected() {
		t.Fatal("connected=true on own with no connection stored at all")
	}
	storeCustom(t, selfHosted)
	if !Connected() {
		t.Fatal("connected=false on own although the approved custom endpoint is stored")
	}

	storeCustom(t, &secrets.CustomAPIConn{Provider: "mine", URL: "https://evil.example/v1", APIKey: "k"})
	if Connected() {
		t.Error("connected=true for a custom endpoint whose host the policy does not approve")
	}
	storeCustom(t, &secrets.CustomAPIConn{Provider: "anthropic", URL: selfHosted.URL, APIKey: "k"})
	if Connected() {
		t.Error("connected=true for a connection naming a provider another route owns")
	}

	storeCustom(t, selfHosted)
	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if Connected() {
		t.Error("connected=true with no restriction held: nothing pins the custom provider then")
	}

	useIsolationPolicy(t, approvedCustom())
	stubUsagePref(t, UsageOff)
	if Connected() {
		t.Error("connected=true under an explicit off")
	}
}

// The reported failure: with the approved custom endpoint as the only provider on "own", the
// shared serve starts (it used to answer ErrNotConnected) and is pinned to that provider.
func TestSharedServeStartsForACustomOnlyWorkspaceOnOwn(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	_ = newFakeDaemon(t)
	useMajor(t, 2)
	stubUsagePref(t, UsageOwn)
	t.Setenv(standaloneEnv, "")
	// An inherited disable switch would refuse before the connection gate this test measures.
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "")
	storeCustom(t, selfHosted)
	useIsolationPolicy(t, approvedCustom())

	// The fake opencode never answers its health check, so Ensure ends in a start timeout either
	// way; what is measured is the gate: not ErrNotConnected, and the process was spawned.
	env, err := startServeForEnv(t)
	if errors.Is(err, ErrNotConnected) {
		t.Fatalf("Ensure = %v, want the connection gate passed", err)
	}
	if env == "" {
		t.Fatalf("the serve was not spawned (Ensure = %v)", err)
	}
	var pin struct {
		Enabled []string `json:"enabled_providers"`
	}
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, routeConfigKey+"="); ok {
			_ = json.Unmarshal([]byte(v), &pin)
		}
	}
	if !slices.Equal(pin.Enabled, []string{"mine"}) {
		t.Errorf("enabled_providers = %v, want only the custom provider", pin.Enabled)
	}
}

// The connection gate and the pin are two readings. A restriction lifted between them must not
// start a custom-only serve: unrestricted there is no pin, and a launch would fall through to
// opencode's own default model. Judged again after the policy reading, the spawn is refused.
func TestSharedServeIsNotSpawnedWhenThePolicyIsLiftedAfterTheGate(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	_ = newFakeDaemon(t)
	useMajor(t, 2)
	stubUsagePref(t, UsageOwn)
	t.Setenv(standaloneEnv, "")
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "")
	storeCustom(t, selfHosted)
	useIsolationPolicy(t, approvedCustom())

	lifted := routepolicy.Open(filepath.Join(t.TempDir(), "rp-lifted.json"), "")
	if _, err := lifted.Accept(routepolicy.Policy{Revision: 6, Allowed: []string{}}); err != nil {
		t.Fatal(err)
	}
	routepolicy.WaitListeners()
	held := routepolicy.Active
	serveBeforeView = func() { routepolicy.Active = func() *routepolicy.Store { return lifted } }
	t.Cleanup(func() { serveBeforeView = nil; routepolicy.Active = held })

	env, err := startServeForEnv(t)
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Ensure = %v, want ErrNotConnected", err)
	}
	if env != "" {
		t.Errorf("a serve was spawned with no pin:\n%s", env)
	}
}
