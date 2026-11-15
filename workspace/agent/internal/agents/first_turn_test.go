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
