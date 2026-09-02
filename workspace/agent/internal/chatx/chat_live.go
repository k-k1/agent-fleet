package chatx

// アシスタントチャットの「実行中ターン」レジストリ。
//
// ストリーミングのターン（handleChatStream）は、開始した HTTP リクエストの寿命から
// あえて切り離して走らせる（chat_handlers.go の context.WithoutCancel 参照）。ブラウザ
// をリロードすると SSE リクエストは中断されるが、それでターンを殺して回答を失わない
// ためだ ── ターンはそのまま走り切って保存され、再接続したクライアントは in_progress
// を見て（chatGet）ポーリングで再アタッチし、確定した回答を読める。
//
// レジストリが保持する CancelFunc は、切り離したターンを「明示的な停止」（Stop ボタン →
// handleChatStop）で確実に止めるための唯一の経路。単なる切断（リロード）では発火しない。

import (
	"context"
	"sync"
)

var liveTurns sync.Map // conversation id -> context.CancelFunc

// registerLiveTurn marks a conversation's turn as running and stores its cancel
// func; the returned func deregisters it (call via defer at turn end).
func RegisterLiveTurn(id string, cancel context.CancelFunc) func() {
	liveTurns.Store(id, cancel)
	return func() { liveTurns.Delete(id) }
}

// turnInFlight reports whether an assistant turn is currently running for this
// conversation. handleChatGet surfaces it as in_progress so a reloaded client
// knows the answer is still coming and polls for it.
func TurnInFlight(id string) bool {
	_, ok := liveTurns.Load(id)
	return ok
}

// cancelLiveTurn stops the detached turn for a conversation, if one is running,
// and reports whether one was found. This is how the Stop button actually cancels
// a turn now that the turn no longer dies with its request connection.
func cancelLiveTurn(id string) bool {
	v, ok := liveTurns.Load(id)
	if !ok {
		return false
	}
	if cancel, ok := v.(context.CancelFunc); ok {
		cancel()
	}
	return true
}
