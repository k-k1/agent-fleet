package kiro

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/acp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var _ agents.ManagedProgressReporter = managedDriver{}

// ManagedProgress: the ACP child sends nothing while a tool runs (measured on 2.31.0: no
// message for 112 s of a 120 s `sleep`), but the tool is a process under that child, in the
// child's own process group (not a session leader, so ToolProcessIn would miss it), and
// procx.GroupToolIn sees it for as long as it lives. The runtime is `kiro-cli` and its
// `kiro-cli-chat` child, which share the group too.
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
	}, table, kiroToolIn)
}

// kiroToolIn finds a tool in the child's process group (ManagedProgress says why).
func kiroToolIn(pid int, tab map[int]procx.Info) bool { return procx.GroupToolIn(pid, tab, "kiro-cli") }

// noteToolCall keeps the set of open ACP tool calls (acp.NoteToolCall).
func (h *threadHandle) noteToolCall(sessionUpdate, id, status string) {
	acp.NoteToolCall(&h.calls, sessionUpdate, id, status)
}
