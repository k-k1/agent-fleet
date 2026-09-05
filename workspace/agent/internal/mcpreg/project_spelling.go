package mcpreg

// Project-scoped MCP (docs/log/56) reads files inside a repository from its own package,
// internal/mcpproj. The types (ServerDef, Origin, Targets, the ledger) are deliberately not
// shared with it (ADR0040 decision 15): this side describes what af hands out and rejects
// reserved names, while mcpproj's job is the opposite — find a reserved name and warn about
// it — and one shared type risks those entries leaking into the effective registry.
//
// The per-kind spelling (what each JSON member is called) is the one thing that does drift
// if it is not shared. This file is that single table: the knowledge the *Servers builders
// (attach.go) and materialize_<kind>.go already embody, exported so the reading side can
// reuse it (docs/log/56 §4.2). codex is out of scope — it is a TOML table, not a JSON
// member; use CodexServerTableName.

import (
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// JSONEntrySpelling describes how one agent kind spells a single MCP server entry
// inside a JSON document holding one map of entries — the same shape jsonConfig
// (materialize_json.go) writes. mcpproj's parser reads this table instead of
// hard-coding key names a second time.
type JSONEntrySpelling struct {
	// ServersKey is the top-level member holding the server map.
	ServersKey string
	// CommandKey holds the stdio command. When ArgsFolded, it holds command+args
	// together as one array (opencode); otherwise it is a bare string and Args is
	// a separate array member named ArgsKey (unused when ArgsFolded).
	CommandKey string
	ArgsFolded bool
	// EnvKey holds the stdio environment map ("env" almost everywhere, opencode's
	// "environment").
	EnvKey     string
	URLKey     string
	HeadersKey string
	// TypeKey discriminates stdio vs http. Empty means the kind has no discriminator
	// member and a reader must infer the transport structurally (URLKey present vs
	// CommandKey present) — true for cursor and kiro (materialize_cursor.go /
	// materialize_kiro.go).
	TypeKey   string
	TypeStdio string
	TypeHTTP  string
	// AlwaysEnabled: this kind's own writer always states an explicit "enabled":
	// true on every entry (opencode — OpencodeServers' comment: "opencode's
	// default, said out loud"). A P1 write matches what the CLI itself would
	// produce (docs/log/56 §6) rather than omit a key only some kinds expect.
	AlwaysEnabled bool
}

// JSONEntrySpellings is keyed by session.Kind*. Only kinds mcpproj parses as a
// generic "one JSON object of entries" file are present — codex (TOML) and agy (no
// project scope, docs/log/56 §4.3) are not.
//
// claude and copilot share ONE entry: both consume .mcp.json, and it is written in
// claude's own spelling (docs/log/56 §2.1 confirms copilot expands the same
// placeholders as claude when reading that shared file) — copilot's OWN spelling
// below (used for its native $COPILOT_HOME/mcp-config.json, materialize_copilot.go)
// only applies to a file mcpproj does not parse in v1.
var JSONEntrySpellings = map[string]JSONEntrySpelling{
	session.KindClaude: {
		ServersKey: "mcpServers", CommandKey: "command", EnvKey: "env",
		URLKey: "url", HeadersKey: "headers",
		TypeKey: "type", TypeStdio: "stdio", TypeHTTP: "http",
	},
	session.KindOpencode: {
		ServersKey: "mcp", CommandKey: "command", ArgsFolded: true, EnvKey: "environment",
		URLKey: "url", HeadersKey: "headers",
		TypeKey: "type", TypeStdio: "local", TypeHTTP: "remote",
		AlwaysEnabled: true,
	},
	session.KindCursor: {
		// No discriminator (cursorServers' comment: "af writes the
		// discriminator-free form, which is the one the parser actually branches on").
		ServersKey: "mcpServers", CommandKey: "command", EnvKey: "env",
		URLKey: "url", HeadersKey: "headers",
	},
	session.KindKiro: {
		// Same discriminator-free shape as cursor (materialize_kiro.go).
		ServersKey: "mcpServers", CommandKey: "command", EnvKey: "env",
		URLKey: "url", HeadersKey: "headers",
	},
}

// CopilotNativeSpelling is copilot's OWN entry shape (materialize_copilot.go),
// exported for documentation/future use even though v1's mcpproj parses
// .mcp.json using JSONEntrySpellings[session.KindClaude] instead (docs/log/56 §2.3).
var CopilotNativeSpelling = JSONEntrySpelling{
	ServersKey: "mcpServers", CommandKey: "command", EnvKey: "env",
	URLKey: "url", HeadersKey: "headers",
	TypeKey: "type", TypeStdio: "local", TypeHTTP: "http",
}

// IsReservedName reports whether name is one Agent Fleet itself occupies in a CLI's
// config: the fixed reserved words, or this boot's rotated af_xxxxxxxx shape.
// Exported for docs/log/56's mcpproj (§7.4), which must FIND this name in a project file
// and flag it red (the opposite of Validate, which REJECTS it from af's own
// registry) — reusing the same recognition here is the only way that stays in sync
// with af_server_name.go's rotation.
func IsReservedName(name string) bool {
	lower := strings.ToLower(name)
	return reservedNames[lower] || afNameRE.MatchString(lower)
}

// IsValidServerName reports whether name would pass Validate's own name check — the
// intersection of every target CLI's accepted server-key charset (nameRe's doc
// comment). mcpproj uses it to warn (not reject — a project file is the user's own)
// when a hand-written name falls outside that intersection (docs/log/56 §7.4).
func IsValidServerName(name string) bool {
	return nameRe.MatchString(name)
}

// CodexServerTableName is mcpServerTableName (materialize_codex.go), exported so
// mcpproj's .codex/config.toml reader recognizes `[mcp_servers.<name>]` /
// `[mcp_servers.<name>.env]` table headers exactly the way af's own writer does —
// both sides must agree on where one server's TOML ends, or a stray table would be
// silently dropped or merged into the wrong entry.
func CodexServerTableName(header string) string { return mcpServerTableName(header) }

// CodexTOMLHeaderRE is tomlHeaderRE (materialize_codex.go), exported for the same
// reason: mcpproj's line scanner must recognize a table header line identically to
// the writer that will one day edit the same file (P1).
var CodexTOMLHeaderRE = tomlHeaderRE
