package agy

// agy writes the cwd -> conversation map (cache/last_conversations.json, the immediate
// source of the resume UUID) ONLY on a graceful exit — measured on v1.1.4 through the
// integration E2E. An earlier observation that it is written on the first prompt was an
// artefact of `-p` runs ending the process per prompt; in a resident TUI session only the
// conversation DB appears up front and the map stays stale until /exit. Killing the pane
// with kill-session therefore loses the UUID for good, so send /exit before stopping to
// give it a chance to flush (agents.GracefulStopper).

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// gracefulStopWindow is how long we wait for agy to exit after /exit before the
// caller falls back to kill-session. The flush is a small JSON write — exit is
// fast when the TUI is idle; a mid-turn TUI may ignore the command and time out.
const gracefulStopWindow = 4 * time.Second

func (agentImpl) GracefulStop(m session.Meta) bool {
	tn := session.TmuxName(m.Name)
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return false
	}
	// A pending interactive prompt (ASK_QUESTION / permission menu) swallows the
	// "/exit" text but its Enter CONFIRMS the highlighted first row — halting a
	// session mid-permission silently APPROVED the tool call (demonstrated on a real
	// machine: halting while a prompt was pending approved a file creation). Escape
	// dismisses either menu (question:
	// Skip, permission: cancel) without choosing, so clear the modal first.
	if st, _ := Probe(m); st != "" {
		_ = tmuxx.Cmd("send-keys", "-t", pane, "Escape").Run()
		time.Sleep(300 * time.Millisecond)
	}
	// C-u first: a draft sitting in the input box would otherwise be submitted
	// as "<draft>/exit" — a quota-burning prompt instead of an exit.
	_ = tmuxx.Cmd("send-keys", "-t", pane, "C-u").Run()
	_ = tmuxx.Cmd("send-keys", "-t", pane, "-l", "/exit").Run()
	// Enter as a separate keystroke after a beat (same Ink/bubbletea quirk as the
	// auth/usage flows: a combined write can swallow the CR).
	time.Sleep(300 * time.Millisecond)
	_ = tmuxx.Cmd("send-keys", "-t", pane, "Enter").Run()
	deadline := time.Now().Add(gracefulStopWindow)
	for time.Now().Before(deadline) {
		if !tmuxx.HasSession(tn) {
			// The exit flushed the map — adopt the conversation UUID right now
			// rather than waiting for a poll that may never come (stop forgets
			// the meta immediately).
			captureConversation(m)
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
