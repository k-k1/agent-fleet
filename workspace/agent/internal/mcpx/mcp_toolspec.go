package mcpx

import "encoding/json"

// toolSpec is one MCP tool, declared once in its family's file: the schema each surface
// advertises it with, the call-side gate, how its arguments are decoded, and the handler. The
// advertised lists (mcpStdioToolList), the argument check (mcpCheckCallArgs) and the dispatch
// (mcpStdioCall) are all derived from it (ADR 0124 Phase 2), so adding a tool means adding a
// spec to its family and, for a session-advertised tool, naming it in that family's session
// list.
type toolSpec struct {
	name string
	// operator is the tool as the assistant surface advertises it; nil = never offered there.
	// Its text is written for the one assistant that reads it and stays Japanese.
	// operatorWrite holds it back to --write.
	operator      func() map[string]any
	operatorWrite bool
	// session is the tool as the session surface advertises it; nil = never offered there. It is
	// a separate text from operator on purpose: a session-advertised description is a fixed cost
	// on every session's first turn (measured 40% cheaper in English, b367ae51), and the
	// operator's text points at tools a session does not get. Which flag offers it is decided by
	// the family list that names it in mcpStdioToolList.
	session func() map[string]any
	// refuse is the call-side gate, asked after the arguments were checked: a non-empty answer is
	// the refusal the model reads, and nothing has been done by then. The advertised set is the
	// scope boundary, so a gate refuses what its surface would not advertise — a client that
	// guesses a name must not reach the handler.
	refuse func() string
	// ownArgs marks a handler that decodes p.Args itself (or forwards it verbatim), so its
	// argument TYPES are not mcpCallArgs' business (it is still filled, best effort, for the
	// fields the handler does read): running the union decode over it would refuse a legitimate
	// call whose field shares a name with a union field of another type.
	ownArgs bool
	handle  func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte
}

// toolCallParams is what a handler gets of tools/call's params: the tool name, and the
// arguments as the client wrote them with the caller stamp already taken out.
type toolCallParams struct {
	Name string
	Args json.RawMessage
}

// toolFamilies is every tool, family by family. The assistant surface advertises its tools in
// this order, the read-only ones first (operatorTools), and that order is part of the
// tools/list golden. A function rather than a variable: the handlers reach back into the list
// derivation, which a package-level initializer would turn into an initialization cycle.
func toolFamilies() [][]toolSpec {
	return [][]toolSpec{
		chromiumTools(),
		fleetObserveTools(),
		fleetSpawnTools(),
		chatPlanTools(),
		memoTools(),
		scheduleTools(),
		driveTools(),
		memorySnapshotTools(),
		assistantTools(),
		selfReportTools(),
		peerTools(),
		branchTools(),
		agentMemoryTools(),
		sessionSearchTools(),
		// The schema depends on the live offer; dispatch needs only the name and the handler.
		imageGenTools(imageGenOffer{}),
		studioTools(),
	}
}

// toolNamed is the spec the dispatch runs for name.
func toolNamed(name string) (toolSpec, bool) {
	for _, family := range toolFamilies() {
		for _, spec := range family {
			if spec.name == name {
				return spec, true
			}
		}
	}
	return toolSpec{}, false
}

// toolOwnsArgs reports whether name's handler decodes its own arguments (toolSpec.ownArgs).
func toolOwnsArgs(name string) bool {
	spec, ok := toolNamed(name)
	return ok && spec.ownArgs
}

// operatorTools is the assistant surface's list: every read-only operator tool, then, when
// write is set, every --write one, each in toolFamilies order.
func operatorTools(write bool) []map[string]any {
	var out []map[string]any
	for _, wantWrite := range []bool{false, true} {
		if wantWrite && !write {
			break
		}
		for _, family := range toolFamilies() {
			for _, spec := range family {
				if spec.operator != nil && spec.operatorWrite == wantWrite {
					out = append(out, spec.operator())
				}
			}
		}
	}
	return out
}

// sessionTools is the session-surface schema of each spec that has one, in order.
func sessionTools(specs []toolSpec) []map[string]any {
	var out []map[string]any
	for _, spec := range specs {
		if spec.session != nil {
			out = append(out, spec.session())
		}
	}
	return out
}

// sessionToolsNamed is the session-surface schema of each named tool, for a family list that
// offers tools declared in other families (the operator's own read tools a session also gets).
// A name with no session schema is a programming error the tools/list golden catches.
func sessionToolsNamed(names ...string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		if spec, ok := toolNamed(name); ok && spec.session != nil {
			out = append(out, spec.session())
		}
	}
	return out
}
