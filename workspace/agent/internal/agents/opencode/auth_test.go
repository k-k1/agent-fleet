package opencode

import (
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Keys are injected into the environment at startup, so saving or deleting one has no effect
// on a running daemon. Measured: deleting a key in the Console leaves the daemon holding it
// in its own environment, still reporting the env connection in connections[] and still
// listing models that could be billed against that key (restarting the Agent does not help,
// because Ensure adopts the live daemon). Supervisor.Restart is the path that applies it.
func TestApplyKeyChangeRestartsServeAndDropsCatalog(t *testing.T) {
	modelsMu.Lock()
	modelsList, modelsAt = []string{"opencode/stale"}, time.Now()
	modelsMu.Unlock()

	got := make(chan string, 1)
	orig := restartServe
	restartServe = func(reason string) { got <- reason }
	defer func() { restartServe = orig }()

	applyKeyChange("provider key removed: OPENCODE_API_KEY")

	select {
	case reason := <-got:
		if reason == "" {
			t.Error("the restart reason is empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a key change did not restart serve — a deleted key stays in the daemon")
	}
	modelsMu.Lock()
	stale := !modelsAt.IsZero()
	modelsMu.Unlock()
	if stale {
		t.Error("the model cache is still valid after a key change")
	}
}

// On the free tier the opencode.ai key is not injected, so that a workspace which chose the
// free tier does not end up on a billed path merely because a key is still stored. Other
// providers' keys are billed to the user themselves and are left alone on every tier.
func TestEnvDropsOpencodeKeyOnlyForFreeUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.Opencode = map[string]string{"OPENCODE_API_KEY": "sk-oc", "ANTHROPIC_API_KEY": "sk-an"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	orig := UsagePref
	defer func() { UsagePref = orig }()

	UsagePref = func() string { return UsageZen }
	if got := env(); len(got) != 2 {
		t.Errorf("a key was dropped on zen: %v", redactEnv(got))
	}
	UsagePref = func() string { return UsageGo }
	if got := env(); len(got) != 2 {
		t.Errorf("a key was dropped on go: %v", redactEnv(got))
	}
	UsagePref = func() string { return UsageFree }
	got := env()
	if len(got) != 1 || !strings.HasPrefix(got[0], "ANTHROPIC_API_KEY=") {
		t.Errorf("free = %v, want ANTHROPIC_API_KEY only", redactEnv(got))
	}
}

// TestUsagePrefDefaultsToOff pins the package-level default of UsagePref itself
// (before internal/agents/opencode.UsagePref is wired to ui_prefs.go's reader) — a
// fresh workspace, or a test that never overrides UsagePref, must see UsageOff.
func TestUsagePrefDefaultsToOff(t *testing.T) {
	if got := UsagePref(); got != UsageOff {
		t.Fatalf("UsagePref() default = %q, want %q", got, UsageOff)
	}
}

// TestConnectedDefaultsOffWithoutCredentials pins the security-sensitive default:
// a fresh workspace (no stored provider key, no account OAuth, no explicit free-tier
// opt-in) must NOT be considered "connected" — headlessAgentAvailable
// (chat_providers.go) gates assistant chat's opencode backend on this, and it used to
// gate on Available() alone (binary-on-PATH), which let assistant chat silently reach
// opencode's zero-auth free tier even when the user configured nothing.
func TestConnectedDefaultsOffWithoutCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Connected also probes the shared serve daemon for a Console OAuth account.
	// HOME isolates the key store but not that loopback process, so a developer's real
	// login would make this supposedly fresh workspace connected. Use the same empty
	// daemon fixture as the OAuth contract tests.
	_ = newFakeDaemon(t)
	orig := UsagePref
	defer func() { UsagePref = orig }()

	UsagePref = func() string { return UsageZen }
	if Connected() {
		t.Fatal("connected=true with no key, no OAuth and no free-tier opt-in — the default must be free tier OFF")
	}

	UsagePref = func() string { return UsageFree }
	if !Connected() {
		t.Fatal("connected=false after an explicit opt-in to the free tier")
	}
	UsagePref = func() string { return UsageZen }

	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.Opencode = map[string]string{"ANTHROPIC_API_KEY": "sk-an"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if !Connected() {
		t.Fatal("connected=false although a provider key is stored")
	}
}

// TestUsageOffOverridesStoredCredentials pins the "off" route as a hard lock: even
// with a provider key AND a completed OAuth login already present, selecting off
// must force Connected()=false and env() must not leak any key — a security policy
// that flips this to off must be able to trust it regardless of what was configured
// before (or gets pasted in later without switching the route back).
func TestUsageOffOverridesStoredCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := secrets.Load()
	if err != nil {
		t.Fatal(err)
	}
	s.Opencode = map[string]string{"OPENCODE_API_KEY": "sk-oc", "ANTHROPIC_API_KEY": "sk-an"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	orig := UsagePref
	defer func() { UsagePref = orig }()
	UsagePref = func() string { return UsageOff }

	if Connected() {
		t.Fatal("connected=true on off — off must override even when a key is present")
	}
	if got := env(); len(got) != 0 {
		t.Errorf("env returned keys on off: %v", redactEnv(got))
	}
	if got := Catalog([]string{"opencode/nemotron-3-ultra-free", "anthropic/claude-x"}, UsageOff); len(got) != 0 {
		t.Errorf("Catalog is not empty on off: %v", got)
	}
}

// redactEnv keeps assertions readable without printing key material.
func redactEnv(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name, _, _ := strings.Cut(e, "=")
		out = append(out, name+"=…")
	}
	return out
}
