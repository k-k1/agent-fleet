package claude

import (
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Ledger of slot sid → the session id claude is actually writing the conversation under.
//
// We always launch claude with `--session-id <deterministic sid>`, so normally this ledger
// stays empty and both the transcript's location and the status key can remain the
// deterministic sid. It only diverges when claude recreates its session itself:
//
//	Several operations make claude relaunch itself (switching to the full-screen TUI with
//	/tui, the restart after sign-in, a model switch). That relaunch argv is rebuilt from
//	the configuration flags alone, and --session-id and --name structurally cannot appear
//	in it. Measured on 2.1.239: the live process's argv was
//	  claude.exe --allow-dangerously-skip-permissions --model opus --permission-mode bypassPermissions
//	while the launch command had both --session-id and --name. Having lost --session-id,
//	claude starts a blank conversation under a new random id, so the deterministic sid's
//	jsonl never appears again. Watching only the deterministic sid leaves the mirror stuck
//	at "no conversation yet" while hook-derived status is written under the other id, and
//	the session vanishes entirely from the Console.
//
// The deterministic sid stays AF's slot key (status, pending payloads, pasted images, …)
// and only "which id claude is writing under right now" is tracked here, with the same
// mechanism (agents.SidStore) codex/opencode have had from the start.
var sids = agents.NewSidStore("claude-sid")

// LiveSID returns the session id claude is actually writing under for our slot —
// slot itself in the normal case.
//
// The ledger is consulted before the deterministic sid's log so that the live conversation
// wins even when a jsonl for the deterministic sid appeared by some other route after a
// drift. When the ledger's value points at no log it falls back to slot silently, so a
// stale entry does no harm.
func LiveSID(slot string) string {
	if live := sids.Read(slot); live != "" && live != slot && len(rawJSONLPaths(live)) > 0 {
		return live
	}
	return slot
}

// NormalizeHookSID maps the session_id a claude hook announced back onto our slot sid,
// recording the mapping when claude has drifted onto an id of its own.
//
// The clue is AF_SESSION_NAME. It is handed over as the tmux session's env
// (session_tmux.go), so it survives into hooks running as children even when claude
// relaunches itself (measured: the post-drift process still had AF_SESSION_NAME). Unlike
// guesswork such as matching cwd, this cannot mistake one session for another. A claude
// outside AF's control (one the user started themselves) has no AF_SESSION_NAME, so its
// hooks pass straight through.
func NormalizeHookSID(live string) string {
	slot := hookSlotSID()
	if live == "" || slot == "" {
		return live
	}
	if slot == live {
		// No drift. Clear any earlier entry: once a relaunch has put --session-id back in
		// effect, still pointing at the old id would resume that one instead.
		if sids.Read(slot) != "" {
			sids.Remove(slot)
		}
		return live
	}
	sids.Write(slot, live)
	return slot
}

// hookSlotSID resolves the slot sid of the session this hook process belongs to.
func hookSlotSID() string {
	name := os.Getenv("AF_SESSION_NAME")
	if name == "" {
		return ""
	}
	m, ok := session.ReadMeta(name)
	if !ok || m.Kind != session.KindClaude {
		return ""
	}
	return session.UUID(m.Dir, m.Name)
}
