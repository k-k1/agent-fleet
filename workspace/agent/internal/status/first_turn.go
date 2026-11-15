package status

// The first turn of a queue delivery (ADR 0137 §7). The Control Plane's circuit breaker needs to
// know how the turn that the delivery's prompt started ended, and how long it ran. The status
// record cannot answer: it describes the latest turn, and a stop deletes it.
//
// So a delivery's session gets a ledger of its own, armed before the prompt is delivered and
// settled by the first end of turn that follows exactly one start. It lives in its own file, is
// written once, and is never removed by a stop or an Agent restart.
//
// The rule that matters is the negative one: a later turn's end must never be read as the first
// turn's. The ledger therefore counts turn starts as the status writers see them (under the same
// per-sid lock). An end settles it only when exactly one start came before it. A second start
// first (the first end was missed: the status was deleted by a stop, a heal, an Agent that was
// not running) voids the ledger for good, and so does an end with no start behind it.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// TurnOutcomeClean is the outcome of a turn that ended without a failure qualifier. Together
// with TurnEndReasonFailed and TurnEndReasonAborted it is the vocabulary a scheduled run records
// (chatx.writeRunOutcome), and the delivery's first turn uses the same words.
const TurnOutcomeClean = "clean"

// FirstTurn is the ledger of one delivery's first turn.
type FirstTurn struct {
	// ArmedAt is when the ledger was armed, before the prompt was delivered.
	ArmedAt string `json:"armedAt"`
	// NoReason marks a kind that records no reason for a turn end (the hook-less TUI kinds): its
	// outcome stays empty however the end was seen.
	NoReason bool `json:"noReason,omitempty"`
	// Delivering: the prompt's delivery began (MarkFirstTurnDelivering). A turn that starts
	// before it is somebody else's, and voids the ledger.
	Delivering bool `json:"delivering,omitempty"`
	// FirstPrompt is the prompt id of the first turn when the kind has one (claude, codex hooks).
	FirstPrompt string `json:"firstPrompt,omitempty"`
	// Starts counts the turn starts seen since ArmedAt; StartAt is the first one's time.
	Starts  int    `json:"starts,omitempty"`
	StartAt string `json:"startAt,omitempty"`
	// Void: the end the Agent saw cannot be shown to be the first turn's; the ledger stays
	// empty for good.
	Void bool `json:"void,omitempty"`
	// Done: the first turn ended. Outcome, EndedAt and Ms are final.
	Done    bool   `json:"done,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	EndedAt string `json:"endedAt,omitempty"`
	Ms      int64  `json:"ms,omitempty"`
}

var firstTurns = fstore.JSON[FirstTurn](paths.AgentStateDir, "session-first-turn", ".json")

// ArmFirstTurn starts the ledger for sid, replacing any older one: it is called by the create
// of a delivery's session, and a name and directory that are used again are a new session.
func ArmFirstTurn(sid string, noReason bool) {
	unlock, _ := lockSid(sid)
	defer unlock()
	_ = firstTurns.Write(sid, FirstTurn{ArmedAt: time.Now().Format(time.RFC3339Nano), NoReason: noReason})
}

// MarkFirstTurnDelivering records that the delivery's prompt is about to be sent. A turn already
// seen is not the prompt's, so it voids the ledger instead.
func MarkFirstTurnDelivering(sid string) {
	unlock, _ := lockSid(sid)
	defer unlock()
	t, ok := firstTurns.Read(sid)
	if !ok || t.Done || t.Void {
		return
	}
	if t.Starts > 0 {
		t.Void = true
	} else {
		t.Delivering = true
	}
	_ = firstTurns.Write(sid, t)
}

// voidOpenFirstTurn is called with the per-sid lock held when the status record is deleted (a
// stop, a heal): a turn still open is one whose end may now go unseen. A settled ledger stays.
func voidOpenFirstTurn(sid string) {
	t, ok := firstTurns.Read(sid)
	if !ok || t.Done || t.Void || t.Starts == 0 {
		return
	}
	t.Void = true
	_ = firstTurns.Write(sid, t)
}

// ReadFirstTurn returns the settled first turn of sid; ok is false until it has ended, and for
// good when it was voided.
func ReadFirstTurn(sid string) (FirstTurn, bool) {
	t, ok := firstTurns.Read(sid)
	if !ok || !t.Done || t.Void {
		return FirstTurn{}, false
	}
	return t, true
}

// RemoveFirstTurn forgets sid's ledger; only deleting the session does.
func RemoveFirstTurn(sid string) { firstTurns.Remove(sid) }

// noteFirstTurnWrite is called by persistLocked, with the per-sid lock held and before the new
// record replaces the old one, so it can tell a start (a working write after an idle or no
// record) from a heartbeat or the return from a question.
func noteFirstTurnWrite(sid string, s SessionStatus) {
	t, ok := firstTurns.Read(sid)
	if !ok || t.Done || t.Void {
		return
	}
	switch {
	case s.State == "working":
		// The ledger holds the first turn's own prompt id, so a later turn is told apart even
		// when the record in between was lost or never changed state.
		if t.FirstPrompt != "" && s.PromptID != "" && s.PromptID != t.FirstPrompt {
			t.Void = true
			break
		}
		prev, had := Read(sid)
		start := s.turnStart || !had || (prev.State == "idle" && !prev.InputNotDelivered)
		if !start {
			// A heartbeat, the answer to a question or permission, or the input of this same turn
			// turning up after the list had called it undelivered: the same turn.
			if t.FirstPrompt == "" && t.Starts == 1 && s.PromptID != "" {
				t.FirstPrompt = s.PromptID
				break
			}
			return
		}
		t.Starts++
		if !t.Delivering {
			t.Void = true // a turn that is not the delivery's prompt's
		}
		switch t.Starts {
		case 1:
			t.StartAt = time.Now().Format(time.RFC3339Nano)
			t.FirstPrompt = s.PromptID
		default:
			t.Void = true
		}
	case s.TurnEnd:
		if t.FirstPrompt != "" && s.PromptID != "" && s.PromptID != t.FirstPrompt {
			t.Void = true // the end of another prompt's turn
			break
		}
		settleFirstTurn(&t, s.TurnEndAt, s.TurnEndReason)
	default:
		return
	}
	_ = firstTurns.Write(sid, t)
}

// settleFirstTurn closes t with an end at endedAt, or voids it when that end is not provably the
// first turn's.
func settleFirstTurn(t *FirstTurn, endedAt, reason string) {
	if t.Starts != 1 {
		t.Void = true
		return
	}
	end, err := time.Parse(time.RFC3339Nano, endedAt)
	start, err2 := time.Parse(time.RFC3339Nano, t.StartAt)
	if err != nil || err2 != nil {
		t.Void = true
		return
	}
	t.Done, t.EndedAt = true, end.UTC().Format(time.RFC3339)
	t.Ms = max(end.Sub(start).Milliseconds(), 0)
	switch {
	case t.NoReason:
		t.Outcome = ""
	case reason == "":
		t.Outcome = TurnOutcomeClean
	default:
		t.Outcome = reason
	}
}

// noteObservedFirstTurnEnd is RecordTurnEnd's half: a poll saw the running turn end, for a kind
// that has no hook to say so. The status record is untouched there, so there is no write to hang
// the settle on.
func noteObservedFirstTurnEnd(sid, at string) {
	unlock, _ := lockSid(sid)
	defer unlock()
	t, ok := firstTurns.Read(sid)
	if !ok || t.Done || t.Void {
		return
	}
	settleFirstTurn(&t, at, "")
	_ = firstTurns.Write(sid, t)
}

// PersistTurnStart is the write of a turn the driver started itself (agents.MarkTurnStart*): it is
// a turn start whatever the record said before. A Managed turn's start carries no prompt id and
// the record may still say working from a turn whose end nobody saw (the Agent was not running),
// so the state alone cannot tell this start from a heartbeat.
func PersistTurnStart(sid string) {
	persist(sid, SessionStatus{State: "working", turnStart: true})
	RemoveCompletionKey(sid)
}
