package chatx

// The route that switches the backend (CLI) mid-conversation (docs/log/19). The switch itself
// only replaces the pin, but unless (1) the model is resolved again against the new CLI and
// (2) history the new backend does not know is replayed on the next send, it breaks quietly:
// another vendor's model id is handed to a different CLI, or the new agent answers without
// knowing the conversation.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func patchConv(t *testing.T, id string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/chat/conversations/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	HandleChatPatch(rec, req)
	return rec
}

// TestChatModelForResolvesPerActualBackend: a conversation carries a single Model, resolved
// against the agent it was created with, so a turn run by another backend resolves it again
// from that CLI's settings.
func TestChatModelForResolvesPerActualBackend(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom","claude":"opus"}}`)
	c := &ChatConversation{Agent: session.KindClaude, Model: "sonnet"}

	if got := chatModelFor(c, session.KindClaude); got != "sonnet" {
		t.Fatalf("pinned backend model = %q, want the conversation's own pin", got)
	}
	// A turn run by codex after an auth fallback or a mid-conversation switch: claude's
	// "sonnet" must not be passed to -m.
	if got := chatModelFor(c, session.KindCodex); got != "gpt-custom" {
		t.Fatalf("foreign backend model = %q, want the codex row from the settings", got)
	}
	// The model reserved for automatic turns (claude only) still wins over everything.
	c.modelOverride = "haiku"
	if got := chatModelFor(c, session.KindClaude); got != "haiku" {
		t.Fatalf("override = %q", got)
	}
	c.modelOverride = ""
	// An old record with no Agent passes through as before; there is nothing to judge the kind
	// from.
	legacy := &ChatConversation{Model: "gpt-5.6"}
	if got := chatModelFor(legacy, session.KindCodex); got != "gpt-5.6" {
		t.Fatalf("legacy conversation model = %q", got)
	}
}

// TestSwitchChatAgentRepinsAndKeepsResumeHandles: a switch replaces the pin and the model and
// leaves the per-backend resume handles and cursors alone, so switching back can carry on with
// that CLI's native session.
func TestSwitchChatAgentRepinsAndKeepsResumeHandles(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom"}}`)
	c := &ChatConversation{
		Agent: session.KindClaude, ActiveAgent: session.KindClaude, Model: "sonnet",
		ClaudeSessionID: "claude-sess", ClaudeMessageCursor: 2,
		Messages: []ChatMessage{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: "a1", Agent: session.KindClaude},
		},
	}
	switchChatAgent(c, session.KindCodex)

	if c.Agent != session.KindCodex || c.ActiveAgent != session.KindCodex {
		t.Fatalf("agent = %q / active = %q, want codex", c.Agent, c.ActiveAgent)
	}
	if c.Model != "gpt-custom" {
		t.Fatalf("model = %q, want the codex row (a carried-over claude model would break -m)", c.Model)
	}
	if c.ClaudeSessionID != "claude-sess" || c.ClaudeMessageCursor != 2 {
		t.Fatalf("claude resume handle lost: %q / %d", c.ClaudeSessionID, c.ClaudeMessageCursor)
	}
	last := c.Messages[len(c.Messages)-1]
	if last.Role != "notice" || last.NoticeKey != noticeKeyAgentSwitched || last.NoticeArgs["agent"] != session.KindCodex {
		t.Fatalf("switch notice = %+v", last)
	}
	if strings.TrimSpace(last.Content) == "" {
		t.Fatal("notice has no source-language fallback content")
	}

	// Re-selecting the current agent does nothing, not even append a notice.
	n := len(c.Messages)
	switchChatAgent(c, session.KindCodex)
	if len(c.Messages) != n {
		t.Fatalf("re-selecting the current agent appended %d message(s)", len(c.Messages)-n)
	}
}

// TestSwitchedAgentGetsHistoryOnNextTurn: on the first send after a switch, the history the
// new backend does not know yet is replayed (the same route as the auth fallback).
func TestSwitchedAgentGetsHistoryOnNextTurn(t *testing.T) {
	c := &ChatConversation{
		Agent: session.KindClaude, ClaudeSessionID: "claude-sess",
		Messages: []ChatMessage{
			{Role: "user", Content: "前の依頼"},
			{Role: "assistant", Content: "前の回答", Agent: session.KindClaude},
		},
	}
	switchChatAgent(c, session.KindCodex)
	c.Messages = append(c.Messages, ChatMessage{Role: "user", Content: "切替後の依頼"})

	got := SyncProviderPrompt(c, session.KindCodex, "切替後の依頼", len(c.Messages)-1)
	for _, want := range []string{"前の依頼", "前の回答", "切替後の依頼"} {
		if !strings.Contains(got, want) {
			t.Fatalf("synced prompt = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "切替後の依頼") != 1 {
		t.Fatalf("current request duplicated: %q", got)
	}
}

func TestHandleChatPatchAgent(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom"}}`)
	conv := &ChatConversation{
		ID: RandUUID(), Slug: NewConvSlug(), Title: "元のタイトル",
		Agent: session.KindClaude, Model: "sonnet",
		Messages: []ChatMessage{{Role: "user", Content: "u1"}},
	}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}

	rec := patchConv(t, conv.ID, `{"agent":"codex"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	c, err := LoadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent != session.KindCodex || c.Model != "gpt-custom" {
		t.Fatalf("persisted agent/model = %q / %q", c.Agent, c.Model)
	}
	if c.Title != "元のタイトル" {
		t.Fatalf("title clobbered by an agent-only patch: %q", c.Title)
	}

	// Renaming behaves as before; existing clients send {title} alone.
	if rec := patchConv(t, conv.ID, `{"title":"新しいタイトル"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if c, _ = LoadConv(conv.ID); c.Title != "新しいタイトル" || c.Agent != session.KindCodex {
		t.Fatalf("rename result = %q / agent %q (agent must survive a title-only patch)", c.Title, c.Agent)
	}

	// A kind that headless chat does not carry is rejected, tmux-only kinds included.
	for _, body := range []string{`{"agent":"kiro"}`, `{"agent":"shell"}`, `{"agent":""}`} {
		if rec := patchConv(t, conv.ID, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("patch %s status = %d, want 400", body, rec.Code)
		}
	}
	if rec := patchConv(t, conv.ID, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d, want 400", rec.Code)
	}
}

// TestHandleChatPatchAgentRejectsWhileTurnInFlight: replacing the pin behind a running turn
// leaves the stored content disagreeing with the provider actually executing. Hanging for
// minutes on a lock is no better, so, as with deletion, it is refused up front with a 409.
func TestHandleChatPatchAgentRejectsWhileTurnInFlight(t *testing.T) {
	withTempHome(t)
	conv := &ChatConversation{ID: RandUUID(), Slug: NewConvSlug(), Agent: session.KindClaude}
	if err := SaveConv(conv); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	deregister := RegisterLiveTurn(conv.ID, cancel)
	defer deregister()

	rec := patchConv(t, conv.ID, `{"agent":"codex"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "conversation_busy" {
		t.Fatalf("error code = %q, want conversation_busy", body.Error.Code)
	}
	if c, err := LoadConv(conv.ID); err != nil || c.Agent != session.KindClaude {
		t.Fatalf("agent switched under a live turn: %v / %+v", err, c)
	}
}
