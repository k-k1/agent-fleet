package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeUIPrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(homeDir(), ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantAgentOrderPref(t *testing.T) {
	tests := []struct {
		name  string
		prefs string
		want  []string
	}{
		{"unset", `{}`, []string{"claude", "codex", "opencode", "cursor", "agy"}},
		{"full order", `{"assistantAgentOrder":["agy","opencode","codex","claude"]}`,
			[]string{"agy", "opencode", "codex", "claude", "cursor"}},
		{"partial order appends the rest in default order",
			`{"assistantAgentOrder":["opencode"]}`,
			[]string{"opencode", "claude", "codex", "cursor", "agy"}},
		{"junk and dupes dropped",
			`{"assistantAgentOrder":["gemini","codex",42,"codex"]}`,
			[]string{"codex", "claude", "opencode", "cursor", "agy"}},
		{"legacy pin promotes to front", `{"assistantAgent":"opencode"}`,
			[]string{"opencode", "claude", "codex", "cursor", "agy"}},
		{"legacy auto falls through to default", `{"assistantAgent":"auto"}`,
			[]string{"claude", "codex", "opencode", "cursor", "agy"}},
		{"order wins over legacy pin",
			`{"assistantAgent":"opencode","assistantAgentOrder":["codex"]}`,
			[]string{"codex", "claude", "opencode", "cursor", "agy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeUIPrefs(t, tt.prefs)
			if got := assistantAgentOrderPref(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAssistantModelPrefs(t *testing.T) {
	writeUIPrefs(t, `{
		"assistantModels":{"opencode":"opencode-go/glm-5.2"},
		"assistantUtilityModels":{"opencode":"","claude":"haiku"}
	}`)
	if got, ok := assistantChatModelPref("opencode"); !ok || got != "opencode-go/glm-5.2" {
		t.Fatalf("chat model = %q, %v", got, ok)
	}
	if got, ok := assistantUtilityModelPref("opencode"); !ok || got != "" {
		t.Fatalf("explicit utility default = %q, %v", got, ok)
	}
	if _, ok := assistantUtilityModelPref("codex"); ok {
		t.Fatal("missing backend must remain distinguishable from explicit default")
	}
}
