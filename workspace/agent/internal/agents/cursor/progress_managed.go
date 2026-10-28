package cursor

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/acp"
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

// progress is the newest sign of life (acp.Progress).
func (h *threadHandle) progress(now time.Time, table func() map[int]procx.Info) (time.Time, bool) {
	return acp.Progress(&h.act, &h.calls, now, func() (int, bool) {
		h.mu.Lock()
		cmd := h.cmd
		h.mu.Unlock()
		if cmd == nil || cmd.Process == nil {
			return 0, false
		}
		return cmd.Process.Pid, true
	}, table, procx.ToolProcessIn)
}

// noteToolCall keeps the set of open ACP tool calls (acp.NoteToolCall).
func (h *threadHandle) noteToolCall(sessionUpdate, id, status string) {
	acp.NoteToolCall(&h.calls, sessionUpdate, id, status)
}
