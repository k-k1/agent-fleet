package mcpproj

// serialize.go — the write-side inverse of parse_json.go / parse_codex.go: turn a
// Server into the raw payload a destination file's format expects. Deliberately
// rebuilds a clean entry from Server's normalized fields only (Command/Args/Env/
// URL/Headers) rather than round-tripping Extra — the same choice mcpreg's own
// *Servers builders make (ClaudeServers, OpencodeServers, …): a written entry
// looks like one the CLI itself would produce, not a patchwork of whatever the
// SOURCE file happened to also carry (docs/log/56 §6, "a newly created file takes the
// shape the CLI itself would produce").

import (
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// buildJSONEntry renders s as the map UpsertJSONEntry should write, per sp's
// spelling (mcpreg.JSONEntrySpellings).
func buildJSONEntry(s Server, sp mcpreg.JSONEntrySpelling) map[string]any {
	out := map[string]any{}
	if sp.TypeKey != "" {
		if s.Transport == TransportHTTP {
			out[sp.TypeKey] = sp.TypeHTTP
		} else {
			out[sp.TypeKey] = sp.TypeStdio
		}
	}
	if s.Transport == TransportHTTP {
		out[sp.URLKey] = s.URL
		if len(s.Headers) > 0 {
			out[sp.HeadersKey] = toAnyMap(s.Headers)
		}
	} else {
		if sp.ArgsFolded {
			out[sp.CommandKey] = toAnySlice(append([]string{s.Command}, s.Args...))
		} else {
			out[sp.CommandKey] = s.Command
			if len(s.Args) > 0 {
				out["args"] = toAnySlice(s.Args)
			}
		}
		if len(s.Env) > 0 {
			out[sp.EnvKey] = toAnyMap(s.Env)
		}
	}
	if sp.AlwaysEnabled {
		out["enabled"] = true
	}
	return out
}

func toAnyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func toAnySlice(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

// buildCodexBlock renders one `[mcp_servers.<name>]` TOML block for s, the same
// shape materialize_codex.go's codexServerBlocks writes for the user scope —
// re-implemented here (not shared) because it reads a mcpproj.Server, not a
// mcpreg.ServerDef (ADR0040 decision 15); the actual TOML syntax helpers
// (mcpreg.TOMLString etc.) ARE shared.
func buildCodexBlock(name string, s Server) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp_servers.%s]\n", name)
	if s.Transport == TransportHTTP {
		fmt.Fprintf(&b, "url = %s\n", mcpreg.TOMLString(s.URL))
	} else {
		fmt.Fprintf(&b, "command = %s\n", mcpreg.TOMLString(s.Command))
		if len(s.Args) > 0 {
			fmt.Fprintf(&b, "args = %s\n", mcpreg.TOMLStringArray(s.Args))
		}
	}
	if s.Transport == TransportHTTP && len(s.Headers) > 0 {
		fmt.Fprintf(&b, "\n[mcp_servers.%s.http_headers]\n", name)
		writeTOMLTable(&b, s.Headers)
	}
	if s.Transport == TransportStdio && len(s.Env) > 0 {
		fmt.Fprintf(&b, "\n[mcp_servers.%s.env]\n", name)
		writeTOMLTable(&b, s.Env)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeTOMLTable(b *strings.Builder, m map[string]string) {
	for _, k := range mcpreg.SortedStringMapKeys(m) {
		fmt.Fprintf(b, "%s = %s\n", mcpreg.TOMLKey(k), mcpreg.TOMLString(m[k]))
	}
}
