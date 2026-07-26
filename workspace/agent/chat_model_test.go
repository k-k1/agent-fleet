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
		{session.KindClaude, "", "sonnet"},
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

func TestResolveChatModelRecommended(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"claude":"recommended","codex":"recommended","cursor":"recommended"}}`)
	tests := map[string]string{
		session.KindClaude: "sonnet",
		session.KindCodex:  defaultCodexChatModel,
		session.KindCursor: "",
	}
	for kind, want := range tests {
		if got := resolveChatModel(kind, ""); got != want {
			t.Errorf("%s recommendation = %q, want %q", kind, got, want)
		}
	}
}

func TestRecommendedCatalogModelRequiresExactEntitlement(t *testing.T) {
	const target = "opencode-go/glm-5.2"
	if got := recommendedCatalogModel([]string{"opencode/glm-5.2", target}, target, "fallback"); got != target {
		t.Fatalf("Go entitlement = %q", got)
	}
	if got := recommendedCatalogModel([]string{"opencode/glm-5.2"}, target, "fallback"); got != "fallback" {
		t.Fatalf("Zen twin must not prove Go entitlement: %q", got)
	}
}
