package muse

// ReadOnlyTools lists the tool names of this kind's transcript that only read: the cross-agent
// move (ADR 0115 decision 7) drops a call to one of them outside the last turns, because
// running it again on the target costs nothing. A name that is missing here is kept, so a
// renamed or new tool errs toward keeping a line, never toward hiding a side effect.
// Names are matched exactly.
//
// Source: the host names its tools (msp Item.Tool). read_file takes a path and changes nothing
// (fileedits_test.go pins it as a non-edit); bash, write_file, edit_file and mcp__* tools are
// never listed.
var ReadOnlyTools = []string{"read_file"}
