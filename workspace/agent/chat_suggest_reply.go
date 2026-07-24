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
func chatReplySuggestPrompt(msgs []chatMessage) string {
	real := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "report" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		real = append(real, m)
	}
	if len(real) > replySuggestTailTurns {
		real = real[len(real)-replySuggestTailTurns:]
	}
	var b strings.Builder
	b.WriteString("会話ログの続きとして、ユーザーが次に送る返信の候補を最大3件、改行区切りで出力してください。\n")
	b.WriteString("直前のアシスタントの発言に噛み合う短文にすること。丁寧語にせず、常体・命令形で簡潔に。\n")
	b.WriteString("例（すべて常体で簡潔に・承認/続行/回答/中断）: 進めて / OK / 1番で / 待って / 修正して\n\n")
	b.WriteString("--- 会話ログ ---\n")
	for _, m := range real {
		text := strings.TrimSpace(m.Content)
		if r := []rune(text); len(r) > chatTitlePerMsgRunes {
			text = string(r[:chatTitlePerMsgRunes]) + "…"
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, text)
	}
	return b.String()
}

func runChatReplySuggestLLM(ctx context.Context, msgs []chatMessage) ([]string, error) {
	reply, err := oneShotHeadless(ctx, replySuggestPersona, chatReplySuggestPrompt(msgs), replySuggestModel())
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
	reps, err := runChatReplySuggestLLM(ctx, c.Messages)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "generation_failed", "reply suggestion failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suggestions": reps})
}
