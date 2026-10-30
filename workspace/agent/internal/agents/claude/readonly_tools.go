package claude

// ReadOnlyTools lists the tool names of this kind's transcript that only read: the cross-agent
// move (ADR 0115 decision 7) drops a call to one of them outside the last turns, because
// running it again on the target costs nothing. A name that is missing here is kept, so a
// renamed or new tool errs toward keeping a line, never toward hiding a side effect.
// Names are matched exactly.
//
// Source: the tool names claude writes into its jsonl (toolInfo in transcript.go).
// LS and NotebookRead are claude's directory listing and notebook reader. ToolSearch only
// loads deferred tool schemas into the context; it touches nothing, and the target has no use
// for the record of which schemas were loaded. Bash, Write, Edit, Skill, TaskStop, Workflow and
// every mcp__* tool stay out: their effects are not visible from the name.
var ReadOnlyTools = []string{"Read", "Grep", "Glob", "LS", "NotebookRead", "WebFetch", "WebSearch", "ToolSearch"}
