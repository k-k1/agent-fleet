package mcpreg

// Exports for docs/log/56 P1's mcpproj to write .codex/config.toml the SAME way
// materialize_codex.go already does — a line-based edit, not a parse→re-emit
// round trip, because config.toml carries comments and [projects."…"] trust
// sections a generic TOML encoder would silently reformat away (docs/log/48 §8.2).
//
// These are pure syntax helpers (TOML string/array/table escaping, table-name
// stripping by NAME predicate) with no ServerDef in their signature, so exporting
// them does not risk mcpproj's project-scope entries leaking into the registry's
// own composition (ADR0040 decision 15) — only the *spelling* is shared, same as
// project_spelling.go's JSON-side table.

// StripCodexServers is stripCodexServers (materialize_codex.go): removes every
// `[mcp_servers.<name>]` table (and its sub-tables) whose name satisfies drop,
// preserving every other byte (comments, [projects."…"] trust sections, spacing).
func StripCodexServers(src string, drop func(name string) bool) string {
	return stripCodexServers(src, drop)
}

// AppendTOMLBlocks is appendTOMLBlocks (materialize_codex.go): appends blocks at
// the end of src, one blank line apart.
func AppendTOMLBlocks(src string, blocks []string) string {
	return appendTOMLBlocks(src, blocks)
}

// TOMLString is tomlStr (attach.go): quotes and escapes s as a TOML basic string.
func TOMLString(s string) string { return tomlStr(s) }

// TOMLStringArray is tomlStrArray (attach.go): renders vals as a TOML string array.
func TOMLStringArray(vals []string) string { return tomlStrArray(vals) }

// TOMLKey is tomlKey (materialize_codex.go): a bare key when the charset allows,
// a quoted string otherwise (a header name may contain characters nameRe forbids
// for a server NAME but that TOML still needs quoted as a table key).
func TOMLKey(k string) string { return tomlKey(k) }

// SortedStringMapKeys is sortedKeys (attach.go), for callers building their own
// TOML tables that must render in a deterministic (and therefore diff-stable) key
// order.
func SortedStringMapKeys(m map[string]string) []string { return sortedKeys(m) }
