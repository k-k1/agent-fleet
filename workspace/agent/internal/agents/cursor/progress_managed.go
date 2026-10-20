package cursor

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.ManagedProgressReporter = managedDriver{}

// ManagedProgress: the ACP child sends nothing while a tool runs (measured on 2026.10.16: no
// message for the whole of a 120 s `sleep`), but the tool is a process under that child and a
// session leader of its own, so procx.ToolProcessIn sees it for as long as it lives.
func (managedDriver) ManagedProgress(m session.Meta) (time.Time, bool) {
	h := handleFor(m.Name)
	if h == nil {
		return time.Time{}, false
	}
	return h.progress(time.Now(), procTable)
}

// procTable reads the process table; a variable so tests can feed a synthetic one.
var procTable = procx.Snapshot

// progress is the newest sign of life. A child that cannot be located in the process table
// (no process, an unreadable /proc) is "could not observe", not "no tool": the stale stamp then
// gives no answer and the row keeps holding.
func (h *threadHandle) progress(now time.Time, table func() map[int]procx.Info) (time.Time, bool) {
	return h.act.Progress(now, func() (alive, observed bool) {
		h.mu.Lock()
		cmd := h.cmd
		h.mu.Unlock()
		if cmd == nil || cmd.Process == nil {
			return false, false
		}
		tab := table()
		if _, ok := tab[cmd.Process.Pid]; !ok {
			return false, false
		}
		callsAlive, callsKnown := h.calls.State()
		return callsAlive || procx.ToolProcessIn(cmd.Process.Pid, tab), callsKnown
	})
}

// noteToolCall keeps the set of open ACP tool calls. A tool that is not a local process (an MCP
// server's, or work done inside a resident helper) leaves nothing in the process table, so the
// protocol's own status is the only trace of it.
func (h *threadHandle) noteToolCall(sessionUpdate, id, status string) {
	switch status {
	case "completed", "failed", "cancelled":
		h.calls.Done(id)
	default:
		if sessionUpdate == "tool_call" || status == "in_progress" {
			h.calls.Add(id)
		}
	}
}
