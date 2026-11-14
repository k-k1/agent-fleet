package credbind

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func freshHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// The bindings are stable for one credential and Workspace, change with another key, account,
// endpoint or generation, carry no input, and cannot be rebuilt by hashing candidates without the
// Workspace's key (ADR 0138 D5b).
func TestBindings(t *testing.T) {
	freshHome(t)
	k1 := Key("ANTHROPIC_API_KEY", "1234")
	if k1 == "" || k1 != Key("ANTHROPIC_API_KEY", " 1234 ") || k1 == Key("ANTHROPIC_API_KEY", "1235") ||
		!strings.HasPrefix(k1, "ANTHROPIC_API_KEY@") || len(k1) != len("ANTHROPIC_API_KEY@")+16 {
		t.Fatalf("Key: %q", k1)
	}
	if Key("codex-api-key", "1234") == k1 {
		t.Error("two slots holding one key must stay apart")
	}
	// A candidate search over plain digests of every four-digit key finds nothing.
	digest := strings.TrimPrefix(k1, "ANTHROPIC_API_KEY@")
	for i := 0; i < 10000; i++ {
		c := []byte{byte('0' + i/1000), byte('0' + i/100%10), byte('0' + i/10%10), byte('0' + i%10)}
		h := sha256.Sum256(c)
		if hex.EncodeToString(h[:])[:16] == digest {
			t.Fatalf("the binding is a plain digest of %q", c)
		}
	}
	a := Account("agy-login", "a@example.com")
	if a == "" || strings.Contains(a, "example") || a == Account("agy-login", "b@example.com") || a == Account("cursor-login", "a@example.com") {
		t.Errorf("Account: %q", a)
	}
	e := Endpoint("member-llamacpp", "http://gpu.example:8080/", "k")
	if e != Endpoint("member-llamacpp", "http://gpu.example:8080", "k") || e == Endpoint("member-llamacpp", "http://gpu.example:8080", "k2") ||
		e == Endpoint("member-llamacpp", "http://other.example:8080", "k") || strings.Contains(e, "gpu") {
		t.Errorf("Endpoint: %q", e)
	}
	if Key("X", " ") != "" || Account("x", "") != "" || Endpoint("x", "", "k") != "" {
		t.Error("an empty credential has no binding")
	}
	// Another Workspace (another key) gives other bindings for the same credential.
	freshHome(t)
	if Key("ANTHROPIC_API_KEY", "1234") == k1 {
		t.Error("the binding does not depend on the Workspace's key")
	}
}

// The generation fallback: stable until the Agent bumps it, per login.
func TestGeneration(t *testing.T) {
	freshHome(t)
	g := Generation("muse-login")
	if g == "" || g != Generation("muse-login") || g != AccountOrGeneration("muse-login", "") {
		t.Fatalf("Generation: %q", g)
	}
	Bump("codex-login-other")
	if Generation("muse-login") != g {
		t.Error("bumping another login moved this one")
	}
	Bump("muse-login")
	if n := Generation("muse-login"); n == g || n == "" {
		t.Errorf("a bump kept the binding: %q", n)
	}
	if AccountOrGeneration("muse-login", "a@example.com") != Account("muse-login", "a@example.com") {
		t.Error("an account id wins over the generation")
	}
}

// The key is created once, private, and reused; one that cannot be read leaves bindings unproven
// rather than weaker.
func TestKeyFile(t *testing.T) {
	freshHome(t)
	first := Key("S", "v")
	p := filepath.Join(paths.AgentConfigDir(), keyFile)
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	keyMu.Lock()
	keyVal = nil
	keyMu.Unlock()
	if Key("S", "v") != first {
		t.Error("the key was not reused after a reload")
	}
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyMu.Lock()
	keyVal = nil
	keyMu.Unlock()
	if b := Key("S", "v"); b != "" {
		t.Errorf("a corrupt key still produced %q", b)
	}
}

// A generation state that cannot be trusted serves no binding rather than generation zero, and a
// Bump rebuilds it under a new epoch, so no login is handed a value an earlier state used.
func TestGenerationFailurePaths(t *testing.T) {
	freshHome(t)
	p := filepath.Join(paths.AgentConfigDir(), genFile)
	g0 := Generation("muse-login")
	other0 := Generation("agy-login")
	Bump("muse-login")
	g1 := Generation("muse-login")
	if g1 == "" || g1 == g0 || Generation("agy-login") != other0 {
		t.Fatalf("bump: %q -> %q, agy %q", g0, g1, Generation("agy-login"))
	}
	for name, body := range map[string]string{"malformed": "{", "null": "null", "no logins": `{"epoch":"x"}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if g := Generation("muse-login"); g != "" {
			t.Errorf("%s state: Generation = %q, want unproven", name, g)
		}
	}
	// Recovery: a Bump rebuilds the state; neither login gets back any earlier binding.
	Bump("muse-login")
	for _, g := range []string{Generation("muse-login"), Generation("agy-login")} {
		if g == "" || g == g0 || g == g1 || g == other0 {
			t.Errorf("after a rebuild: %q reuses an earlier binding (%q %q %q)", g, g0, g1, other0)
		}
	}
	// A Bump that cannot persist leaves the login unproven, never on its replaced binding, and
	// removes the state so a restart (the in-memory mark gone) starts a fresh epoch.
	beforeFail := Generation("cursor-login")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	Bump("cursor-login")
	if g := Generation("cursor-login"); g != "" {
		t.Errorf("after a failed bump: %q, want unproven", g)
	}
	genMu.Lock()
	genPoisoned = map[string]bool{} // a restart
	genMu.Unlock()
	if g := Generation("cursor-login"); g == "" || g == beforeFail {
		t.Errorf("after a failed bump and a restart: %q, want a fresh binding (old %q)", g, beforeFail)
	}
	Bump("cursor-login")
	if g := Generation("cursor-login"); g == "" {
		t.Error("a successful bump did not clear the failure")
	}
	t.Cleanup(func() { genMu.Lock(); genPoisoned = map[string]bool{}; genMu.Unlock() })
}

// A lost state file never reissues a predecessor's binding: the next read starts and persists a
// fresh epoch, which then holds across a reload.
func TestGenerationAfterTheStateIsLost(t *testing.T) {
	freshHome(t)
	p := filepath.Join(paths.AgentConfigDir(), genFile)
	g0 := Generation("muse-login")
	Bump("muse-login")
	g1 := Generation("muse-login")
	if g1 == "" || g1 == g0 {
		t.Fatalf("bump: %q -> %q", g0, g1)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	g2 := Generation("muse-login")
	if g2 == "" || g2 == g0 || g2 == g1 {
		t.Errorf("after the state was lost: %q, reissues %q or %q", g2, g0, g1)
	}
	ResetForTest() // a reload of the binding key; the state is read from disk again
	if g := Generation("muse-login"); g != g2 {
		t.Errorf("after a reload: %q, want the persisted %q", g, g2)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("the fresh epoch was not persisted: %v", err)
	}
}
