package claude

import (
	"os"
	"path/filepath"
	"testing"

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

	sub := resolveDefault(env(), nil, acct("uuid-1"))
	if sub.Route != RouteSubscription || sub.Binding != routes.AccountBinding(RouteSubscription, "uuid-1") {
		t.Fatalf("subscription = %+v", sub)
	}
	if o := resolveDefault(env(), nil, acct("uuid-2")); o.Binding == sub.Binding {
		t.Error("another account kept the binding")
	}
	// The anthropic-api binding is the one an explicit selection stores (BindRoute).
	if k := resolveDefault(env("ANTHROPIC_API_KEY", "sk-1"), nil, acct("uuid-1")); k.Route != RouteAPI ||
		k.Binding != apiKeyConnection+"@"+keyFingerprint("sk-1") {
		t.Errorf("env key = %+v", k)
	}
	if o := resolveDefault(env("CLAUDE_CODE_OAUTH_TOKEN", "tok"), nil, acct("uuid-1")); o.Route != RouteSubscription ||
		o.Binding != routes.KeyBinding("CLAUDE_CODE_OAUTH_TOKEN", "tok") {
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
	if b := (agentImpl{}).LaunchBinding(*sel(RouteAPI, "")); b != "" {
		t.Errorf("anthropic-api = %q, want BindRoute's", b)
	}
	if b := (agentImpl{}).LaunchBinding(*sel(RouteSubscription, "")); b != routes.AccountBinding(RouteSubscription, accountID()) {
		t.Errorf("subscription = %q", b)
	}
}
