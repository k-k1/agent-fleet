package opencode

import (
	"encoding/json"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.ManagedProgressReporter = managedDriver{}

// ManagedProgress: opencode 2.x sends no event while a tool runs (measured on 2.0.32: nothing,
// the server-wide stream included, between session.tool.progress and session.tool.success of a
// 120 s `sleep`), and the shell is a child of the one shared daemon, so neither a heartbeat nor
// the process table says which session it belongs to. The trace of a silent tool is the pair
// session.tool.called ... session.tool.success, so a call that was announced and has not
// finished counts as a live tool.
func (managedDriver) ManagedProgress(m session.Meta) (time.Time, bool) {
	h := handleFor(m.Name)
	if h == nil {
		return time.Time{}, false
	}
	return h.act.Progress(time.Now(), h.tools.State)
}

// noteProgress stamps the owning handle for an event that names its session and keeps the set of
// tool calls in flight. A call whose end event is missed is dropped when its step or execution
// ends, so a lost event cannot pin the row for longer than the turn.
func noteProgress(typ string, props json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionID"`
		ID        string `json:"id"`
	}
	if json.Unmarshal(props, &p) != nil || p.SessionID == "" {
		return
	}
	h := handleBySes(p.SessionID)
	if h == nil {
		return
	}
	h.act.Touch()
	switch typ {
	case "session.tool.called", "session.tool.progress":
		// progress restates a running call, so one whose called event fell into a gap in the
		// stream is restored by the next event it sends.
		h.tools.Add(p.ID)
	case "session.tool.success", "session.tool.failed", "session.tool.error":
		h.tools.Done(p.ID)
	case "session.execution.started", "session.step.ended", "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted", "session.idle":
		// A turn boundary seen on a live stream: nothing from before it is still running, and
		// the set is trustworthy again.
		h.tools.Reset()
	}
}
