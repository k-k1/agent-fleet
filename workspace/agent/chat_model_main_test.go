package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestTurnModelRecordedPerMessage(t *testing.T) {
	withTempHome(t)
	stubChatProvider(t, session.KindClaude, mainStubProvider{reply: "了解", model: "claude-sonnet-5-20260501"})
	conv := &chatx.ChatConversation{
		ID: chatx.RandUUID(), Agent: session.KindClaude, Model: "sonnet",
		Tools: assistants.ToolsAFWrite, AssistantID: "operator",
	}
	if err := chatx.SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	if _, err := runOperatorTurn(conv.ID, "状況は?"); err != nil {
		t.Fatal(err)
	}
	c, err := chatx.LoadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Messages[len(c.Messages)-1].Model; got != "claude-sonnet-5-20260501" {
		// The CLI's own id, not the "sonnet" alias we passed on the command line.
		t.Fatalf("assistant model = %q, want the model the CLI reported", got)
	}
	if c.TurnModel != "" { // scratch state, never persisted
		t.Fatalf("turnModel leaked into the stored conversation: %q", c.TurnModel)
	}
}

func TestTurnModelBlankWhenUnknown(t *testing.T) {
	withTempHome(t)
	stubChatProvider(t, session.KindClaude, mainStubProvider{reply: "了解"})
	conv := &chatx.ChatConversation{
		ID: chatx.RandUUID(), Agent: session.KindClaude, Model: "sonnet",
		Tools: assistants.ToolsAFWrite, AssistantID: "operator",
	}
	if err := chatx.SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	if _, err := runOperatorTurn(conv.ID, "状況は?"); err != nil {
		t.Fatal(err)
	}
	c, err := chatx.LoadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Messages[len(c.Messages)-1].Model; got != "" {
		t.Fatalf("assistant model = %q, want empty (must not guess from conversation.Model)", got)
	}
}
