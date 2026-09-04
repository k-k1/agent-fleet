package chatx

// Registry of the assistant chat's in-flight turns.
//
// A streaming turn (handleChatStream) deliberately runs detached from the lifetime of the
// HTTP request that started it (see context.WithoutCancel in chat_handlers.go). Reloading
// the browser aborts the SSE request, and that must not kill the turn and lose the answer:
// the turn runs to completion and is saved, while a reconnecting client sees in_progress
// (chatGet), re-attaches by polling and reads the finished answer.
//
// The CancelFunc the registry holds is the only path that reliably stops a detached turn on
// an explicit halt (the Stop button -> handleChatStop). A mere disconnect (a reload) does
// not fire it.

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
