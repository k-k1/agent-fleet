package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureStatusHooksShape pins the session-state hook wiring: the simple events, the
// SessionStart→boot reset, and the catch-all PostToolUse→working heartbeat that keeps a
// long turn from false-idling when the working status file is lost mid-turn.
func TestEnsureStatusHooksShape(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	EnsureStatusHooks()
	hooks := readHooks(t, dir)

	// Simple matcher-less events carry the right state command.
	for event, want := range map[string]string{
		"UserPromptSubmit": "session-status working",
		"Stop":             "session-status idle",
		"SessionStart":     "session-status boot",
		"Notification":     "session-status permission",
	} {
		if b, _ := json.Marshal(hooks[event]); !strings.Contains(string(b), want) {
			t.Errorf("hooks[%q] = %s, want it to contain %q", event, b, want)
		}
	}

	// PostToolUse is a single catch-all (empty matcher) → working heartbeat.
	post, _ := json.Marshal(hooks["PostToolUse"])
	if !strings.Contains(string(post), `"matcher":""`) || !strings.Contains(string(post), "session-status working") {
		t.Errorf("PostToolUse = %s, want a catch-all (matcher \"\") → working", post)
	}
	if strings.Contains(string(post), "AskUserQuestion") || strings.Contains(string(post), "ExitPlanMode") {
		t.Errorf("PostToolUse should have migrated off the per-tool matchers: %s", post)
	}
}

// TestEnsureStatusHooksIdempotent confirms a second call is a no-op (stable settings) —
// EnsureStatusHooks runs on every agent startup and must not churn settings.json.
func TestEnsureStatusHooksIdempotent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	EnsureStatusHooks()
	first, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	EnsureStatusHooks()
	second, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("settings.json changed on the second EnsureStatusHooks call:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestEnsureStatusHooksMigratesOldPostToolUse checks that settings carrying the legacy
// two-matcher PostToolUse (AskUserQuestion/ExitPlanMode) get rewritten to the catch-all.
func TestEnsureStatusHooksMigratesOldPostToolUse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	seed := map[string]any{"hooks": map[string]any{
		"PostToolUse": []any{
			map[string]any{"matcher": "AskUserQuestion", "hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("working")}}},
			map[string]any{"matcher": "ExitPlanMode", "hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("working")}}},
		},
	}}
	b, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	EnsureStatusHooks()
	post, _ := json.Marshal(readHooks(t, dir)["PostToolUse"])
	if strings.Contains(string(post), "AskUserQuestion") || !strings.Contains(string(post), `"matcher":""`) {
		t.Errorf("old PostToolUse not migrated to catch-all: %s", post)
	}
}

func readHooks(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	h, ok := m["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks in settings: %s", b)
	}
	return h
}
