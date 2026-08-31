package mcpreg

// codex's native MCP config: `[mcp_servers.<name>]` tables in
// $CODEX_HOME/config.toml (~/.codex/config.toml in the workspace).
//
// Measured on codex-cli 0.145.0 — `codex mcp add` in an isolated CODEX_HOME writes:
//
//	[mcp_servers.srv]
//	command = "/bin/echo"
//	args = ["a"]
//
//	[mcp_servers.srv.env]
//	FOO = "bar"
//
// and a hand-written remote table round-trips through `codex mcp list --json` as
// streamable_http with `http_headers` / `env_http_headers` / `startup_timeout_sec`.
// Unlike the assistant chat (attach.go), a materialized definition may put header and
// env VALUES straight into this file: it is a 0600 file, which is what docs/log/48 §5.1
// promises, whereas the chat's only per-exec channel is argv.
//
// The edit is line-based, like codex/settings.go's notice-key editor, for the same
// reason: config.toml is the user's file and holds comments and project trust
// sections that a parse → re-emit round trip would silently reformat away. So af
// removes the whole table (and its sub-tables) for the names it owns, then appends
// fresh ones; every other byte is carried through untouched.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func codexConfigPath() string {
	return filepath.Join(paths.CodexHome(), "config.toml")
}

func materializeCodex(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	path := codexConfigPath()
	src := ""
	b, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		src = string(b)
	case !os.IsNotExist(rerr):
		return nil, nil, false, rerr
	}

	// Names to strip = the ones af is retiring, PLUS the ones it is about to write.
	// The second half is not optional: TOML rejects a duplicate table, so appending
	// `[mcp_servers.x]` while an older `[mcp_servers.x]` is still present would leave
	// codex unable to read its own config at all.
	drop := map[string]bool{}
	removed = goneFrom(prev, defs)
	for _, n := range removed {
		drop[n] = true
	}
	keep := map[string]bool{}
	for _, d := range defs {
		drop[d.Name] = true
		keep[d.Name] = true
	}

	// af's own name rotates every boot; last boot's table has to go even when the
	// ownership ledger no longer names it (see StaleAFServerName).
	out := appendTOMLBlocks(stripCodexServers(src, func(name string) bool {
		return drop[name] || StaleAFServerName(name, keep)
	}), codexServerBlocks(defs))
	written = defNames(defs)
	if out == src {
		return written, removed, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, false, err
	}
	if err := writeFileAtomic(path, []byte(out), 0o600); err != nil {
		return nil, nil, false, err
	}
	return written, removed, true, nil
}

// tomlHeaderRE matches a table header line, `[a.b]` or `[[a.b]]`, with an optional
// trailing comment. Capturing the inside without the brackets is enough because the
// only names af writes are bare keys (nameRe is the TOML bare-key charset).
var tomlHeaderRE = regexp.MustCompile(`^\s*\[\[?([^]]+)]]?\s*(?:#.*)?$`)

// mcpServerTableName returns the server a `[mcp_servers.…]` header belongs to, or ""
// for any other table. `mcp_servers.foo` and `mcp_servers.foo.env` both yield "foo".
func mcpServerTableName(header string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(header), "mcp_servers.")
	if !ok {
		return ""
	}
	name := rest
	if i := strings.IndexByte(rest, '.'); i >= 0 {
		name = rest[:i]
	}
	return strings.Trim(strings.TrimSpace(name), `"'`)
}

// stripCodexServers removes every `[mcp_servers.<name>]` table (and its sub-tables)
// whose name is in drop, together with the blank lines that separated it — so a
// write followed by a remove gives back the original file byte for byte.
func stripCodexServers(src string, drop func(name string) bool) string {
	if src == "" {
		return src
	}
	var out []string
	skipping := false
	for _, line := range strings.Split(src, "\n") {
		if m := tomlHeaderRE.FindStringSubmatch(line); m != nil {
			skipping = drop(mcpServerTableName(m[1]))
			if skipping {
				for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
					out = out[:len(out)-1]
				}
			}
		}
		if !skipping {
			out = append(out, line)
		}
	}
	joined := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if joined == "" {
		return ""
	}
	return joined + "\n"
}

// appendTOMLBlocks puts blocks at the end of src, one blank line apart. Each block is
// newline-free at its edges so the spacing is decided here, once.
func appendTOMLBlocks(src string, blocks []string) string {
	parts := make([]string, 0, len(blocks)+1)
	if body := strings.TrimRight(src, "\n"); body != "" {
		parts = append(parts, body)
	}
	parts = append(parts, blocks...)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// codexServerBlocks renders one TOML block per definition. Sub-tables (`env`,
// `http_headers`) must come after their parent's bare keys, so each definition is
// emitted as a single block rather than interleaved.
func codexServerBlocks(defs []ServerDef) []string {
	var out []string
	for _, d := range defs {
		var b strings.Builder
		fmt.Fprintf(&b, "[mcp_servers.%s]\n", d.Name)
		if d.Transport == TransportHTTP {
			fmt.Fprintf(&b, "url = %s\n", tomlStr(d.URL))
		} else {
			fmt.Fprintf(&b, "command = %s\n", tomlStr(d.Command))
			if len(d.Args) > 0 {
				fmt.Fprintf(&b, "args = %s\n", tomlStrArray(d.Args))
			}
		}
		if d.TimeoutMS > 0 {
			// codex takes seconds as a float; a definition carries milliseconds.
			fmt.Fprintf(&b, "startup_timeout_sec = %.1f\n", float64(d.TimeoutMS)/1000)
		}
		if d.Transport == TransportStdio {
			// Codex does not inherit arbitrary variables into stdio MCP children.
			// Builtins name the host variables their wrapper/server needs; values
			// stay in the parent environment and never enter config.toml.
			if forward := extraEnvVars(d); len(forward) > 0 {
				sort.Strings(forward)
				fmt.Fprintf(&b, "env_vars = %s\n", tomlStrArray(forward))
			}
		}
		if d.Transport == TransportHTTP && len(d.Headers) > 0 {
			fmt.Fprintf(&b, "\n[mcp_servers.%s.http_headers]\n", d.Name)
			writeTOMLTable(&b, d.Headers)
		}
		if d.Transport == TransportStdio && len(d.Env) > 0 {
			fmt.Fprintf(&b, "\n[mcp_servers.%s.env]\n", d.Name)
			writeTOMLTable(&b, d.Env)
		}
		out = append(out, strings.TrimRight(b.String(), "\n"))
	}
	return out
}

func writeTOMLTable(b *strings.Builder, m map[string]string) {
	for _, k := range sortedKeys(m) {
		fmt.Fprintf(b, "%s = %s\n", tomlKey(k), tomlStr(m[k]))
	}
}

var bareKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey quotes a key that is not a TOML bare key. Header names are the reason:
// validation lets through anything without a colon or newline, so "X.Foo" is
// reachable and would otherwise become a dotted path into a nested table.
func tomlKey(k string) string {
	if bareKeyRE.MatchString(k) {
		return k
	}
	return tomlStr(k)
}
