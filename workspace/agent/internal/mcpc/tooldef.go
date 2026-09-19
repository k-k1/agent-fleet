package mcpc

import "strings"

// toolNamePrefix/toolNameSep match the prefix mcpreg/af_server_name.go documents for
// how a CLIENT sees one of af's own tools (`mcp__<server>__<tool>`, afClientToolRE) —
// reused here so every server this client attaches to gets the same shape.
//
// Routing a prefixed name back to (server, tool) is deliberately NOT a generic split on
// the first "__": mcpreg's name grammar (def.go's nameRe) allows underscores inside a
// server name, so "mcp__my__server__do_thing" is genuinely ambiguous between server
// "my" tool "server__do_thing" and server "my__server" tool "do_thing" without knowing
// which names are actually registered. Manager knows the live server set, so it
// resolves the ambiguity by testing the CONCRETE registered name (toolNameForServer)
// rather than guessing from the string shape alone.
const toolNamePrefix = "mcp__"
const toolNameSep = "__"

// PrefixToolName builds the name a []harness.ToolDef carries for one server's tool.
func PrefixToolName(serverName, toolName string) string {
	return toolNamePrefix + serverName + toolNameSep + toolName
}

// toolNameForServer reports the bare tool name if prefixed was built by
// PrefixToolName(serverName, ...), or ok=false otherwise.
func toolNameForServer(prefixed, serverName string) (toolName string, ok bool) {
	prefix := PrefixToolName(serverName, "")
	rest, found := strings.CutPrefix(prefixed, prefix)
	if !found || rest == "" {
		return "", false
	}
	return rest, true
}
