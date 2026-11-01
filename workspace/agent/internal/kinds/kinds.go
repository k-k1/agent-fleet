// Package kinds reads the list of session kinds the Agent registers (session.KindTable), in registration order.
//
// It holds names and their product labels only: no agents.Agent and no capability. That is
// what lets every package import it, including bridge and mcpreg, which the agent packages
// themselves import. The
// kind → agents.Agent map that carries the capabilities is internal/agentreg, one level up,
// because it has to import every agent package; its tests hold its kinds equal to this list.
//
// Keep the imports to internal/session (for the constants): a package this one depends on
// can never consult it, and kinds_test.go fails when bridge or mcpreg could not.
package kinds

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// All returns every registered kind in registration order, as a slice the caller may keep.
func All() []string {
	table := session.KindTable()
	out := make([]string, len(table))
	for i, e := range table {
		out[i] = e.Kind
	}
	return out
}

// Known reports whether kind is registered. The match is exact: "Codex", " codex" and
// "kiro-cli" are not kinds.
func Known(kind string) bool {
	_, ok := find(kind)
	return ok
}

// Label is kind's outward product name ("Claude Code", "GitHub Copilot"), or "" for a kind
// that has none and for anything that is not a registered kind.
func Label(kind string) string {
	e, _ := find(kind)
	return e.Label
}

func find(kind string) (session.KindInfo, bool) {
	for _, e := range session.KindTable() {
		if e.Kind == kind {
			return e, true
		}
	}
	return session.KindInfo{}, false
}
