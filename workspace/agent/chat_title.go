package main

// アシスタントチャットのタイトルAI提案（プレビュー専用）。session_title.go と同じ oneShotHeadless
// 基盤・persona・クリーニングを流用するが、チャットの会話はすでに構造化された chatMessage 配列を
// 持つため transcript.Turn への変換は不要（sidechain/tool-only turn を持たない分、セッションより
// 単純）。セッションの自動提案バナー（SuggestedTitle 保留→受理/却下）に相当する仕組みは持たず、
// リネームダイアログの「AIに提案してもらう」ボタン専用 — conv.Title は書き換えない。

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
func chatTitleSuggestPrompt(msgs []chatMessage, lang string) string {
	var b strings.Builder
	b.WriteString(titleSuggestInstructions(lang)) // session_title.go と共有（前置き・ラベル禁止の 悪い例 込み）
	writeChatTitleWindow(&b, msgs)
	b.WriteString(titleSuggestFooter(lang)) // 会話の続きを書き始めるのを防ぐ後置きも共有
	return b.String()
}

// writeChatTitleWindow appends the opening + most recent non-empty user/assistant
// messages (head/tail windowing, per-message length cap). Report cards (role=="report")
// are skipped — they're session-origin, not conversation topic — and so are system
// notices (role=="notice"), which are UI housekeeping whose body is only a
// source-language fallback for the Console's catalog (ADR 0033).
func writeChatTitleWindow(b *strings.Builder, msgs []chatMessage) {
	real := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "report" || m.Role == "notice" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		real = append(real, m)
	}
	writeMsg := func(m chatMessage) {
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

func runChatTitleSuggestLLM(ctx context.Context, msgs []chatMessage) (string, error) {
	lang := titleLang() // セッション件名と同じ規約（表示言語で生成し、後からの切替では作り直さない）
	reply, err := oneShotHeadless(ctx, titleSuggestPersona(lang), chatTitleSuggestPrompt(msgs, lang), titleModel())
	if err != nil {
		return "", fmt.Errorf("chat title generation failed: %w", err)
	}
	return cleanSuggestedTitle(reply), nil
}

// handleChatSuggestTitle previews an AI title suggestion for a conversation WITHOUT
// persisting it — mirrors handleSuggestTitle (session_title.go): the rename dialog's
// "AIに提案してもらう" button fills the text field for the user to edit/accept themselves.
// Works even when the conversation already has a title (renaming is exactly that case).
func handleChatSuggestTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !paths.ValidIDSegment(id) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatConversationNotFnd, "invalid conversation id")
		return
	}
	if !uiprefs.AssistantTitleSuggest() {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeTitleFeatureDisabled, "assistant title suggestion is turned off")
		return
	}
	c, err := loadConv(id)
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
