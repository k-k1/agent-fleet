package main

// スケジュール発のアシスタント発火（docs/log/38 session_mode=assistant）の受け口。
//
// CP スケジューラが発火時に POST /assistant-turns {conv, prompt} を叩き、指定会話
// （UUID または "a…" slug）にプロンプトを user ターンとして投入して 1 ターン走らせる。
// ターン機構は再発明しない — runOperatorTurn（Discord @メンション経路と同一・
// handleChatSend の非 HTTP 双子）に委譲するので、ロック・自動圧縮・overflow 自己修復・
// AutoTurns リセット・無人承認ゲート（破壊的ツールはブリッジ承認が必要＝ブリッジ未接続
// なら fail-closed）まで全部同じ挙動になる。
//
// 実行中の会話への発火は 409 turn_in_progress を返し、CP 側が skipped_overlap として
// 記録する（reuse セッションの overlap=skip と同じ無人非配送サーフェス）。配達検証
// （/input の confirm）に相当する追加機構は不要 — このターンは同期実行で、返答が
// 返る＝実行された、エラー＝実行されなかった、が呼び出しの意味論そのもの。

import (
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

func handleAssistantTurn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Conv   string `json:"conv"` // conversation UUID or "a…" slug
		Prompt string `json:"prompt"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeChatPromptEmpty, "prompt is empty")
		return
	}
	id, ok := chatx.ResolveConvRef(req.Conv)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "conv_not_found", "no such conversation: "+req.Conv)
		return
	}
	// A turn already in flight would only queue behind the conversation lock and pile
	// up; surface it as a conflict so the scheduler records skipped_overlap instead.
	if chatx.TurnInFlight(id) {
		httpx.WriteErr(w, http.StatusConflict, "turn_in_progress", "an assistant turn is already running for this conversation")
		return
	}
	// 使用量台帳（ADR 0029 §3）: 機構はブリッジと同じでも、消費の意味は「定時実行が
	// 無人で回したチャット1ターン」— feature=assistant.chat / trigger=schedule で数える。
	reply, err := runOperatorTurnAs(id, req.Prompt, usagex.Tag{
		Feature: usagex.FeatureAssistantChat, Trigger: usagex.TriggerSchedule, Ref: id,
	})
	if err != nil {
		// reply carries the localized reason line (runOperatorTurn's contract).
		msg := strings.TrimSpace(reply)
		if msg == "" {
			msg = err.Error()
		}
		httpx.WriteErr(w, http.StatusBadGateway, "turn_failed", msg)
		return
	}
	slug := ""
	if c, cerr := chatx.LoadConv(id); cerr == nil {
		slug = c.Slug
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"conv": id, "slug": slug, "reply": reply})
}
