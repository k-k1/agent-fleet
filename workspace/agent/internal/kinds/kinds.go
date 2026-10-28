// Package kinds is the list of session kinds the Agent registers, in registration order.
//
// It holds names only: no agents.Agent and no capability. That is what lets every package
// import it, including bridge and mcpreg, which the agent packages themselves import. The
// kind → agents.Agent map that carries the capabilities is internal/agentreg, one level up,
// because it has to import every agent package; its tests hold its kinds equal to this list.
//
// Keep the imports to internal/session (for the constants): a package this one depends on
// can never consult it, and kinds_test.go fails when bridge or mcpreg could not.
package kinds

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// registered is every kind, in registration order. A loop over the registry runs in this
// order, so moving a line can change behaviour: the managed kinds appear in the order main.go
// reconciles them and shutdown.go aborts and shuts them down (opencode, codex, copilot, cursor,
// kiro, lcpp, muse).
var registered = [...]string{
	session.KindClaude,
	session.KindOpencode,
	session.KindCodex,
	session.KindCopilot,
	session.KindCursor,
	session.KindKiro,
	session.KindAgy,
	session.KindLcpp,
	session.KindMuse,
	session.KindShell,
	session.KindSSM,
}

// All returns every registered kind in registration order, as a slice the caller may keep.
func All() []string {
	out := make([]string, len(registered))
	copy(out, registered[:])
	return out
}

// Known reports whether kind is registered. The match is exact: "Codex", " codex" and
// "kiro-cli" are not kinds.
func Known(kind string) bool {
	for _, k := range registered {
		if k == kind {
			return true
		}
	}
	return false
}
