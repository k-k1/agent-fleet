package projcfg

// edittoml.go — format-preserving edits to codex's .codex/config.toml
// (docs/56 §6). Unlike editjson.go this needs no new scanning logic of its own:
// mcpreg's materialize_codex.go already implements exactly this edit (strip the
// named table(s), append fresh ones) for the USER scope, and its two primitives
// (StripCodexServers / AppendTOMLBlocks, project_codex.go) take only a name
// predicate and pre-rendered block text — no ServerDef in sight — so this file is
// a thin reuse rather than a second implementation that could drift from the one
// af's own writer already exercises.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"

// UpsertCodexBlock replaces name's `[mcp_servers.<name>]` block (and any
// sub-tables) in src with block — a fully rendered `[mcp_servers.<name>]…`
// fragment, newline-free at its edges — appending it fresh if name was not
// already present. Every other byte (comments, other tables, a
// `[projects."…"]` trust section) is preserved.
func UpsertCodexBlock(src, name, block string) string {
	stripped := mcpreg.StripCodexServers(src, func(n string) bool { return n == name })
	return mcpreg.AppendTOMLBlocks(stripped, []string{block})
}

// DeleteCodexBlock removes name's `[mcp_servers.<name>]` block from src, if
// present. A no-op otherwise.
func DeleteCodexBlock(src, name string) string {
	return mcpreg.StripCodexServers(src, func(n string) bool { return n == name })
}
