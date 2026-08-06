package main

// 返信サジェスト v2 のチャット版。session_suggest_reply.go の persona / モデル / 整形
// （cleanSuggestedReplies）をそのまま流用し、文脈だけ chatMessage 配列（report・空を除く
// 直近数件）から組む。chat_title.go と同じく conv は書き換えない preview 専用。

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// chatReplySuggestPrompt は直近メッセージ（末尾窓）を文脈に、返信候補の生成を指示する。
// report と notice は会話の話題ではない（notice 本文は表示用カタログの正本言語
// フォールバックにすぎない — ADR 0033）ので窓から外す。
// 窓の切り出し（畳み込み・文字予算・行単位の末尾）はセッション版と完全に共通。チャットは
// 1 メッセージが最初から 1 発言なので畳み込みは基本 no-op だが、予算窓の効き方は同じ。
func chatReplySuggestPrompt(msgs []chatMessage, lang string) string {
	real := make([]replyMsg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "report" || m.Role == "notice" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		real = append(real, replyMsg{m.Role, m.Content})
	}
	var b strings.Builder
	b.WriteString(replySuggestInstructions(lang, replyCounterpartChat))
	b.WriteString(replySuggestLogHeader(lang))
	replySuggestWindow(&b, real)
	return b.String()
}

func runChatReplySuggestLLM(ctx context.Context, msgs []chatMessage) ([]string, error) {
	lang := uiLocale()
	reply, err := oneShotHeadless(ctx, replySuggestPersona(lang), chatReplySuggestPrompt(msgs, lang), replySuggestModel())
	if err != nil {
		return nil, fmt.Errorf("chat reply suggestion failed: %w", err)
	}
	return cleanSuggestedReplies(reply), nil
}

// handleChatSuggestReplies は preview 専用。Console のチャット✨ボタンが叩き、返ってきた候補を
// コンポーサー上のチップ列にマージする。
func handleChatSuggestReplies(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validConvID(id) {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatConversationNotFnd, "invalid conversation id")
		return
	}
	if !replySuggestEnabled() {
		httpx.WriteErr(w, http.StatusBadRequest, "feature_disabled", "reply suggestion is turned off")
		return
	}
	c, err := loadConv(id)
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
	ctx = withUsageTag(ctx, usageTag{Feature: usageFeatureSuggestChat, Trigger: usageTriggerManual, Ref: c.ID})
	reps, err := runChatReplySuggestLLM(ctx, c.Messages)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "reply suggestion failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestions": reps})
}
