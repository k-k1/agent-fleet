package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/credbind"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

func TestResolveDefault(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	acct := func(id string) func() string { return func() string { return id } }
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	// No credentials file: the subscription route with nothing to bind.
	if l := resolveDefault(env(), nil, acct("uuid-1")); l != (routes.Launch{Route: RouteSubscription}) {
		t.Errorf("no credentials file = %+v", l)
	}
	if err := os.WriteFile(credsPath(), []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := resolveDefault(env(), nil, acct("uuid-1"))
	if sub.Route != RouteSubscription || sub.Binding != credbind.Account(RouteSubscription, "uuid-1") {
		t.Fatalf("subscription = %+v", sub)
	}
	// A login that names no account binds to its generation, which the Agent's login flow bumps.
	g := resolveDefault(env(), nil, acct(""))
	if g.Binding == "" || g.Binding != credbind.Generation(RouteSubscription) {
		t.Errorf("identityless login = %+v", g)
	}
	credbind.Bump(RouteSubscription)
	if n := resolveDefault(env(), nil, acct("")); n.Binding == g.Binding {
		t.Error("a bumped generation kept the binding")
	}
	if o := resolveDefault(env(), nil, acct("uuid-2")); o.Binding == sub.Binding {
		t.Error("another account kept the binding")
	}
	// The keyed binding of the key, the same one an explicit selection's record gets.
	if k := resolveDefault(env("ANTHROPIC_API_KEY", "sk-1"), nil, acct("uuid-1")); k.Route != RouteAPI ||
		k.Binding != credbind.Key(apiKeyConnection, "sk-1") || strings.Contains(k.Binding, keyFingerprint("sk-1")) {
		t.Errorf("env key = %+v", k)
	}
	if o := resolveDefault(env("CLAUDE_CODE_OAUTH_TOKEN", "tok"), nil, acct("uuid-1")); o.Route != RouteSubscription ||
		o.Binding != credbind.Key("CLAUDE_CODE_OAUTH_TOKEN", "tok") {
		t.Errorf("env token = %+v", o)
	}
	for name, getenv := range map[string]func(string) string{
		"both tokens":       env("ANTHROPIC_API_KEY", "sk", "CLAUDE_CODE_OAUTH_TOKEN", "tok"),
		"a provider switch": env("CLAUDE_CODE_USE_BEDROCK", "1"),
		"a base URL":        env("ANTHROPIC_BASE_URL", "http://127.0.0.1:1"),
	} {
		if l := resolveDefault(getenv, nil, acct("uuid-1")); l != (routes.Launch{}) {
			t.Errorf("%s resolved to %+v", name, l)
		}
	}
	f := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(f, []byte(`{"env":{"ANTHROPIC_API_KEY":"sk"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if l := resolveDefault(env(), []string{f}, acct("uuid-1")); l != (routes.Launch{}) {
		t.Errorf("a settings file that sets a key resolved to %+v", l)
	}
}

// An explicit subscription selection names no connection; the launch record binds it to the
// account all the same, and leaves the other routes to BindRoute.
func TestLaunchBindingSubscription(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	old := loadAPIKey
	t.Cleanup(func() { loadAPIKey = old })
	loadAPIKey = func() (string, error) { return "sk-ant-1", nil }
	bound := apiKeyConnection + "@" + keyFingerprint("sk-ant-1")
	if b := (agentImpl{}).LaunchBinding(*sel(RouteAPI, bound)); b != credbind.Key(apiKeyConnection, "sk-ant-1") || strings.Contains(b, keyFingerprint("sk-ant-1")) {
		t.Errorf("anthropic-api = %q, want the keyed binding instead of the stored fingerprint", b)
	}
	if b := (agentImpl{}).LaunchBinding(*sel(RouteAPI, apiKeyConnection+"@0000000000000000")); b != "" {
		t.Errorf("a selection bound to another key = %q, want unproven", b)
	}
	if b := (agentImpl{}).LaunchBinding(*sel(RouteBedrock, "p1@us-east-1#0011223344556677")); b != "" {
		t.Errorf("bedrock = %q, want the stored connection kept", b)
	}
	if b := (agentImpl{}).LaunchBinding(*sel(RouteSubscription, "")); b != "" {
		t.Errorf("subscription without a credentials file = %q", b)
	}
	if err := os.WriteFile(credsPath(), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b := (agentImpl{}).LaunchBinding(*sel(RouteSubscription, "")); b == "" {
		t.Error("subscription with a credentials file has no binding")
	}
}

// Usable means held: an open acceptance gate is not a credential, and a credential in the
// environment counts as much as one in the store.
func TestUsableRoutesNeedACredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv(bedrockGateEnv, "1")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	old := loadAPIKey
	t.Cleanup(func() { loadAPIKey = old })
	loadAPIKey = func() (string, error) { return "", nil }
	if u := (agentImpl{}).UsableRoutes(); len(u) != 0 {
		t.Errorf("nothing held, gate open: %v", u)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok-env")
	if u := (agentImpl{}).UsableRoutes(); len(u) != 2 || u[0] != RouteAPI || u[1] != RouteSubscription {
		t.Errorf("env credentials: %v", u)
	}
}
