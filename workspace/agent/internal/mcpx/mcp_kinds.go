package mcpx

import (
	"slices"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// KindInfo is one registered kind, the facts the tool texts enumerate. It is the registry's
// answer (sessionx), the same one the kind catalog (agentkinds/catalog.json) is generated from.
type KindInfo struct {
	Kind string
	// Default: the kind an empty kind means (claude).
	Default bool
	// ManagedOnly: no Terminal (CLI) route at all.
	ManagedOnly bool
	// ManagedDefault: an MCP create_session asks for the managed driver (ManagedOnly included).
	ManagedDefault bool
}

// toolKinds is the registry in the order the tool texts list it: the default kind first, then
// the registry's own order. An enumeration a model reads is one it launches from, so it is
// built from the registry rather than kept by hand: a kind left out of a hand list is one no
// model ever starts (muse fell out of the operator's create_session list that way).
func toolKinds(keep func(KindInfo) bool) []KindInfo {
	all := deps.Kinds()
	out := make([]KindInfo, 0, len(all))
	for _, k := range all {
		if !keep(k) {
			continue
		}
		if k.Default {
			out = append([]KindInfo{k}, out...)
		} else {
			out = append(out, k)
		}
	}
	return out
}

// isTerminalKind is a kind that runs a terminal, not an agent: it has no model to list.
func isTerminalKind(kind string) bool {
	return slices.Contains([]string{session.KindShell, session.KindSSM}, kind)
}

// modelKinds is every kind list_models answers for: each one that runs an agent.
func modelKinds() []KindInfo {
	return toolKinds(func(k KindInfo) bool { return !isTerminalKind(k.Kind) })
}

// kindNames is the names of kinds, in order.
func kindNames(kinds []KindInfo) []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.Kind
	}
	return out
}

// joinKinds joins the names of kinds with sep, marking the default kind with defaultMark ("" for
// none). A full-width mark takes no space before the next separator, as Japanese text is written.
func joinKinds(kinds []KindInfo, sep, defaultMark string) string {
	var b strings.Builder
	for i, k := range kinds {
		if i > 0 {
			if strings.HasSuffix(b.String(), "）") {
				b.WriteString(strings.TrimLeft(sep, " "))
			} else {
				b.WriteString(sep)
			}
		}
		b.WriteString(k.Kind)
		if k.Default {
			b.WriteString(defaultMark)
		}
	}
	return b.String()
}
