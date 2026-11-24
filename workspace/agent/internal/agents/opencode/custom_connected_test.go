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

func serveOK() bool {
	ok, _ := serveGate()
	return ok
}

func storeCustom(t *testing.T, c *secrets.CustomAPIConn) {
	t.Helper()
	if err := secrets.Update(func(s *secrets.Data) error { s.CustomAPI = c; return nil }); err != nil {
		t.Fatal(err)
	}
}

// On "own" a workspace whose only provider is the approved custom endpoint may start the shared
// serve and the kind is launchable: the serve's pin gives it that endpoint. Not approved (host off
// the list, no restriction, no connection) it is not, and "off" wins over all of it. Connected,
// which the assistant's headless runs ask, never counts the custom endpoint: those runs carry no pin.
func TestServeConnectedOnOwnCountsTheApprovedCustomEndpoint(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "") // an inherited switch would hide the cases below
	_ = newFakeDaemon(t)                      // isolate from a developer's real Console login
	stubUsagePref(t, UsageOwn)
	status := func() bool {
		s, err := secrets.Load()
		if err != nil {
			t.Fatal(err)
		}
		return Status(s)["connected"] == true
	}

	useIsolationPolicy(t, approvedCustom())
	if serveOK() || status() {
		t.Fatal("connected on own with no connection stored at all")
	}
	storeCustom(t, selfHosted)
	if !serveOK() || !status() {
		t.Fatal("not connected on own although the approved custom endpoint is stored")
	}
	if Connected() {
		t.Error("Connected() counts the custom endpoint: a headless run has no pin to hold it to")
	}

	storeCustom(t, &secrets.CustomAPIConn{Provider: "mine", URL: "https://evil.example/v1", APIKey: "k"})
	if serveOK() || status() {
		t.Error("connected for a custom endpoint whose host the policy does not approve")
	}
	storeCustom(t, &secrets.CustomAPIConn{Provider: "anthropic", URL: selfHosted.URL, APIKey: "k"})
	if serveOK() || status() {
		t.Error("connected for a connection naming a provider another route owns")
	}

	storeCustom(t, selfHosted)
	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if serveOK() || status() {
		t.Error("connected with no restriction held: nothing pins the custom provider then")
	}

	// The custom endpoint is only ever served by the shared serve, so a disabled serve takes it away.
	useIsolationPolicy(t, approvedCustom())
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "1")
	if serveOK() || status() {
		t.Error("connected on the custom endpoint while the shared serve is disabled")
	}
	t.Setenv("AF_OPENCODE_SERVE_DISABLE", "")

	useIsolationPolicy(t, approvedCustom())
	stubUsagePref(t, UsageZen)
	if serveOK() {
		t.Error("the custom endpoint counted on a route other than own")
	}
	stubUsagePref(t, UsageOff)
	if serveOK() || status() {
		t.Error("connected under an explicit off")
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

// The connection gate and the pin are two readings. A restriction lifted, or the route switched to
// "off", between them must not start a custom-only serve: unrestricted there is no pin, and "off"
// is a hard lock. Judged again against the policy view the pin is built from, the spawn is refused.
func TestSharedServeIsNotSpawnedWhenTheSettingMovesAfterTheGate(t *testing.T) {
	for name, move := range map[string]func(t *testing.T){
		"restriction lifted": func(t *testing.T) {
			lifted := routepolicy.Open(filepath.Join(t.TempDir(), "rp-lifted.json"), "")
			if _, err := lifted.Accept(routepolicy.Policy{Revision: 6, Allowed: []string{}}); err != nil {
				t.Fatal(err)
			}
			routepolicy.WaitListeners()
			routepolicy.Active = func() *routepolicy.Store { return lifted }
		},
		"route switched to off": func(t *testing.T) { UsagePref = func() string { return UsageOff } },
	} {
		t.Run(name, func(t *testing.T) {
			isolatedHome(t)
			t.Setenv("AF_SECRET_KEY", "")
			_ = newFakeDaemon(t)
			useMajor(t, 2)
			stubUsagePref(t, UsageOwn)
			t.Setenv(standaloneEnv, "")
			t.Setenv("AF_OPENCODE_SERVE_DISABLE", "")
			storeCustom(t, selfHosted)
			useIsolationPolicy(t, approvedCustom())

			held, prefHeld := routepolicy.Active, UsagePref
			serveBeforeView = func() { move(t) }
			t.Cleanup(func() { serveBeforeView = nil; routepolicy.Active = held; UsagePref = prefHeld })

			env, err := startServeForEnv(t)
			if !errors.Is(err, ErrNotConnected) {
				t.Fatalf("Ensure = %v, want ErrNotConnected", err)
			}
			if env != "" {
				t.Errorf("a serve was spawned:\n%s", env)
			}
		})
	}
}
