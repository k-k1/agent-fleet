package agents

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// A Managed turn's start carries no prompt id, and after an Agent restart in the middle of the
// first turn the record still says working. The next turn's start is then a heartbeat to the
// state alone, and its end would be taken for the first turn's. The driver's own start write
// is a start whatever the record said.
func TestManagedMissedFirstEndMustNotAcceptALaterTurn(t *testing.T) {
	captureNotifier(t)
	const sid = "managed-first"
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid)
	MarkTurnStartRun(sid, TurnInput{Prompt: "the queue's task"})
	// The Agent dies here; the first turn ends while nothing is running, and the record stays
	// working. A person's turn starts after the restart.
	MarkTurnStartRun(sid, TurnInput{Prompt: "a person's message"})
	MarkTurnEnd(sid, TurnCompleted)
	if got, ok := status.ReadFirstTurn(sid); ok {
		t.Fatalf("a later turn's end was reported as the first turn's: %+v", got)
	}
}

// The ordinary managed first turn still settles, including the optimistic working write the
// create makes after the send.
func TestManagedFirstTurnSettlesWithTheOptimisticWorkingWrite(t *testing.T) {
	captureNotifier(t)
	const sid = "managed-ok"
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid)
	MarkTurnStartRun(sid, TurnInput{Prompt: "task"})
	status.PersistRev(sid, "working") // markSessionWorking after Send
	MarkTurnEnd(sid, TurnCompleted)
	if got, ok := status.ReadFirstTurn(sid); !ok || got.Outcome != status.TurnOutcomeClean {
		t.Fatalf("first turn = %+v ok=%v", got, ok)
	}
}

// The create's optimistic working write usually comes before the pump's own start of the same
// turn: the second is a confirmation, not another turn.
func TestOptimisticWorkingBeforeTheManagedStartIsTheSameTurn(t *testing.T) {
	captureNotifier(t)
	const sid = "managed-opt-first"
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid)
	status.PersistRev(sid, "working") // markSessionWorking after Send
	MarkTurnStartRun(sid, TurnInput{Prompt: "task", ClientMessageID: "c1"})
	MarkTurnEnd(sid, TurnCompleted)
	if got, ok := status.ReadFirstTurn(sid); !ok || got.Outcome != status.TurnOutcomeClean {
		t.Fatalf("first turn = %+v ok=%v", got, ok)
	}
}

// codex marks the same input at send and again on its turn/started notification.
func TestTheSameInputStartedTwiceIsOneTurn(t *testing.T) {
	captureNotifier(t)
	const sid = "managed-twice"
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid)
	in := TurnInput{Prompt: "task", ClientMessageID: "c1"}
	MarkTurnStartRun(sid, in)
	MarkTurnStartRun(sid, in)
	MarkTurnEnd(sid, TurnCompleted)
	if _, ok := status.ReadFirstTurn(sid); !ok {
		t.Fatal("one input started twice voided the first turn")
	}
	// A different input while the first turn has not ended is a second turn.
	const sid2 = "managed-two"
	status.ArmFirstTurn(sid2, false)
	status.MarkFirstTurnDelivering(sid2)
	MarkTurnStartRun(sid2, in)
	MarkTurnStartRun(sid2, TurnInput{Prompt: "other", ClientMessageID: "c2"})
	MarkTurnEnd(sid2, TurnCompleted)
	if got, ok := status.ReadFirstTurn(sid2); ok {
		t.Fatalf("a second input's turn settled the ledger: %+v", got)
	}
}
