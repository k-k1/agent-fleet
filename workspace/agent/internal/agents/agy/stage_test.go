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
