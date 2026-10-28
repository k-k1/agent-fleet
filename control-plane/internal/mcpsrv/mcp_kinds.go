package mcpsrv

import (
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/agentkinds"
)

// toolKinds is the kind catalog in the order the tool texts list it: claude, the default kind,
// first, then the catalog's own order. The enumerations a model reads are built from the
// catalog rather than kept by hand, because a kind left out of a hand list is one no model
// ever starts — and a hand list drifts from the Agent's copy (#2857).
func toolKinds() []agentkinds.Entry {
	all := agentkinds.All()
	out := make([]agentkinds.Entry, 0, len(all))
	for _, e := range all {
		if e.Kind == "claude" {
			out = append([]agentkinds.Entry{e}, out...)
		} else {
			out = append(out, e)
		}
	}
	return out
}

// kindNames is the toolKinds names that keep says yes to.
func kindNames(keep func(agentkinds.Entry) bool) []string {
	var out []string
	for _, e := range toolKinds() {
		if keep(e) {
			out = append(out, e.Kind)
		}
	}
	return out
}

// isTerminalKind is a kind that runs a terminal, not an agent: it has no model to list.
func isTerminalKind(kind string) bool { return kind == "shell" || kind == "ssm" }

// modelKinds is every kind list_models answers for: each one that runs an agent.
func modelKinds() []string {
	return kindNames(func(e agentkinds.Entry) bool { return !isTerminalKind(e.Kind) })
}

// createKinds is every kind create_session offers: all of them but ssm, which needs an
// ssm_target the tool has no parameter for.
func createKinds() []string {
	return kindNames(func(e agentkinds.Entry) bool { return e.Kind != "ssm" })
}

// joinKinds joins kinds with sep, marking claude with defaultMark ("" for none).
func joinKinds(kinds []string, sep, defaultMark string) string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k
		if k == "claude" {
			out[i] += defaultMark
		}
	}
	return strings.Join(out, sep)
}

// listKinds is kinds as an English list: "a, b or c".
func listKinds(kinds []string, last string) string {
	if len(kinds) < 2 {
		return strings.Join(kinds, "")
	}
	return strings.Join(kinds[:len(kinds)-1], ", ") + " " + last + " " + kinds[len(kinds)-1]
}

// managedKindsSentence tells the caller which kinds create_session starts on the managed
// driver: the managed-default kinds that also have a Terminal route, then the managed-only ones.
func managedKindsSentence() string {
	byDefault := kindNames(func(e agentkinds.Entry) bool { return e.ManagedDefault && !e.ManagedOnly })
	only := kindNames(func(e agentkinds.Entry) bool { return e.ManagedOnly })
	return listKinds(byDefault, "and") + " sessions default to the managed driver, not TUI; " +
		listKinds(only, "and") + " are always managed."
}
