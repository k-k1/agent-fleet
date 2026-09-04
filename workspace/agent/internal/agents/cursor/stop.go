package cursor

// Graceful stop for the TUI route. cursor appends to its JSONL live and resume is pinned
// to the UUID AF assigns, so there is no "exit or lose the resume id" constraint like
// agy's. But cursor-agent leaves a resident worker-server process behind after a turn
// (measured - docs/log/40), so before kill-session tears the pane down we try a proper
// exit once (two Ctrl+D presses - measured/docs) and let the CLI clean up after itself.
// Enter while a pending menu (permission/plan) is up approves the highlighted row (the
// same risk as copilot c639973), so dismiss it with Escape first.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// gracefulStopWindow is how long we wait for the exit after the two Ctrl+D presses. Idle
// exit is fast; a mid-turn TUI may ignore them, and the timeout makes the caller fall back
// to kill.
const gracefulStopWindow = 4 * time.Second

func (agentImpl) GracefulStop(m session.Meta) bool {
	tn := session.TmuxName(m.Name)
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return false
	}
	if LiveState(m) == "working" {
		// Interrupt an in-flight turn (Esc) before exiting.
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Escape").Run()
		time.Sleep(300 * time.Millisecond)
	}
	// C-u clears the composer draft: leftover text can make Ctrl+D mean something else.
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-u").Run()
	// Two Ctrl+D presses exit (measured: the first asks to confirm, the second exits).
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-d").Run()
	time.Sleep(300 * time.Millisecond)
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-d").Run()
	deadline := time.Now().Add(gracefulStopWindow)
	for time.Now().Before(deadline) {
		if !tmuxx.HasSession(tn) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
