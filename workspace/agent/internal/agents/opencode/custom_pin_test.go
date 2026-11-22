package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

var selfHosted = &secrets.CustomAPIConn{Provider: "mine", URL: "https://llm.internal.example/v1", APIKey: "k"}

func approvedCustom() routepolicy.Policy {
	return routepolicy.Policy{Revision: 5, Restricted: true, Allowed: []string{},
		Endpoints: []string{"llm.internal.example"}, EndpointRoutes: []string{routes.RouteCustomAPI}}
}

func useCustom(t *testing.T, c *secrets.CustomAPIConn) {
	t.Helper()
	old := credentialView
	t.Cleanup(func() { credentialView = old })
	credentialView = func() (map[string]string, string, oauthState, *secrets.CustomAPIConn) {
		return nil, UsageZen, oauthState{known: true}, c
	}
}

// The binding is of the provider the URL belongs to; another custom provider is not on that
// endpoint and resolves to nothing, because the route's approval is of one host. Without a
// connection the launch is what it was.
func TestCustomLaunchIsTiedToTheProvider(t *testing.T) {
	got := resolveModel("mine/m", nil, UsageZen, oauthState{}, nil, selfHosted)
	want := routes.Launch{Route: routes.RouteCustomAPI, Binding: credbind.Endpoint(routes.RouteCustomAPI, selfHosted.URL, "k")}
	if got != want || got.Binding == "" {
		t.Errorf("own provider: %+v, want %+v", got, want)
	}
	if l := resolveModel("other/m", nil, UsageZen, oauthState{}, nil, selfHosted); l != (routes.Launch{}) {
		t.Errorf("another custom provider with a connection stored: %+v", l)
	}
	if l := resolveModel("other/m", nil, UsageZen, oauthState{}, nil, nil); l != (routes.Launch{Route: routes.RouteCustomAPI}) {
		t.Errorf("no connection: %+v", l)
	}
	moved := *selfHosted
	moved.URL = "https://elsewhere.example/v1"
	if l := resolveModel("mine/m", nil, UsageZen, oauthState{}, nil, &moved); l.Binding == got.Binding {
		t.Error("repointing the URL kept the binding")
	}
	// A connection that names a provider another route owns is not one.
	for _, id := range []string{"anthropic", "opencode", "llamacpp", " "} {
		c := *selfHosted
		c.Provider = id
		if customConn(&c, []string{"llamacpp"}) != nil {
			t.Errorf("provider %q accepted", id)
		}
	}
}

// Under a restriction that approves the host the route is allowed and the serve's pin lists the
// custom provider with its URL; with the host not approved, or no connection, nothing changes.
func TestCustomEndpointIsPinnedWhenApproved(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	env, ok := servePolicyEnv(nil)
	if !ok {
		t.Fatal("not restricted")
	}
	enabled, cfg := runPin(t, env)
	if strings.Join(enabled, ",") != "mine" {
		t.Errorf("enabled_providers = %v", enabled)
	}
	url := cfg["provider"].(map[string]any)["mine"].(map[string]any)["options"].(map[string]any)["baseURL"]
	if url != selfHosted.URL {
		t.Errorf("baseURL = %v", url)
	}
	// The run's own provider entries are kept.
	out, err := RunPolicyEnv([]string{routeConfigKey + `={"provider":{"mine":{"options":{"apiKey":"x"}}}}`}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, cfg = runPin(t, out)
	opts := cfg["provider"].(map[string]any)["mine"].(map[string]any)["options"].(map[string]any)
	if opts["baseURL"] != selfHosted.URL || opts["apiKey"] != "x" {
		t.Errorf("merged options = %v", opts)
	}

	useCustom(t, &secrets.CustomAPIConn{Provider: "mine", URL: "https://evil.example/v1"})
	env, _ = servePolicyEnv(nil)
	if enabled, cfg = runPin(t, env); len(enabled) != 0 || cfg["provider"] != nil {
		t.Errorf("unapproved host pinned: %v %v", enabled, cfg)
	}
	useCustom(t, nil)
	env, _ = servePolicyEnv(nil)
	if enabled, cfg = runPin(t, env); len(enabled) != 0 || cfg["provider"] != nil {
		t.Errorf("no connection pinned: %v %v", enabled, cfg)
	}
}

// A changed custom URL is another pin: the serve's credential generation moves.
func TestCustomPinMovesTheCredentialGeneration(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	a := credentialGen()
	moved := *selfHosted
	moved.URL = "https://llm.internal.example/v2"
	useCustom(t, &moved)
	if b := credentialGen(); a == b {
		t.Error("generation unchanged by a new URL")
	}
}

// A config file that sets the pinned provider's base URL refuses the run, as one that sets
// enabled_providers does; another provider's URL, or no stored connection, is no conflict.
func TestConfigFileOverridingTheCustomURLIsRefused(t *testing.T) {
	useIsolationPolicy(t, approvedCustom())
	useCustom(t, selfHosted)
	write := func(body string) string {
		f := filepath.Join(t.TempDir(), "opencode.json")
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	bad := map[string]string{
		"baseURL":    `{"provider":{"mine":{"options":{"baseURL":"https://evil.example"}}}}`,
		"lower-case": `{"provider":{"mine":{"options":{"baseurl":"https://evil.example"}}}}`,
		"unparsed":   `{"provider":{"mine":{"options":{"baseURL":"x"}}},}}}`,
	}
	for name, body := range bad {
		if err := policyConfigErr([]string{write(body)}); err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
	ok := write(`{"provider":{"other":{"options":{"baseURL":"https://x.example"}},"mine":{"models":{"m":{}}}}}`)
	if err := policyConfigErr([]string{ok}); err != nil {
		t.Errorf("unrelated config refused: %v", err)
	}
	useCustom(t, nil)
	if err := policyConfigErr([]string{write(bad["baseURL"])}); err != nil {
		t.Errorf("no connection stored, yet refused: %v", err)
	}
}

// The registered host is the connection's, and only for a usable one.
func TestCustomEndpointHostIsRegistered(t *testing.T) {
	p := approvedCustom()
	useIsolationPolicy(t, p)
	useCustom(t, selfHosted)
	if !p.Allows(routes.RouteCustomAPI) {
		t.Error("approved host not allowed")
	}
	useCustom(t, &secrets.CustomAPIConn{Provider: "mine", URL: "https://evil.example"})
	if p.Allows(routes.RouteCustomAPI) {
		t.Error("another host allowed")
	}
	useCustom(t, nil)
	if p.Allows(routes.RouteCustomAPI) {
		t.Error("no connection allowed")
	}
}
