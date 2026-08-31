// Package mcpproj is the "read" half of docs/log/56 (P0): gather one working copy's
// project-scope MCP server definitions — the files a CLI itself reads when it runs
// FROM that repo, as opposed to internal/mcpreg's user/global config — into one
// snapshot, and flag what an operator should worry about before ever reflecting a
// server from one file to another (P1's plan/apply).
//
// It deliberately shares NO type with internal/mcpreg (ADR0040 決定15,
// docs/log/56 §4.2). mcpreg.ServerDef means "Agent Fleet distributes this" and its
// Validate rejects af's own reserved names; this package does the opposite — it
// must FIND a project file defining "af" or "af_xxxxxxxx" and flag it red (docs/log/56
// §7.4, the hijack docs/log/48 §8.4 describes). Folding the two types together would
// let a project-file entry leak into the registry's effective-list composition and
// get distributed to the user scope, which is exactly the bug ADR0040 exists to
// prevent. What IS shared is the per-kind field SPELLING (mcpreg.JSONEntrySpellings
// etc.) — reusing the same knowledge mcpreg's own writers already encode, so the
// two sides cannot silently disagree on what a key is called.
//
// mcpproj has no write path yet (P1). Every function here only reads; nothing in
// this package is called from a session-launch or agent-startup path (docs/log/56 §3's
// axis separation — internal/mcpreg.Materialize* is the only writer with an
// automatic trigger, and it never looks inside a repo).
package mcpproj

// Transports. Independent constants from mcpreg's (same values today, but a
// project-file parser has no reason to import mcpreg's registry-flavored type just
// for two string literals — see the package doc on type sharing).
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// Placeholder dialects (docs/log/56 §2.1).
const (
	DialectDollarBrace    = "dollar_brace"     // ${VAR}
	DialectDollarEnvBrace = "dollar_env_brace" // ${env:VAR}
	DialectEnvBrace       = "env_brace"        // {env:VAR}
)

// Snapshot is one working copy's project-scope MCP servers, gathered across every
// kind's own file (docs/log/56 §4.1).
type Snapshot struct {
	// Repo is the display name (the ~/repos/<name> folder name), not an absolute
	// path — the Console never needs the container's filesystem layout.
	Repo     string `json:"repo"`
	VCS      string `json:"vcs"`
	Worktree bool   `json:"worktree"`
	Files    []File `json:"files"`
	// Kinds carries every agent kind's static facts (gate, dialect support, whether
	// it even has a project scope) regardless of whether that kind's file exists —
	// docs/log/57 憲章「無いものが消えると、無いのか未対応なのか分からない」.
	Kinds []KindInfo `json:"kinds"`
	// Warnings are cross-file findings (e.g. the same server name spelled two
	// different ways in two files) that do not belong to a single File.
	Warnings []Warning `json:"warnings,omitempty"`
}

// File is one project-scope config file inside the working copy.
type File struct {
	// Path is repo-relative (".mcp.json", "opencode.json", ".codex/config.toml", …).
	Path             string   `json:"path"`
	Kinds            []string `json:"kinds"` // agent kinds that read this file
	Exists           bool     `json:"exists"`
	Parsable         bool     `json:"parsable"` // false: exists but unreadable as its own format — never touched (docs/log/57 憲章3)
	Tracked          bool     `json:"tracked"`
	TrackedUncertain bool     `json:"trackedUncertain,omitempty"` // VCS cannot answer (svn / none) — docs/log/56 §7.2
	Ignored          bool     `json:"ignored"`
	Servers          []Server `json:"servers,omitempty"`
	// Note carries a raw parser error (untranslated — same convention as the
	// existing git-view endpoints returning gitx's own message verbatim) when
	// Parsable is false.
	Note string `json:"note,omitempty"`
}

// Server is one MCP server entry, normalized across kinds. Env/Headers VALUES are
// masked before this ever reaches the wire (docs/log/56 §7.3 / §4.1) — Console never
// receives a real secret through this endpoint, independent of the mcpreg registry's
// own masking.
type Server struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio" | "http"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	// Extra holds kind-specific keys the normalization above does not model (e.g.
	// copilot's "tools"/"timeout", kiro's "timeout", codex's
	// "startup_timeout_sec") — so a P1 write can round-trip them instead of
	// silently dropping a value in a file it did not create (docs/log/56 §4.1's Extra).
	Extra map[string]any `json:"extra,omitempty"`
}

// KindInfo is the static, content-independent facts docs/log/56 §8 / §2.1 measured for
// one agent kind's project scope — shown even when that kind's file is absent, so
// "no file" and "not supported" never look the same (docs/log/57 憲章「未検証バッジ」).
type KindInfo struct {
	Kind            string `json:"kind"`
	HasProjectScope bool   `json:"hasProjectScope"`
	// Unverified marks kiro: the mcp subcommand family requires a login this
	// container cannot provide, so its project-scope contract (merge? conflict
	// winner?) is assumed, not measured (docs/log/56 §4.3).
	Unverified bool `json:"unverified,omitempty"`
	// GateCode is what stands between "written" and "effective" for a freshly added
	// server, from docs/log/56 §2.2 / §8 — a code, not text, so the Console localizes it
	// (docs/log/48 P0-3's one-code-per-reason rule extended to informational codes).
	// "" | "approval" (claude/cursor) | "trust" (codex) | "none" (opencode/copilot).
	GateCode string `json:"gateCode,omitempty"`
	// Dialects are the placeholder syntaxes this kind expands, from docs/log/56 §2.1:
	// "dollar_brace" (${VAR}), "dollar_env_brace" (${env:VAR}), "env_brace"
	// ({env:VAR}). Empty means none (codex) or unmeasured (kiro).
	Dialects []string `json:"dialects,omitempty"`
}

// Warning is one thing docs/log/56 §7 says an operator should see before acting: a name
// hijack, a name outside the shared charset, a placeholder that will not expand
// where it is written, a secret-looking value already sitting in a tracked file, or
// the same server spelled two different ways across files. Every field beyond Code
// is a parameter for the Console's own localized template (err.<code> catalog,
// docs/log/48 P0-3) — mcpproj never emits pre-built prose, so it never leaks a
// server-language string into the other locale (docs/log/23 P0-3's lesson).
type Warning struct {
	Severity string   `json:"severity"` // "red" | "yellow"
	Code     string   `json:"code"`
	File     string   `json:"file,omitempty"`
	Files    []string `json:"files,omitempty"` // for a cross-file finding
	Server   string   `json:"server,omitempty"`
	Key      string   `json:"key,omitempty"`     // env/header member name, when relevant
	Kind     string   `json:"kind,omitempty"`    // the kind that does not expand Dialect
	Dialect  string   `json:"dialect,omitempty"` // "dollar_brace" | "dollar_env_brace" | "env_brace"
}

// Warning codes (docs/log/56 §10's "1 理由 = 1 コード" extended to these read-side
// findings). Unlike a REST rejection these carry several structured params (file /
// server / kind / dialect), so the Console does NOT resolve them through the flat
// "err.<code>" catalog — console/src/features/repos/projectMcpWire.ts's
// warningText() switches on Code and interpolates a dedicated "pmcp.w_<code>" key
// per locale. Add both the ja and en "pmcp.w_…" keys and a warningText() case
// whenever a code is added here.
const (
	CodeFileUnreadable     = "mcp_project_file_unreadable"
	CodeNameHijack         = "mcp_project_name_hijack"
	CodeNameInvalid        = "mcp_project_name_invalid"
	CodeDialectMismatch    = "mcp_project_dialect_mismatch"
	CodeDialectBroken      = "mcp_project_dialect_broken"
	CodeSecretTracked      = "mcp_project_secret_tracked"
	CodeSecretVCSUncertain = "mcp_project_secret_vcs_uncertain"
	CodeServerDiverged     = "mcp_project_server_diverged"
)
