package opencode

// ReadOnlyTools lists the tool names of this kind's transcript that only read: the cross-agent
// move (ADR 0115 decision 7) drops a call to one of them outside the last turns, because
// running it again on the target costs nothing. A name that is missing here is kept, so a
// renamed or new tool errs toward keeping a line, never toward hiding a side effect.
// Names are matched exactly.
//
// Source: opencode's built-in tool ids (lower case); bash, edit, write and patch change things.
// websearch is search-only, like webfetch (opencode 2.0.28 tool list in v2state.go).
var ReadOnlyTools = []string{"read", "glob", "grep", "list", "webfetch", "websearch"}
