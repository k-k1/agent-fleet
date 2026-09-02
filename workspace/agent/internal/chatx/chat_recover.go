package chatx

// コンテキスト超過エラーの検知・自己修復・通知（docs/log/33 第3段）。
//
// resume 駆動のチャットはコンテキストが積み上がり、いずれプロバイダのウィンドウを
// 超えて 1 ターンが 400 で失敗する。従来これは:
//   - 対話ターン: プロバイダエラーとして利用者に返るだけ（次も同じく失敗して詰む）
//   - 自動ターン（docs/log/30 オペレーター）: log に書くだけの black hole
// だった。第3段はこれを塞ぐ:
//   1. 超過エラーを判別（isContextOverflowErr）
//   2. 対話/自動ターンとも、その場で自前コンパクション（第2段 compactConversation）を
//      試し、成功したら新セッションで 1 回だけリトライ（recoverForRetry）
//   3. リトライも不能なら notice＋通知で必ず可視化（noteContextOverflow）
//
// 実測（2026-07）: claude -p は is_error+"Prompt is too long …"（terminal_reason
// "prompt_too_long"）、codex exec は "Input exceeds the maximum length …"
// / input_too_large。文言ドリフトに強くするため小文字部分一致を複数持つ。

import (
	"context"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

// contextOverflowNeedles はコンテキスト/入力過大エラーの判別語（小文字）。プロバイダの
// 文言が変わっても取りこぼしを減らすよう寛容に持つ。取りこぼしても notice が出ない
// だけで（通常のプロバイダエラーとして返る）、誤検知しても余分な要約 1 ターンを試す
// だけ（安全側）。
var contextOverflowNeedles = []string{
	"prompt is too long",  // claude
	"too many tokens",     //
	"context length",      // 一般的な OpenAI 系
	"context_length",      //
	"maximum context",     //
	"context window",      //
	"input is too large",  // codex
	"input too large",     //
	"input_too_large",     // codex (input_error_code)
	"exceeds the maximum", // codex "Input exceeds the maximum length …"
	"maximum length",      //
	"reduce the length",   //
	"too large for",       //
}

// isContextOverflowErr reports whether err looks like a context-window / input-size
// overflow (vs a transient network or auth failure).
func isContextOverflowErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, n := range contextOverflowNeedles {
		if strings.Contains(msg, n) {
			return true
		}
	}
	return false
}

// recoverForRetry attempts self-healing when origErr is a context overflow: it runs
// the app-level compaction (第2段) on the CURRENT session and, on success, leaves a
// PendingHandoff so the caller can rebuild the prompt (its own injectPendingReports /
// injectHandoff) and retry on the fresh session. Returns true only when a retry is
// worth attempting. The caller holds the conversation lock.
//
// If compaction itself fails (e.g. the accumulated context ALREADY exceeds the window,
// so even the summary turn overflows), returns false — the caller then surfaces
// noteContextOverflow. No retry loop: one heal attempt per failed turn.
func recoverForRetry(ctx context.Context, c *chatConversation, prov chatProvider, origErr error) bool {
	if !isContextOverflowErr(origErr) {
		return false
	}
	if err := compactConversation(ctx, c, prov, compactReasonRecovery); err != nil {
		return false
	}
	return true
}

// noteContextOverflow appends a one-off notice that the turn failed on context overflow
// and could not be auto-healed, and mirrors it into the notification center (so an
// unattended auto turn — docs/log/30 — is never silently lost). The caller holds the
// conversation lock and saves afterwards.
func noteContextOverflow(c *chatConversation) {
	c.Messages = append(c.Messages, newNotice(noticeKeyCtxOverflow, nil, contextOverflowContent()))
	ev := notice.New("chat-context-overflow", "", "", c.Title)
	ev.Payload["conversation_id"] = c.ID
	ev.Payload["conversationTitle"] = c.Title
	_ = notice.Put(ev)
}

// contextOverflowContent は超過 notice の正本言語（ja）フォールバック本文。表示は
// noticeKeyCtxOverflow のカタログ訳が担う（chat_notice.go / ADR 0033）。
func contextOverflowContent() string {
	return "コンテキストが上限を超えたため、応答を生成できませんでした。" +
		"この会話はこのままでは続行が難しい状態です。ヘッダのコンテキストバー右にある「圧縮」で" +
		"要約だけを新しいセッションへ引き継いで続けるか、新しいチャットを開いて必要な要点だけを渡してください。"
}
