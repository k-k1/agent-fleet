package agy

import (
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
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

// InputIdle is the idle composer footer on the pane right now. "esc to cancel" stays on screen
// for the whole of a turn (stale.go), so a turn agy has just begun is refused even before its
// DB rows land. Not paneIdleSettled: its settle window starts at the first sighting, and the
// nudge asks only once its own 60 s of idle are over, so every first ask would read unsettled
// and restart the grace.
func (agentImpl) InputIdle(m session.Meta) bool {
	return hasIdleFooter(capturePaneFrame(session.TmuxName(m.Name)))
}

// capturePaneFrame is the capture seam, so a test can feed a frame without tmux.
var capturePaneFrame = tmuxx.CapturePane
