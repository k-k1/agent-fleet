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
	status.MarkFirstTurnDelivering(sid, "task")
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
	status.MarkFirstTurnDelivering(sid, "task")
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
	status.MarkFirstTurnDelivering(sid, "c1")
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
	status.MarkFirstTurnDelivering(sid, "task")
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
	status.MarkFirstTurnDelivering(sid2, "task")
	MarkTurnStartRun(sid2, in)
	MarkTurnStartRun(sid2, TurnInput{Prompt: "other", ClientMessageID: "c2"})
	MarkTurnEnd(sid2, TurnCompleted)
	if got, ok := status.ReadFirstTurn(sid2); ok {
		t.Fatalf("a second input's turn settled the ledger: %+v", got)
	}
}

// The create's optimistic working write was made and the Agent died before the pump started the
// turn: the delivery's input is lost. A person's different input after the restart must not be
// taken for the delivery's turn.
func TestLostOptimisticInputDoesNotAdoptALaterStart(t *testing.T) {
	captureNotifier(t)
	const sid = "managed-lost"
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid, "q_d1")
	status.PersistRev(sid, "working") // optimistic, then the Agent dies
	MarkTurnStartRun(sid, TurnInput{Prompt: "a person's message", ClientMessageID: "p1"})
	MarkTurnEnd(sid, TurnCompleted)
	if got, ok := status.ReadFirstTurn(sid); ok {
		t.Fatalf("a person's turn was reported as the delivery's: %+v", got)
	}
}

// A start with no id, or another id, never confirms the optimistic write: it is a turn of its own.
func TestAStartThatIsNotTheDeliverysInputVoidsTheLedger(t *testing.T) {
	for name, in := range map[string]TurnInput{"no id": {Prompt: "task"}, "other id": {Prompt: "task", ClientMessageID: "other"}} {
		captureNotifier(t)
		sid := "managed-void-" + name
		status.ArmFirstTurn(sid, false)
		status.MarkFirstTurnDelivering(sid, "q_d1")
		status.PersistRev(sid, "working") // optimistic
		MarkTurnStartRun(sid, in)
		MarkTurnEnd(sid, TurnCompleted)
		if got, ok := status.ReadFirstTurn(sid); ok {
			t.Fatalf("%s: reported %+v", name, got)
		}
	}
}

// An Agent restart that finds the ledger holding only the unconfirmed optimistic write voids it.
func TestRestartVoidsAnUnconfirmedOptimisticLedger(t *testing.T) {
	captureNotifier(t)
	const sid = "managed-restart"
	status.ArmFirstTurn(sid, false)
	status.MarkFirstTurnDelivering(sid, "q_d1")
	status.PersistRev(sid, "working")
	if n := status.VoidUnconfirmedFirstTurns(); n != 1 {
		t.Fatalf("voided %d", n)
	}
	MarkTurnStartRun(sid, TurnInput{Prompt: "q", ClientMessageID: "q_d1"})
	MarkTurnEnd(sid, TurnCompleted)
	if got, ok := status.ReadFirstTurn(sid); ok {
		t.Fatalf("a ledger the restart found unconfirmed settled: %+v", got)
	}
}
