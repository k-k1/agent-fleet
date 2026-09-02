package chatx

import (
	"context"
	"testing"
)

// The in-flight registry underpins the reload-safe chat turn: handleChatGet reports
// turnInFlight() as in_progress so a reloaded client polls for the detached reply, and
// the Stop button cancels via cancelLiveTurn(). Verify register / query / cancel / clear.
func TestLiveTurnRegistry(t *testing.T) {
	const id = "conv-1"

	if TurnInFlight(id) {
		t.Fatal("no turn registered yet, want not in flight")
	}
	if cancelLiveTurn(id) {
		t.Fatal("cancel with nothing registered should report not found")
	}

	ctx, cancel := context.WithCancel(context.Background())
	deregister := RegisterLiveTurn(id, cancel)

	if !TurnInFlight(id) {
		t.Fatal("turn registered, want in flight")
	}

	if !cancelLiveTurn(id) {
		t.Fatal("cancel of a registered turn should report found")
	}
	select {
	case <-ctx.Done(): // cancelLiveTurn must actually cancel the turn's context
	default:
		t.Fatal("cancelLiveTurn did not cancel the context")
	}

	// A turn is still in flight until it deregisters (cancel != deregister), so a stopped
	// turn keeps in_progress true until it winds down and saves — the poller keeps waiting.
	if !TurnInFlight(id) {
		t.Fatal("cancel alone must not deregister; want still in flight until turn ends")
	}

	deregister()
	if TurnInFlight(id) {
		t.Fatal("after deregister, want not in flight")
	}
}
