package agents

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// UniqueSlotForSessionID maps a provider-side session id (the one a caller stamps on an MCP
// call) to the one AF slot that owns it. The id is only a key: AF's own slot mapping decides,
// never the caller's claim, and anything but exactly one match answers ok=false so the caller
// falls back to a weaker identity instead of guessing between two slots.
//
// slotID reads a slot's mapped id ("" for a slot that cannot match) and is where a caller puts
// its cheap pre-filters (kind, working directory), because it runs for every non-archived,
// validly named meta until a second match makes the answer ambiguous.
// accept runs only on a slot whose id already matched, for the checks that cost a probe; nil
// accepts all. A rejected match does not count towards ambiguity.
func UniqueSlotForSessionID(metas []session.Meta, id string, slotID func(session.Meta) string, accept func(session.Meta) bool) (string, bool) {
	if id == "" {
		return "", false
	}
	var name string
	for _, m := range metas {
		if m.Archived || !session.ValidName(m.Name) {
			continue
		}
		if slotID(m) != id {
			continue
		}
		if accept != nil && !accept(m) {
			continue
		}
		if name != "" {
			return "", false
		}
		name = m.Name
	}
	return name, name != ""
}
