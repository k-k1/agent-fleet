package sessionx

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// terminalTraits is kind's agents.TerminalTraits by exact registry key: zero for a kind without
// them and for a spelling that is no kind.
func terminalTraits(kind string) agents.TerminalTraits {
	a, _ := RegisteredAgent(kind)
	return traitsOf(a)
}

// normalizedTerminalTraits is terminalTraits through AgentOf's claude fallback, for the paths
// that read a stored session with an empty or legacy kind as claude (NormalizeKind): an old
// claude session with no kind must still be refused on an expired login.
func normalizedTerminalTraits(kind string) agents.TerminalTraits {
	return traitsOf(AgentOf(kind))
}

func traitsOf(a agents.Agent) agents.TerminalTraits {
	if t, ok := a.(agents.TerminalTraiter); ok {
		return t.TerminalTraits()
	}
	return agents.TerminalTraits{}
}

// terminalScreen is the terminal-only screen kind's pane shows right now ("" for none, for a
// kind that has no such screens, and when the pane cannot be read).
func terminalScreen(kind, name string) string {
	t := terminalTraits(kind)
	if t.TerminalScreen == nil {
		return ""
	}
	pane := tmuxx.SessionPaneID(session.TmuxName(name))
	if pane == "" {
		return ""
	}
	out, err := tmuxx.Cmd("capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	return t.TerminalScreen(string(out))
}

// callTrue calls a name probe trait, false when the kind has none.
func callTrue(f func(name string) bool, name string) bool { return f != nil && f(name) }

// callTrue0 calls a probe trait that takes nothing, false when the kind has none.
func callTrue0(f func() bool) bool { return f != nil && f() }

// healIdle clears a stale non-idle live state: the kind's own heal when it has one, else the
// marker is just removed.
func healIdle(t agents.TerminalTraits, sid string) {
	if t.HealIdle != nil {
		t.HealIdle(sid)
		return
	}
	status.Remove(sid)
}
