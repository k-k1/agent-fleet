package harness

// BuiltinTools returns segment E's own tool set (ADR 0093 decision 5 / docs/log/99
// §4.6): read, write, edit, glob, grep, ls, bash, ask_user, todo_write. A caller
// builds a Registry from these (NewRegistry(BuiltinTools()...)), optionally adding
// more Tool values for segment F's resolved MCP tools in a later phase.
func BuiltinTools() []Tool {
	return []Tool{
		{Def: readToolDef, Run: runRead},
		{Def: writeToolDef, Mutates: true, Run: runWrite},
		{Def: editToolDef, Mutates: true, Run: runEdit},
		{Def: lsToolDef, Run: runLs},
		{Def: globToolDef, Run: runGlob},
		{Def: grepToolDef, Run: runGrep},
		{Def: bashToolDef, Mutates: true, Run: runBash},
		{Def: askUserToolDef, Run: runAskUser},
		{Def: todoWriteToolDef, Run: runTodoWrite},
	}
}
