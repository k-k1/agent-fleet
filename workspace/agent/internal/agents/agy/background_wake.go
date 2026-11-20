package agy

import (
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.BackgroundWaker = agentImpl{}

// FinishedTurnMark is the conversation plus the step idx its newest executor run ended at
// (turnEnd, pending.go). A turn agy starts on its own when a background task completes ends
// in a new executor_metadata row, so the mark moves; a completion notice agy writes without
// running the model does not. The conversation is part of the mark so a /clear, which starts
// over at low idxs, can never read as the same turn. A build without executor_metadata, or a
// DB that cannot be read, has no mark: the nudge then stays off rather than guess.
func (agentImpl) FinishedTurnMark(m session.Meta) (string, bool) {
	r, ok := lastStep(m)
	if !ok {
		return "", false
	}
	return finishedTurnMark(r)
}

func finishedTurnMark(r lastStepRow) (string, bool) {
	if r.turnLog != turnLogRead || r.turnEnd < 0 {
		return "", false
	}
	return r.conv + ":" + strconv.Itoa(r.turnEnd), true
}
