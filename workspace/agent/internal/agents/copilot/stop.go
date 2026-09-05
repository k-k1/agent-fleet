package copilot

// Graceful stop for the TUI route. copilot appends to events.jsonl live, so it has none of
// agy's "lose the resume ID unless you /exit" constraint, but /exit does release the inuse
// lock and commit the session checkpoint (measured: it prints an exit summary), so it is
// tried once before the kill. While a pending menu (permission / plan) is up, Enter would
// CONFIRM the highlighted row — the same risk agy demonstrated on real hardware — so it is
// dismissed with Escape first.

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// gracefulStopWindow is how long we wait for copilot to exit after /exit before
// the caller falls back to kill-session. Idle exit is fast; a mid-turn TUI may
// ignore the command and time out.
const gracefulStopWindow = 4 * time.Second

func (agentImpl) GracefulStop(m session.Meta) bool {
	tn := session.TmuxName(m.Name)
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return false
	}
	if LiveState(m) == "question" {
		// A permission menu is open: dismiss it with Escape before Enter can approve it.
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Escape").Run()
		time.Sleep(300 * time.Millisecond)
	}
	// C-u first: a draft in the composer would otherwise be submitted as
	// "<draft>/exit". Enter is a separate keystroke (measured: an Enter in the same send-keys
	// is swallowed by paste folding and never submits).
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-u").Run()
	_ = tmuxx.Cmd("send-keys", "-t", pane, "-l", "/exit").Run()
	time.Sleep(300 * time.Millisecond)
	_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
	deadline := time.Now().Add(gracefulStopWindow)
	for time.Now().Before(deadline) {
		if !tmuxx.HasSession(tn) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
