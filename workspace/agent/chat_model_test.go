package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestResolveChatModel(t *testing.T) {
	tests := []struct {
		agent string
		model string
		want  string
	}{
		{session.KindCodex, "", defaultCodexChatModel},
		{session.KindCodex, "  gpt-5.6  ", "gpt-5.6"},
		{session.KindClaude, "", ""},
		{session.KindOpencode, "", ""},
	}
	for _, tt := range tests {
		if got := resolveChatModel(tt.agent, tt.model); got != tt.want {
			t.Errorf("resolveChatModel(%q, %q) = %q, want %q", tt.agent, tt.model, got, tt.want)
		}
	}
}
