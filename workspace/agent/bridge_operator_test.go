package main

import (
	"context"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"strings"
	"testing"
)

// fakeChatProv is a stub chatProvider that returns a canned reply and captures the
// prompt it was handed — enough to exercise runOperatorTurn without a real CLI.
type fakeChatProv struct {
	reply  string
	prompt *string
}

func (f fakeChatProv) Send(_ context.Context, _ *chatConversation, prompt string) (string, error) {
	if f.prompt != nil {
		*f.prompt = prompt
	}
	return f.reply, nil
}

// stubChatProvider swaps a backend in the provider registry for the test's duration.
func stubChatProvider(t *testing.T, kind string, p chatProvider) {
	t.Helper()
	old, had := chatProviders[kind]
	chatProviders[kind] = p
	t.Cleanup(func() {
		if had {
			chatProviders[kind] = old
		} else {
			delete(chatProviders, kind)
		}
	})
}

// TestRunOperatorTurn: an inbound operator message appends a user turn, runs the
// provider, records the assistant reply, resets the unattended auto-turn budget, and
// returns the reply text for the receiver to post back.
func TestRunOperatorTurn(t *testing.T) {
	withTempHome(t)
	var gotPrompt string
	stubChatProvider(t, "claude", fakeChatProv{reply: "フリートは2件稼働中です", prompt: &gotPrompt})

	conv := &chatConversation{
		ID: randUUID(), Agent: "claude", Tools: assistants.ToolsAFWrite, AssistantID: "operator",
		AutoTurns: 3, // a prior unattended run — a real user message must reset it
		Messages:  []chatMessage{},
	}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}

	reply, err := runOperatorTurn(conv.ID, "  稼働状況は?  ")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "フリートは2件稼働中です" {
		t.Fatalf("reply = %q", reply)
	}
	if !strings.Contains(gotPrompt, "稼働状況は?") {
		t.Fatalf("provider prompt missing the (trimmed) user text: %q", gotPrompt)
	}

	c, err := loadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Messages) != 2 || c.Messages[0].Role != "user" || c.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", c.Messages)
	}
	if c.Messages[0].Content != "稼働状況は?" {
		t.Fatalf("user turn = %q (should be trimmed)", c.Messages[0].Content)
	}
	if c.Messages[1].Content != "フリートは2件稼働中です" {
		t.Fatalf("assistant turn = %q", c.Messages[1].Content)
	}
	if c.AutoTurns != 0 {
		t.Fatalf("AutoTurns = %d, want 0 (reset by a user message)", c.AutoTurns)
	}
}

// TestRunOperatorTurnMissingConv returns a localized reason (never panics) when the
// operator conversation is gone.
func TestRunOperatorTurnMissingConv(t *testing.T) {
	withTempHome(t)
	reply, err := runOperatorTurn(randUUID(), "hi")
	if err == nil {
		t.Fatal("expected an error for a missing conversation")
	}
	if !strings.Contains(reply, "見つかりません") {
		t.Fatalf("reason = %q", reply)
	}
}

// TestCreateOperatorConversation snapshots the built-in operator assistant so the
// conversation carries af_write (a bare conversation would attach no MCP tools).
func TestCreateOperatorConversation(t *testing.T) {
	withTempHome(t)
	id, err := createOperatorConversation()
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadConv(id)
	if err != nil {
		t.Fatal(err)
	}
	if c.AssistantID != "operator" {
		t.Fatalf("assistant id = %q", c.AssistantID)
	}
	if !c.AFWriteEnabled() {
		t.Fatalf("operator conversation must grant af_write, got tools=%q", c.Tools)
	}
	if strings.TrimSpace(c.Persona) == "" {
		t.Fatal("persona not snapshotted")
	}
}

// TestMaybePushOperatorReply is a no-op (no panic, no send) when the conversation is
// not the bridge operator conversation, or the reply is empty.
func TestMaybePushOperatorReply(t *testing.T) {
	withTempHome(t)
	maybePushOperatorReply("some-conv", "")        // empty → nothing
	maybePushOperatorReply("some-conv", "a reply") // no operator state → nothing
}
