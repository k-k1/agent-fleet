package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
)

// TestHandleAssistantTurnTagsSchedule: a scheduled fire stores its prompt as a user message
// attributed to the schedule, so the Console does not offer it back as the member's own input
// in the composer's ↑ history.
func TestHandleAssistantTurnTagsSchedule(t *testing.T) {
	withTempHome(t)
	stubChatProvider(t, "claude", fakeChatProv{reply: "朝の点検は異常なし"})
	conv := &chatx.ChatConversation{
		ID: chatx.RandUUID(), Agent: "claude", Tools: assistants.ToolsAFWrite, AssistantID: "operator",
		Messages: []chatx.ChatMessage{},
	}
	if err := chatx.SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	body := `{"conv":"` + conv.ID + `","prompt":"朝の点検"}`
	rec := httptest.NewRecorder()
	handleAssistantTurn(rec, httptest.NewRequest(http.MethodPost, "/assistant-turns", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}

	c, err := chatx.LoadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Messages) != 2 || c.Messages[0].Role != "user" || c.Messages[0].Source != "schedule" {
		t.Fatalf("messages = %+v, want a user turn with source=schedule first", c.Messages)
	}
}
