// Package kinds is the list of session kinds the Agent registers, in registration order.
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

// registered is every kind, in registration order. A loop over the registry runs in this
// order, so moving a line can change behaviour: the managed kinds appear in the order main.go
// reconciles them and shutdown.go aborts and shuts them down (opencode, codex, copilot, cursor,
// kiro, lcpp, muse).
var registered = [...]entry{
	{session.KindClaude, "Claude Code"},
	{session.KindOpencode, "opencode"},
	{session.KindCodex, "Codex"},
	{session.KindCopilot, "GitHub Copilot"},
	{session.KindCursor, "Cursor"},
	{session.KindKiro, "Kiro"},
	{session.KindAgy, "Antigravity"},
	{session.KindLcpp, ""},
	{session.KindMuse, "Muse Code"},
	{session.KindShell, ""},
	{session.KindSSM, ""},
}

// entry is one registered kind. label is its outward product name, as the notification
// bridge writes it; the Console's agent registry (console/src/agents/registry.ts) is the naming
// source of truth, so keep the two in sync. "" means the kind has none and is shown by its id.
type entry struct {
	kind, label string
}

// All returns every registered kind in registration order, as a slice the caller may keep.
func All() []string {
	out := make([]string, len(registered))
	for i, e := range registered {
		out[i] = e.kind
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
	return e.label
}

func find(kind string) (entry, bool) {
	for _, e := range registered {
		if e.kind == kind {
			return e, true
		}
	}
	return entry{}, false
}
