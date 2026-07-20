package agy

import (
	"encoding/json"
	"os"
	"testing"
)

func readSettingsRaw(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	return m
}

func trusted(t *testing.T) []string {
	t.Helper()
	var out []string
	list, _ := readSettingsRaw(t)["trustedWorkspaces"].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestEnsureWorkspaceTrustedCreatesAndIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	EnsureWorkspaceTrusted("/home/dev/repos/proj")
	EnsureWorkspaceTrusted("/home/dev/repos/proj")
	if got := trusted(t); len(got) != 1 || got[0] != "/home/dev/repos/proj" {
		t.Fatalf("got %v", got)
	}
}

func TestEnsureWorkspaceTrustedPreservesOtherKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeSettings(map[string]any{
		"enableTelemetry":   false,
		"trustedWorkspaces": []any{"/w1"},
	})
	EnsureWorkspaceTrusted("/w2")
	m := readSettingsRaw(t)
	if v, ok := m["enableTelemetry"].(bool); !ok || v {
		t.Fatalf("enableTelemetry lost: %v", m)
	}
	if got := trusted(t); len(got) != 2 || got[0] != "/w1" || got[1] != "/w2" {
		t.Fatalf("got %v", got)
	}
}

func TestEnforceTelemetryOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// No settings at all → creates the file with the opt-out pinned.
	enforceTelemetryOff()
	if v, ok := readSettingsRaw(t)["enableTelemetry"].(bool); !ok || v {
		t.Fatal("enableTelemetry not pinned false")
	}
	// Flipped on (missed toggle) → forced back off, other keys preserved.
	writeSettings(map[string]any{"enableTelemetry": true, "trustedWorkspaces": []any{"/w"}})
	enforceTelemetryOff()
	m := readSettingsRaw(t)
	if v, ok := m["enableTelemetry"].(bool); !ok || v {
		t.Fatal("enableTelemetry not forced off")
	}
	if got := trusted(t); len(got) != 1 || got[0] != "/w" {
		t.Fatalf("trustedWorkspaces lost: %v", got)
	}
}
