package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
)

var openRouterOnly = routepolicy.Policy{Revision: 3, Restricted: true, Allowed: []string{routes.RouteOpenRouterAPI}}

// runPin is the enabled_providers and the rest of a run's inline configuration.
func runPin(t *testing.T, env []string) (enabled []string, cfg map[string]any) {
	t.Helper()
	v := envValue(env, routeConfigKey)
	if v == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(v), &cfg); err != nil {
		t.Fatalf("inline config %q: %v", v, err)
	}
	for _, p := range cfg["enabled_providers"].([]any) {
		enabled = append(enabled, p.(string))
	}
	return enabled, cfg
}

// A headless `opencode run` is isolated under a restriction like the shared serve: with a retained
// opencode.ai login beside an allowed OpenRouter key, its pin lists only openrouter, so opencode's
// own small-model and title calls cannot reach opencode.ai; inherited forbidden keys and the config
// sources that could replace the pin are gone, and the run's own inline configuration is kept.
// With no restriction the environment is returned unchanged.
func TestRunPolicyEnvIsolatesAStandaloneRun(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	oldView := credentialView
	t.Cleanup(func() { credentialView = oldView })
	credentialView = func() (map[string]string, string, oauthState) {
		return map[string]string{"OPENROUTER_API_KEY": "or"}, UsageZen, oauthState{known: true, connected: true}
	}
	own := `{"model":"openrouter/x","experimental":{"policies":[{"action":"tool.use","resource":"shell:*","effect":"deny"}]}}`
	base := []string{"PATH=/bin", "OPENROUTER_API_KEY=or", opencodeKeyEnv + "=inherited", "ANTHROPIC_API_KEY=inherited",
		"OPENCODE_CONFIG=/x/af.json", "OPENCODE_CONFIG_DIR=/x", routeConfigKey + "=" + own}

	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if got, err := RunPolicyEnv(base, t.TempDir()); err != nil || !slices.Equal(got, base) {
		t.Fatalf("control: the unrestricted run's environment changed: %v %v", got, err)
	}

	useIsolationPolicy(t, openRouterOnly)
	got, err := RunPolicyEnv(slices.Clone(base), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{opencodeKeyEnv + "=", "ANTHROPIC_API_KEY=", "OPENCODE_CONFIG=", "OPENCODE_CONFIG_DIR="} {
		if slices.ContainsFunc(got, func(e string) bool { return strings.HasPrefix(e, k) }) {
			t.Errorf("the restricted run got %s", k)
		}
	}
	if !slices.Contains(got, "OPENROUTER_API_KEY=or") {
		t.Error("the allowed key was removed")
	}
	enabled, cfg := runPin(t, got)
	if !slices.Equal(enabled, []string{"openrouter"}) {
		t.Errorf("enabled_providers = %v, want only openrouter", enabled)
	}
	if cfg["model"] != "openrouter/x" || cfg["experimental"] == nil {
		t.Errorf("the run's own inline configuration was lost: %v", cfg)
	}
	if n := len(slices.DeleteFunc(slices.Clone(got), func(e string) bool { return !strings.HasPrefix(e, routeConfigKey+"=") })); n != 1 {
		t.Errorf("%d inline configurations", n)
	}

	// A run with no inline configuration of its own gets the pin alone.
	if enabled, _ := runPin(t, mustRunEnv(t, []string{"PATH=/bin"})); !slices.Equal(enabled, []string{"openrouter"}) {
		t.Errorf("bare run: enabled_providers = %v", enabled)
	}
	if _, err := RunPolicyEnv([]string{routeConfigKey + "={not json"}, t.TempDir()); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("an unreadable inline configuration: %v", err)
	}
}

func mustRunEnv(t *testing.T, env []string) []string {
	t.Helper()
	out, err := RunPolicyEnv(env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A config file a run reads that would widen the pin refuses it under a restriction: a global one,
// or one in the run's directory or above. With no restriction the files change nothing.
func TestRunPolicyEnvRefusesAConfigThatWidensThePin(t *testing.T) {
	home := isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	dir := filepath.Join(home, "chat", "c1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(f, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{globalConfigFiles()[0], filepath.Join(home, "chat", "opencode.json")} {
		write(f, `{"enabled_providers":["opencode"]}`)
		useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
		if _, err := RunPolicyEnv(nil, dir); err != nil {
			t.Errorf("control: unrestricted with %s: %v", f, err)
		}
		useIsolationPolicy(t, openRouterOnly)
		if _, err := RunPolicyEnv(nil, dir); !errors.Is(err, ErrPolicyConfig) {
			t.Errorf("a run over %s that sets enabled_providers: %v", f, err)
		}
		_ = os.Remove(f)
	}
}
