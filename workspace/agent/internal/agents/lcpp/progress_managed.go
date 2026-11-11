package lcpp

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.ManagedProgressReporter = managedDriver{}

// turnKey is the one in-flight entry a handle holds: the pump runs one turn at a time.
const turnKey = "turn"

// ManagedProgress: lcpp runs in-process, so there is no child process, no daemon and no event
// stream to read. What "a tool is in flight" means here is "runTurn is executing": from the
// moment the turn leaves the queue until its messages are persisted, every step that can be
// silent for a long time (a bash command, an MCP tools/call, a model request that has not
// produced its first chunk, an engine wake retry, a compaction, a wait for the user's approval)
// happens inside that window, on this handle's goroutine. Counting open tool calls instead
// would miss the model request and the wake retry, both unbounded in time. The cost is that a
// call that hangs inside the window cannot be told from a long one and holds until the turn is
// stopped (the safe side, as for cursor / kiro / opencode).
//
// No heartbeat was measured, because there is nothing to measure: the signal is the handle's
// own state, not a runtime's behaviour. A handle that has never run a turn gives no answer and
// keeps holding; one whose turn ended reports that moment, so a row left "working" afterwards
// (a crash between the state write and the turn's end) can lapse.
func (managedDriver) ManagedProgress(m session.Meta) (time.Time, bool) {
	h := handleFor(m.Name)
	if h == nil {
		return time.Time{}, false
	}
	return h.act.Progress(time.Now(), h.turns.State)
}

// enterTurn marks a turn as running and returns the function that marks it over.
func (h *threadHandle) enterTurn() func() {
	h.act.Touch()
	h.turns.Add(turnKey)
	return func() {
		h.turns.Done(turnKey)
		h.act.Touch()
	}
}
