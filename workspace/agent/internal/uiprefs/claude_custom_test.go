package uiprefs

import (
	"slices"
	"testing"
)

// The same input table as settings.dom.test.tsx's normalizeClaudeCustomModels case: the two
// sides must keep the same ids, or a model shows in the Console but not in the Agent's list.
func TestClaudeCustomModelsMatchesTheConsoleRule(t *testing.T) {
	writePrefs(t, map[string]any{"claudeCustomModels": []any{
		" claude-opus-4-8 ", "CLAUDE-OPUS-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]", "opus", "bad model", 42, "",
		"claude-", "claude-_x", "claude-[1m]", "claude-opus 4",
	}})
	want := []string{"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]"}
	if got := ClaudeCustomModels(); !slices.Equal(got, want) {
		t.Fatalf("ClaudeCustomModels() = %q, want %q", got, want)
	}
}
