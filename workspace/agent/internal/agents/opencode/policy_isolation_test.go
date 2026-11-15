package opencode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/routepolicy"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/routes"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func useIsolationPolicy(t *testing.T, p routepolicy.Policy) {
	t.Helper()
	s := routepolicy.Open(filepath.Join(t.TempDir(), "rp.json"), "")
	if _, err := s.Accept(p); err != nil {
		t.Fatal(err)
	}
	// The policy listeners (Accept starts them), the security restarts they or Ensure start, and
	// the reconciles those start, an earlier test's included, read Active in the background.
	settle := func() {
		routepolicy.WaitListeners()
		reconcileInflight.Wait()
	}
	settle()
	old := routepolicy.Active
	routepolicy.Active = func() *routepolicy.Store { return s }
	t.Cleanup(func() {
		settle()
		routepolicy.Active = old
	})
}

var anthropicOnly = routepolicy.Policy{Revision: 2, Restricted: true, Allowed: []string{routes.RouteAnthropicAPI}}

func storeKeys(t *testing.T, keys map[string]string) {
	t.Helper()
	if err := secrets.Update(func(s *secrets.Data) error { s.Opencode = keys; return nil }); err != nil {
		t.Fatal(err)
	}
}

// Under a restriction, a key whose routes are all forbidden, and the engine token when
// af-engine-llm is, are not put in an opencode process's environment; with no restriction every
// key goes in as before.
func TestKeyEnvUnderThePolicy(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	stubUsagePref(t, UsageZen)
	storeKeys(t, map[string]string{"ANTHROPIC_API_KEY": "a", "OPENAI_API_KEY": "o", opencodeKeyEnv: "z", "MY_CUSTOM_KEY": "c"})
	oldEngine := EngineEnv
	EngineEnv = func(string) []string { return []string{"AF_ENGINE_TOKEN=t"} }
	t.Cleanup(func() { EngineEnv = oldEngine })
	names := func() []string {
		var out []string
		for _, kv := range keyEnv() {
			out = append(out, strings.SplitN(kv, "=", 2)[0])
		}
		slices.Sort(out)
		return out
	}
	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if got := names(); len(got) != 5 {
		t.Fatalf("control: unrestricted env = %v", got)
	}
	useIsolationPolicy(t, anthropicOnly)
	if got := names(); !slices.Equal(got, []string{"ANTHROPIC_API_KEY"}) {
		t.Errorf("restricted env = %v, want only the allowed provider's key", got)
	}
}

// The shared serve's provider list: a provider is enabled only when every party that could pay
// there is allowed.
func TestAllowedServeProviders(t *testing.T) {
	key := map[string]string{opencodeKeyEnv: "z"}
	out := oauthState{known: true}
	acct := oauthState{known: true, connected: true, id: "c1"}
	zenOnly := routepolicy.Policy{Restricted: true, Allowed: []string{routes.RouteOpencodeZen, routes.RouteAnthropicAPI}}
	for name, tc := range map[string]struct {
		p    routepolicy.Policy
		keys map[string]string
		acct oauthState
		want []string
	}{
		"free, Zen allowed":          {zenOnly, nil, out, []string{"anthropic", "opencode"}},
		"key, Zen allowed":           {zenOnly, key, out, []string{"anthropic", "opencode"}},
		"account forbidden":          {zenOnly, nil, acct, []string{"anthropic"}},
		"account state unknown":      {zenOnly, nil, oauthState{}, []string{"anthropic"}},
		"Go needs its key's route":   {routepolicy.Policy{Restricted: true, Allowed: []string{routes.RouteOpencodeGo, routes.RouteOpencodeZen}}, key, out, []string{"opencode", "opencode-go"}},
		"engines with af-engine-llm": {routepolicy.Policy{Restricted: true, Allowed: []string{routes.RouteAFEngineLLM}}, nil, out, []string{"llamacpp"}},
	} {
		if got := allowedServeProviders(tc.p, tc.keys, UsageZen, tc.acct, []string{"llamacpp"}); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// startServeForEnv starts the shared serve on a fake opencode that writes its environment, and
// returns that environment (empty when it was not started) and Ensure's error.
func startServeForEnv(t *testing.T) (string, error) {
	t.Helper()
	home := os.Getenv("HOME")
	bin := t.TempDir()
	envLog := filepath.Join(home, "child-env")
	_ = os.Remove(envLog)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\nenv > "+envLog+"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(serveAddrEnv, "http://127.0.0.1:"+freePortForTest(t))
	oldTimeout := serveStartTimeout
	serveStartTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { serveStartTimeout = oldTimeout })
	s := &Supervisor{}
	t.Cleanup(s.Shutdown)
	_, _, err := s.Ensure()
	b, _ := os.ReadFile(envLog)
	return string(b), err
}

// The shared serve runs isolated under a restriction: forbidden keys, inherited ones included, are
// not in its environment, and an inline pin lists only the allowed providers. With no restriction
// it starts exactly as before.
func TestSharedServeIsIsolatedUnderThePolicy(t *testing.T) {
	isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	useMajor(t, 2)
	stubUsagePref(t, UsageZen)
	t.Setenv(standaloneEnv, "")
	t.Setenv("OPENAI_API_KEY", "inherited")
	storeKeys(t, map[string]string{"ANTHROPIC_API_KEY": "a", opencodeKeyEnv: "z"})

	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	env, _ := startServeForEnv(t)
	if env == "" || strings.Contains(env, routeConfigKey+"=") || !strings.Contains(env, "OPENAI_API_KEY=inherited") || !strings.Contains(env, opencodeKeyEnv+"=z") {
		t.Fatalf("control: the unrestricted serve's environment changed:\n%s", env)
	}

	useIsolationPolicy(t, anthropicOnly)
	env, _ = startServeForEnv(t)
	if env == "" {
		t.Fatal("the restricted serve was not started")
	}
	for _, k := range []string{"OPENAI_API_KEY=", opencodeKeyEnv + "="} {
		if strings.Contains(env, k) {
			t.Errorf("the restricted serve got %s", k)
		}
	}
	var pin struct {
		Enabled []string `json:"enabled_providers"`
	}
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, routeConfigKey+"="); ok {
			_ = json.Unmarshal([]byte(v), &pin)
		}
	}
	if !slices.Equal(pin.Enabled, []string{"anthropic"}) {
		t.Errorf("enabled_providers = %v, want only anthropic", pin.Enabled)
	}
}

// A config file that would override the pin makes opencode unavailable under a restriction, with
// the reason: the shared serve for a global file, a session for one in its directories. With no
// restriction the same files change nothing.
func TestConfigThatWidensThePinRefusesUnderThePolicy(t *testing.T) {
	home := isolatedHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	useMajor(t, 2)
	stubUsagePref(t, UsageZen)
	t.Setenv(standaloneEnv, "")
	storeKeys(t, map[string]string{"ANTHROPIC_API_KEY": "a"})
	if err := os.MkdirAll(filepath.Dir(globalConfigFiles()[0]), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalConfigFiles()[0], []byte(`{"enabled_providers":["openai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	useIsolationPolicy(t, routepolicy.Policy{Revision: 1, Allowed: []string{}})
	if env, _ := startServeForEnv(t); env == "" {
		t.Fatal("control: an unrestricted serve did not start with the file present")
	}
	useIsolationPolicy(t, anthropicOnly)
	if _, err := startServeForEnv(t); !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("the restricted serve started over a file that sets enabled_providers: %v", err)
	}
	_ = os.Remove(globalConfigFiles()[0])

	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{"policies":[{"action":"provider.use","effect":"allow"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := managedDriver{}.resume(session.Meta{Name: "oc1", Kind: session.KindOpencode, Dir: dir})
	if !errors.Is(err, ErrPolicyConfig) {
		t.Errorf("a session whose directory widens the pin: %v", err)
	}
}
