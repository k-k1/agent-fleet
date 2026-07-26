package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestResolveChatModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		agent string
		model string
		want  string
	}{
		{session.KindCodex, "", defaultCodexChatModel},
		{session.KindCodex, "  gpt-5.6  ", "gpt-5.6"},
		{session.KindClaude, "", ""},
		{session.KindOpencode, "", defaultOpencodeChatModel},
		{session.KindOpencode, "  anthropic/claude-opus-4-6  ", "anthropic/claude-opus-4-6"},
		{session.KindAgy, "", defaultAgyChatModel},
		{session.KindAgy, "  Gemini 3.1 Pro (High)  ", "Gemini 3.1 Pro (High)"},
	}
	for _, tt := range tests {
		if got := resolveChatModel(tt.agent, tt.model); got != tt.want {
			t.Errorf("resolveChatModel(%q, %q) = %q, want %q", tt.agent, tt.model, got, tt.want)
		}
	}
}

func TestResolveChatModelUsesAssistantPreference(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom","opencode":""}}`)
	if got := resolveChatModel(session.KindCodex, ""); got != "gpt-custom" {
		t.Fatalf("codex preference = %q", got)
	}
	if got := resolveChatModel(session.KindOpencode, ""); got != "" {
		t.Fatalf("explicit CLI default = %q, want empty", got)
	}
	if got := resolveChatModel(session.KindCodex, "explicit"); got != "explicit" {
		t.Fatalf("conversation pin must win, got %q", got)
	}
}
