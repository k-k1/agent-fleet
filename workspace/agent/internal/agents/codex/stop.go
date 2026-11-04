package codex

// Stop for a Terminal codex slot. The turn (and any input queued behind a tool call) lives in
// codex's own per-user app-server daemon (CODEX_HOME/app-server-daemon, PPID 1), not in the
// pane, so tmux kill-session ends only the TUI and the turn can finish — and a queued prompt
// run — with nothing attached (docs/log/133 §1, issue #2607).
//
// That daemon is shared by every codex TUI on the same CODEX_HOME, so it is never signalled or
// stopped here: only this slot's thread is interrupted, through the daemon's own control
// socket. The thread id is the hook-captured one (sids), so another session's thread is never
// named.

import (
	"encoding/json"
	"log"
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// stopInterruptRounds bounds how often the thread is re-checked after an interrupt: a prompt
// the daemon had queued can start as the next turn the moment the first one is aborted.
const stopInterruptRounds = 3

// stopSettle is the pause between an interrupt and the re-check.
var stopSettle = 400 * time.Millisecond

// terminalControlAddr is the socket the Terminal TUI's daemon listens on.
func terminalControlAddr() string {
	return "unix://" + filepath.Join(paths.CodexHome(), "app-server-control", "app-server-control.sock")
}

// GracefulStop interrupts the slot's running turn on codex's daemon before the pane is
// killed; the caller still kills the pane (false).
func (agentImpl) GracefulStop(m session.Meta) bool {
	tid := sids.Read(session.UUID(m.Dir, m.Name))
	if tid == "" {
		return false
	}
	if err := interruptThread(terminalControlAddr(), tid); err != nil {
		log.Printf("codex: interrupt %s on stop: %v", m.Name, err)
	}
	return false
}

// interruptThread aborts the in-progress turn of threadID on the app-server at addr, and keeps
// aborting while a new one starts (up to stopInterruptRounds). No daemon, or a thread it does
// not know, has nothing running and is not an error worth more than a log line.
func interruptThread(addr, threadID string) error {
	cl, err := newAppClient(addr)
	if err != nil {
		return err
	}
	go cl.readLoop()
	defer cl.close()
	for i := 0; i < stopInterruptRounds; i++ {
		raw, err := cl.call("thread/turns/list", map[string]any{"threadId": threadID, "limit": 1, "sortDirection": "desc"}, 5*time.Second)
		if err != nil {
			return err
		}
		var res struct {
			Data []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &res) != nil || len(res.Data) == 0 || res.Data[0].Status != "inProgress" {
			return nil
		}
		if _, err := cl.call("turn/interrupt", map[string]any{"threadId": threadID, "turnId": res.Data[0].ID}, 10*time.Second); err != nil {
			return err
		}
		time.Sleep(stopSettle)
	}
	return nil
}
