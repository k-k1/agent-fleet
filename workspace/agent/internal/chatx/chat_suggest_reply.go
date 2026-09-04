package chatx

// The chat side of reply suggestions v2. The persona, model and formatting
// (cleanSuggestedReplies) come from session_suggest_reply.go unchanged; only the context is
// built from the ChatMessage slice (the last few, minus report and empty ones). Like
// chat_title.go this is preview only and never rewrites the conversation.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

// ChatReplySuggestPrompt asks for reply candidates using the most recent messages (a
// trailing window) as context. report and notice are not topics of the conversation (a
// notice body is only the source-language fallback of the display catalogue, ADR 0033), so
// they stay out of the window.
// Cutting that window (folding, character budget, line-aligned tail) is shared with the
// session version. In chat one message is already one utterance, so folding is mostly a
// no-op, but the budget window behaves identically.
func ChatReplySuggestPrompt(msgs []ChatMessage, lang string) string {
	real := make([]ReplyMsg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "report" || m.Role == "notice" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		real = append(real, ReplyMsg{Role: m.Role, Text: m.Content})
	}
	var b strings.Builder
	b.WriteString(replySuggestInstructions(lang, replyCounterpartChat))
	b.WriteString(replySuggestLogHeader(lang))
	replySuggestWindow(&b, real)
	return b.String()
}

func runChatReplySuggestLLM(ctx context.Context, msgs []ChatMessage) ([]string, error) {
	lang := uiprefs.Locale()
	reply, err := OneShotHeadless(ctx, OneShotShort, replySuggestPersona(lang), ChatReplySuggestPrompt(msgs, lang), replySuggestModel())
	if err != nil {
		return nil, fmt.Errorf("chat reply suggestion failed: %w", err)
	}
	return cleanSuggestedReplies(reply), nil
}

// HandleChatSuggestReplies is preview only. The Console's chat sparkle button calls it and
// merges the returned candidates into the chip row above the composer.
func HandleChatSuggestReplies(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatConversationNotFnd, "invalid conversation id")
		return
	}
	if !replySuggestEnabled() {
		httpx.WriteErr(w, http.StatusBadRequest, "feature_disabled", "reply suggestion is turned off")
		return
	}
	c, err := LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	if len(c.Messages) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, "no_content", "not enough conversation yet to suggest replies")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), replySuggestTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureSuggestChat, Trigger: usagex.TriggerManual, Ref: c.ID})
	reps, err := runChatReplySuggestLLM(ctx, c.Messages)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "reply suggestion failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestions": reps})
}
