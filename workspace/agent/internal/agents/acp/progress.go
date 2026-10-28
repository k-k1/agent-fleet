package acp

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

// Progress is the newest sign of life of an ACP child whose pid reports. The child sends nothing
// while a tool runs, but the tool is a process under it, which toolIn finds in the process table
// (the kinds differ in how the tool sits under the child). A child that cannot be located in the
// process table (no process, an unreadable /proc) is "could not observe", not "no tool": the
// stale stamp then gives no answer and the row keeps holding.
func Progress(act *agents.Activity, calls *agents.InFlight, now time.Time, pid func() (int, bool),
	table func() map[int]procx.Info, toolIn func(pid int, tab map[int]procx.Info) bool) (time.Time, bool) {
	return act.Progress(now, func() (alive, observed bool) {
		p, ok := pid()
		if !ok {
			return false, false
		}
		tab := table()
		if _, ok := tab[p]; !ok {
			return false, false
		}
		callsAlive, callsKnown := calls.State()
		return callsAlive || toolIn(p, tab), callsKnown
	})
}

// NoteToolCall keeps the set of open ACP tool calls. A tool that is not a local process (an MCP
// server's, or work done inside a resident helper) leaves nothing in the process table, so the
// protocol's own status is the only trace of it.
func NoteToolCall(calls *agents.InFlight, sessionUpdate, id, status string) {
	switch status {
	case "completed", "failed", "cancelled":
		calls.Done(id)
	default:
		if sessionUpdate == "tool_call" || status == "in_progress" {
			calls.Add(id)
		}
	}
}
