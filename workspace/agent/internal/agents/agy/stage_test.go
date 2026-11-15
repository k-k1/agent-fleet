package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The staging settings must trust the login dir (a trust prompt otherwise appears in a fresh
// home) and keep telemetry off, and land where agy reads them under that home.
func TestSeedStagingSettings(t *testing.T) {
	stage := t.TempDir()
	if err := seedStagingSettings(stage, "/work/login-flow"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(stage, ".gemini", "antigravity-cli", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if tw, _ := m["trustedWorkspaces"].([]any); len(tw) != 1 || tw[0] != "/work/login-flow" || m["enableTelemetry"] != false {
		t.Fatalf("settings = %v", m)
	}
}

// tokenPathIn names the same file tokenPath does, under another home.
func TestTokenPathInMatchesTokenPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if tokenPathIn(home) != tokenPath() {
		t.Fatalf("tokenPathIn = %q, tokenPath = %q", tokenPathIn(home), tokenPath())
	}
}

func writeTestFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A first connection also takes the staged onboarding state, or its sessions would open on the
// onboarding screens; a home that has its own keeps it.
func TestInstallLoginOnboardingState(t *testing.T) {
	stage := t.TempDir()
	writeTestFile(t, tokenPathIn(stage), "new-token")
	writeTestFile(t, onboardingState(stage), "post_onboarding: true")

	first := t.TempDir()
	if err := installLogin(stage, first); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(onboardingState(first)); string(b) != "post_onboarding: true" {
		t.Fatalf("first connection onboarding state = %q", b)
	}

	again := t.TempDir()
	writeTestFile(t, onboardingState(again), "mine")
	writeTestFile(t, tokenPathIn(again), "old-token")
	if err := installLogin(stage, again); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(onboardingState(again)); string(b) != "mine" {
		t.Fatalf("an existing onboarding state was replaced: %q", b)
	}
	if b, _ := os.ReadFile(tokenPathIn(again)); string(b) != "new-token" {
		t.Fatalf("token = %q", b)
	}
}
