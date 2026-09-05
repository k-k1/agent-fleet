package chatx

// AI title suggestion for assistant chats (preview only). Reuses session_title.go's
// oneShotHeadless plumbing, persona and cleaning, but a chat conversation already is a
// structured chatMessage slice, so no conversion to transcript.Turn is needed (simpler than a
// session, which also has sidechain and tool-only turns). There is no equivalent of the
// session's automatic suggestion banner (SuggestedTitle pending -> accept/reject): this serves
// only the rename dialog's "ask the AI" (AIに提案してもらう) button, and never writes conv.Title.

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

const (
	chatTitleHeadMsgs    = 2
	chatTitleTailMsgs    = 6
	chatTitlePerMsgRunes = 400
)

// chatTitleSuggestPrompt mirrors titleSuggestPrompt (session_title.go): opening + most
// recent real exchanges, weighting the recent topic.
func ChatTitleSuggestPrompt(msgs []ChatMessage, lang string) string {
	var b strings.Builder
	b.WriteString(titleSuggestInstructions(lang)) // shared with session_title.go (preamble plus the no-label counter-examples)
	writeChatTitleWindow(&b, msgs)
	b.WriteString(titleSuggestFooter(lang)) // the trailer that stops the model continuing the conversation is shared too
	return b.String()
}

// writeChatTitleWindow appends the opening + most recent non-empty user/assistant
// messages (head/tail windowing, per-message length cap). Report cards (role=="report")
// are skipped — they're session-origin, not conversation topic — and so are system
// notices (role=="notice"), which are UI housekeeping whose body is only a
// source-language fallback for the Console's catalog (ADR 0033).
func writeChatTitleWindow(b *strings.Builder, msgs []ChatMessage) {
	real := make([]ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "report" || m.Role == "notice" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		real = append(real, m)
	}
	writeMsg := func(m ChatMessage) {
		text := strings.TrimSpace(m.Content)
		if r := []rune(text); len(r) > chatTitlePerMsgRunes {
			text = string(r[:chatTitlePerMsgRunes]) + "…"
		}
		fmt.Fprintf(b, "%s: %s\n", m.Role, text)
	}
	if len(real) <= chatTitleHeadMsgs+chatTitleTailMsgs {
		for _, m := range real {
			writeMsg(m)
		}
		return
	}
	for _, m := range real[:chatTitleHeadMsgs] {
		writeMsg(m)
	}
	b.WriteString("…（中略）…\n")
	for _, m := range real[len(real)-chatTitleTailMsgs:] {
		writeMsg(m)
	}
}

func runChatTitleSuggestLLM(ctx context.Context, msgs []ChatMessage) (string, error) {
	lang := titleLang() // same rule as session titles: generate in the display language and do not regenerate on a later switch
	reply, err := OneShotHeadless(ctx, OneShotShort, titleSuggestPersona(lang), ChatTitleSuggestPrompt(msgs, lang), titleModel())
	if err != nil {
		return "", fmt.Errorf("chat title generation failed: %w", err)
	}
	return cleanSuggestedTitle(reply), nil
}

// handleChatSuggestTitle previews an AI title suggestion for a conversation WITHOUT
// persisting it — mirrors handleSuggestTitle (session_title.go): the rename dialog's
// "ask the AI" (AIに提案してもらう) button fills the text field for the user to edit/accept themselves.
// Works even when the conversation already has a title (renaming is exactly that case).
func HandleChatSuggestTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatConversationNotFnd, "invalid conversation id")
		return
	}
	if !uiprefs.AssistantTitleSuggest() {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTitleFeatureDisabled, "assistant title suggestion is turned off")
		return
	}
	c, err := LoadConv(id)
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, errCodeChatConversationNotFnd, "conversation not found")
		return
	}
	if len(c.Messages) == 0 {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTitleNoContent, "not enough conversation yet to suggest a title")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), titleSuggestTimeout)
	defer cancel()
	ctx = usagex.WithTag(ctx, usagex.Tag{Feature: usagex.FeatureTitleChat, Trigger: usagex.TriggerManual, Ref: c.ID})
	title, err := runChatTitleSuggestLLM(ctx, c.Messages)
	if err != nil || title == "" {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "title generation failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestedTitle": title})
}
