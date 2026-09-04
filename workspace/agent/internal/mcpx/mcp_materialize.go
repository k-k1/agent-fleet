package mcpx

// MCP wiring for sessions (docs/log/48 P3, extended to every kind in P5). Materializes the
// `targets.session` definitions of the effective registry into each CLI's own global config.
// What gets written lives in internal/mcpreg/materialize_*.go; this file only owns WHEN it is
// written.
//
// Three triggers (docs/log/48 §8.3):
//   - agent startup (so a hand-run CLI is covered from the moment the container wakes)
//   - just before a session launches (register -> new session is the shortest path)
//   - on a registry change (CRUD)
//
// It does not affect sessions that are already running: every CLI reads its config at startup.
// The Console says so explicitly ("takes effect from the next session",
// mcp.session_restart_note).

import (
	"log"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// StartManagedSession is the managed-driver counterpart of startSessionTmux's
// materialize hook: Resume() is what LAUNCHES a managed session, so the config has to
// be current first. Only the launch sites go through here — the many Resume() calls
// that merely re-attach to a running thread (turn send, bridge, answer) must not pay
// for a registry read on every message.
func StartManagedSession(d agents.Driver, m session.Meta) (agents.ThreadHandle, error) {
	Materialize(m.Kind)
	ensureClaudeSettingsWiring(m.Kind) // see session_status.go: repairs a stale hook/statusLine path
	return d.Resume(m)
}

// Materialize writes the registry into one kind's native CLI config. Failures are
// logged, never fatal: a session must still launch when its MCP config could not be
// updated (the user gets the previously written set, which is the same thing a
// stopped CP gives them for tenant rows).
func Materialize(kind string) {
	logMaterializeMCP([]mcpreg.MaterializeResult{mcpreg.Materialize(kind)})
}

// MaterializeAll writes every implemented kind — used at boot and after a
// registry change, where "which kind" isn't yet known.
func MaterializeAll() {
	logMaterializeMCP(mcpreg.MaterializeAll())
}

func logMaterializeMCP(res []mcpreg.MaterializeResult) {
	for _, r := range res {
		switch {
		case r.Err != "":
			log.Printf("mcp materialize %s: %s", r.Kind, r.Err)
		case r.Changed:
			log.Printf("mcp materialize %s: %d server(s), removed %v", r.Kind, len(r.Written), r.Removed)
		}
	}
}
