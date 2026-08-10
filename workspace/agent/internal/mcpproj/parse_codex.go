package mcpproj

// codex's project-scope MCP config is .codex/config.toml's `[mcp_servers.<name>]`
// tables — the same shape mcpreg's materialize_codex.go WRITES, and also what
// `codex mcp add` writes by hand. There is no TOML library in this module (the
// existing codex settings/materialize code is all hand-rolled line/regex editing,
// on purpose — config.toml carries comments and [projects."…"] trust sections a
// generic parse→re-emit would reformat away, docs/48 §8.2). This file follows the
// same convention for READING: a scanner narrow enough for the subset af itself
// (and `codex mcp add`) actually produces, not a general TOML implementation.
//
// Table-name recognition (mcpreg.CodexServerTableName / CodexTOMLHeaderRE) is
// shared with the writer so both sides agree on where one server's TOML block
// starts and ends.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// parseCodexServers scans src (a whole config.toml) for every `[mcp_servers.*]`
// table and returns one Server per name. Any other table (including
// `[projects."<dir>"]`) is skipped untouched.
func parseCodexServers(src string) (map[string]Server, error) {
	builds := map[string]*serverBuild{}
	ensure := func(name string) *serverBuild {
		b, ok := builds[name]
		if !ok {
			b = &serverBuild{name: name}
			builds[name] = b
		}
		return b
	}

	var cur string // current server name, "" when outside any mcp_servers.* table
	var sub string // "" (top-level fields) | "env" | "http_headers" | "env_http_headers" | other (ignored)

	for _, line := range strings.Split(src, "\n") {
		if m := mcpreg.CodexTOMLHeaderRE.FindStringSubmatch(line); m != nil {
			header := strings.TrimSpace(m[1])
			name := mcpreg.CodexServerTableName(header)
			if name == "" {
				cur, sub = "", ""
				continue
			}
			cur, sub = name, subTableSuffix(header, name)
			ensure(cur)
			continue
		}
		if cur == "" {
			continue // outside any mcp_servers table — not our concern
		}
		key, val, ok := splitTOMLAssignment(line)
		if !ok {
			continue
		}
		if err := ensure(cur).apply(sub, key, val); err != nil {
			return nil, fmt.Errorf("mcp_servers.%s: %w", cur, err)
		}
	}

	out := make(map[string]Server, len(builds))
	for name, b := range builds {
		out[name] = b.server()
	}
	return out, nil
}

// subTableSuffix returns what follows "mcp_servers.<name>." in header ("env",
// "http_headers", …), or "" when header is exactly "mcp_servers.<name>" with no
// sub-table. name is already known (mcpreg.CodexServerTableName's result), so this
// only has to strip it back off — bare or quoted, though af itself (nameRe's
// charset) never needs the quoted form.
func subTableSuffix(header, name string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(header), "mcp_servers.")
	if !ok {
		return ""
	}
	for _, prefix := range []string{name + ".", `"` + name + `".`, `'` + name + `'.`} {
		if s, ok := strings.CutPrefix(rest, prefix); ok {
			return s
		}
	}
	return ""
}

// serverBuild accumulates one [mcp_servers.<name>] table (plus its sub-tables)
// across possibly-non-contiguous lines.
type serverBuild struct {
	name           string
	command        string
	args           []string
	url            string
	env            map[string]string
	httpHeaders    map[string]string
	envHTTPHeaders map[string]string // header -> env VAR NAME (no value available here)
	extra          map[string]any
}

func (b *serverBuild) apply(sub, key string, val tomlValue) error {
	switch sub {
	case "":
		switch key {
		case "command":
			b.command, _ = val.str()
		case "args":
			b.args = val.strArray()
		case "url":
			b.url, _ = val.str()
		// af always writes env/http_headers as their OWN [mcp_servers.name.env]
		// sub-table (materialize_codex.go), but a hand-written config.toml — or a
		// future af writer — may fold them inline on one line instead; recognize
		// both spellings rather than silently dropping the inline form into Extra.
		case "env":
			if m, ok := val.inlineTable(); ok {
				b.env = mergeStrMap(b.env, m)
			}
		case "http_headers":
			if m, ok := val.inlineTable(); ok {
				b.httpHeaders = mergeStrMap(b.httpHeaders, m)
			}
		case "env_http_headers":
			if m, ok := val.inlineTable(); ok {
				b.envHTTPHeaders = mergeStrMap(b.envHTTPHeaders, m)
			}
		default:
			b.setExtra(key, val.native())
		}
	case "env":
		if b.env == nil {
			b.env = map[string]string{}
		}
		if s, ok := val.str(); ok {
			b.env[key] = s
		}
	case "http_headers":
		if b.httpHeaders == nil {
			b.httpHeaders = map[string]string{}
		}
		if s, ok := val.str(); ok {
			b.httpHeaders[key] = s
		}
	case "env_http_headers":
		if b.envHTTPHeaders == nil {
			b.envHTTPHeaders = map[string]string{}
		}
		if s, ok := val.str(); ok {
			b.envHTTPHeaders[key] = s
		}
	default:
		// An unrecognized sub-table (codex may grow one) — keep it out of Extra's
		// flat namespace rather than guess a shape for it.
	}
	return nil
}

func (b *serverBuild) setExtra(key string, v any) {
	if b.extra == nil {
		b.extra = map[string]any{}
	}
	b.extra[key] = v
}

func (b *serverBuild) server() Server {
	s := Server{Name: b.name}
	if b.url != "" {
		s.Transport = TransportHTTP
		s.URL = b.url
		if len(b.httpHeaders) > 0 {
			s.Headers = b.httpHeaders
		}
	} else {
		s.Transport = TransportStdio
		s.Command = b.command
		if len(b.args) > 0 {
			s.Args = b.args
		}
		if len(b.env) > 0 {
			s.Env = b.env
		}
	}
	extra := b.extra
	// env_http_headers names a VARIABLE, not a value — there is nothing to mask and
	// nothing useful to show as a header value, so it is surfaced only as a name
	// list, kept out of Headers/Env so it is never mistaken for a resolved secret.
	if len(b.envHTTPHeaders) > 0 {
		if extra == nil {
			extra = map[string]any{}
		}
		names := make([]string, 0, len(b.envHTTPHeaders))
		for h := range b.envHTTPHeaders {
			names = append(names, h)
		}
		extra["env_http_headers"] = names
	}
	if len(extra) > 0 {
		s.Extra = extra
	}
	return s
}

// --- line-level TOML fragments (deliberately narrow — see file header) -----------

// splitTOMLAssignment recognizes `key = value` (bare or quoted key), stripping an
// unquoted trailing `# comment`. Returns ok=false for anything else (table headers,
// blank lines, comment-only lines).
func splitTOMLAssignment(line string) (key string, val tomlValue, ok bool) {
	t := stripTOMLComment(line)
	t = strings.TrimSpace(t)
	if t == "" {
		return "", tomlValue{}, false
	}
	eq := indexUnquoted(t, '=')
	if eq < 0 {
		return "", tomlValue{}, false
	}
	k := strings.TrimSpace(t[:eq])
	k = strings.Trim(k, `"'`)
	if k == "" {
		return "", tomlValue{}, false
	}
	return k, tomlValue{raw: strings.TrimSpace(t[eq+1:])}, true
}

// stripTOMLComment removes a trailing `# ...` that starts outside a quoted string.
func stripTOMLComment(line string) string {
	inStr := false
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inStr:
			if c == '\\' && quote == '"' {
				i++ // skip escaped char
				continue
			}
			if c == quote {
				inStr = false
			}
		case c == '"' || c == '\'':
			inStr = true
			quote = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

// indexUnquoted finds the first byte b in s that is not inside a quoted string.
func indexUnquoted(s string, b byte) int {
	inStr := false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\\' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				inStr = false
			}
		case c == '"' || c == '\'':
			inStr = true
			quote = c
		case c == b:
			return i
		}
	}
	return -1
}

// tomlValue is an unparsed RHS, decoded lazily into whatever shape the caller asks
// for (this file only ever needs a string, a string array, or "whatever it is" for
// Extra).
type tomlValue struct{ raw string }

func (v tomlValue) str() (string, bool) {
	s := strings.TrimSpace(v.raw)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return unquoteTOMLString(s), true
	}
	return "", false
}

func (v tomlValue) strArray() []string {
	s := strings.TrimSpace(v.raw)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil
	}
	inner := s[1 : len(s)-1]
	var out []string
	for _, part := range splitTOMLList(inner) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(part) >= 2 && (part[0] == '"' || part[0] == '\'') && part[len(part)-1] == part[0] {
			out = append(out, unquoteTOMLString(part))
		}
	}
	return out
}

// inlineTable parses `{ "k" = "v", … }` into a string map — the shape
// tomlInlineTable (attach.go) writes for codex's per-exec header overrides, which a
// hand-written config.toml may also use for env / http_headers / env_http_headers
// instead of a separate sub-table.
func (v tomlValue) inlineTable() (map[string]string, bool) {
	s := strings.TrimSpace(v.raw)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, false
	}
	out := map[string]string{}
	for _, part := range splitTOMLList(s[1 : len(s)-1]) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := indexUnquoted(part, '=')
		if eq < 0 {
			continue
		}
		k := strings.Trim(strings.TrimSpace(part[:eq]), `"'`)
		if v2, ok := (tomlValue{raw: strings.TrimSpace(part[eq+1:])}).str(); ok {
			out[k] = v2
		}
	}
	return out, true
}

func mergeStrMap(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// native decodes v into a string, float64, bool, []string or the raw text as a last
// resort — good enough for Extra, which only ever needs to round-trip a scalar
// (startup_timeout_sec, env_vars) for display, not for re-serialization in P0.
func (v tomlValue) native() any {
	s := strings.TrimSpace(v.raw)
	if str, ok := v.str(); ok {
		return str
	}
	if strings.HasPrefix(s, "[") {
		return v.strArray()
	}
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// splitTOMLList splits a bracket-array's inner text on top-level commas (not inside
// quotes).
func splitTOMLList(s string) []string {
	var out []string
	inStr := false
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\\' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				inStr = false
			}
		case c == '"' || c == '\'':
			inStr = true
			quote = c
		case c == ',':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// unquoteTOMLString reverses tomlStr's escaping (attach.go) for a basic ("...")
// string. TOML literal strings ('...') have no escapes to undo.
func unquoteTOMLString(s string) string {
	if len(s) < 2 {
		return s
	}
	body, quote := s[1:len(s)-1], s[0]
	if quote == '\'' {
		return body
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			switch body[i+1] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte(body[i+1])
			}
			i++
			continue
		}
		b.WriteByte(body[i])
	}
	return b.String()
}
