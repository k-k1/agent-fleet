package chatx

// Detection, self-healing and notification for context-overflow errors (docs/log/33 stage 3).
//
// A resume-driven chat accumulates context until it passes the provider's window and one turn
// fails with a 400. That used to mean:
//   - interactive turn: the provider error is handed to the user and the next turn fails the
//     same way, so the conversation is stuck
//   - automatic turn (docs/log/30 operator): a black hole with only a log line
//
// Stage 3 closes both:
//  1. recognise the overflow (IsContextOverflowErr)
//  2. for interactive and automatic turns alike, try the app-level compaction (stage 2,
//     compactConversation) on the spot and, on success, retry exactly once on the new session
//     (RecoverForRetry)
//  3. when a retry is impossible, surface it with a notice plus a notification
//     (NoteContextOverflow)
//
// Measured (2026-07): claude -p returns is_error + "Prompt is too long …" (terminal_reason
// "prompt_too_long"), codex exec returns "Input exceeds the maximum length …" /
// input_too_large. Several lowercase substrings are kept so wording drift does not defeat the
// match.

import (
	"context"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
)

// contextOverflowNeedles are the lowercase markers of a context/input-too-large error. They
// are deliberately generous so a reworded provider message still matches: a miss only costs
// the notice (the plain provider error is returned instead) and a false positive only costs
// one extra summarisation turn, so erring wide is the safe side.
var contextOverflowNeedles = []string{
	"prompt is too long",  // claude
	"too many tokens",     //
	"context length",      // common in the OpenAI family
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

// IsContextOverflowErr reports whether err looks like a context-window / input-size
// overflow (vs a transient network or auth failure).
func IsContextOverflowErr(err error) bool {
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
// the app-level compaction (stage 2) on the CURRENT session and, on success, leaves a
// PendingHandoff so the caller can rebuild the prompt (its own injectPendingReports /
// injectHandoff) and retry on the fresh session. Returns true only when a retry is
// worth attempting. The caller holds the conversation lock.
//
// If compaction itself fails (e.g. the accumulated context ALREADY exceeds the window,
// so even the summary turn overflows), returns false — the caller then surfaces
// noteContextOverflow. No retry loop: one heal attempt per failed turn.
func RecoverForRetry(ctx context.Context, c *ChatConversation, prov ChatProvider, origErr error) bool {
	if !IsContextOverflowErr(origErr) {
		return false
	}
	if err := compactConversation(ctx, c, prov, CompactReasonRecovery); err != nil {
		return false
	}
	return true
}

// noteContextOverflow appends a one-off notice that the turn failed on context overflow
// and could not be auto-healed, and mirrors it into the notification center (so an
// unattended auto turn — docs/log/30 — is never silently lost). The caller holds the
// conversation lock and saves afterwards.
func NoteContextOverflow(c *ChatConversation) {
	c.Messages = append(c.Messages, newNotice(noticeKeyCtxOverflow, nil, contextOverflowContent()))
	ev := notice.New("chat-context-overflow", "", "", c.Title)
	ev.Payload["conversation_id"] = c.ID
	ev.Payload["conversationTitle"] = c.Title
	_ = notice.Put(ev)
}

// contextOverflowContent is the source-language (ja) fallback body of the overflow notice.
// What is displayed comes from the catalogue translation of noticeKeyCtxOverflow
// (chat_notice.go / ADR 0033).
func contextOverflowContent() string {
	return "コンテキストが上限を超えたため、応答を生成できませんでした。" +
		"この会話はこのままでは続行が難しい状態です。ヘッダのコンテキストバー右にある「圧縮」で" +
		"要約だけを新しいセッションへ引き継いで続けるか、新しいチャットを開いて必要な要点だけを渡してください。"
}
