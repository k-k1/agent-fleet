package mcpproj

// Placeholder-dialect detection (docs/56 §2.1, the measured heart of this feature):
// which of the three placeholder syntaxes a server's values reference, and whether
// the kind(s) reading this file will actually expand it.

import "regexp"

// dialectSupport is which placeholder syntaxes a kind's CLI expands (measured,
// docs/56 §2.1). opencode's ${env:VAR} case is NOT "unsupported" like the others —
// it partially expands and leaves a stray "$", which is why it gets its own code
// (CodeDialectBroken) instead of the generic mismatch.
type dialectSupport struct {
	dollarBrace    bool
	dollarEnvBrace bool
	envBrace       bool
}

var dialectByKind = map[string]dialectSupport{
	// claude/copilot share this table only informally — they share the FILE
	// (.mcp.json) and its dialect, but this map is keyed by kind because the
	// warning needs to name which of the file's kinds is affected.
	"claude":   {dollarBrace: true},
	"copilot":  {dollarBrace: true},
	"cursor":   {dollarBrace: true, dollarEnvBrace: true},
	"opencode": {envBrace: true},
	"codex":    {}, // expands nothing (docs/56 §2.1)
}

var (
	reDollarEnvBrace = regexp.MustCompile(`\{env:[A-Za-z_][A-Za-z0-9_]*\}`) // matched then checked for a preceding '$'
	reDollarBrace    = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)
)

// detectDialects returns every placeholder dialect referenced in s, deduplicated.
func detectDialects(s string) []string {
	found := map[string]bool{}
	for _, m := range reDollarEnvBrace.FindAllStringIndex(s, -1) {
		if m[0] > 0 && s[m[0]-1] == '$' {
			found[DialectDollarEnvBrace] = true
		} else {
			found[DialectEnvBrace] = true
		}
	}
	if reDollarBrace.MatchString(s) {
		found[DialectDollarBrace] = true
	}
	out := make([]string, 0, len(found))
	for d := range found {
		out = append(out, d)
	}
	return out
}

// supports reports whether kind's dialectSupport includes d.
func (ds dialectSupport) supports(d string) bool {
	switch d {
	case DialectDollarBrace:
		return ds.dollarBrace
	case DialectDollarEnvBrace:
		return ds.dollarEnvBrace
	case DialectEnvBrace:
		return ds.envBrace
	}
	return false
}

// dialectWarningsForValue checks one string value against every kind in fileKinds
// and appends a warning per (kind, dialect) mismatch found. server/key identify
// where the value came from for the wire Warning.
func dialectWarningsForValue(value, file, server, key string, fileKinds []string) []Warning {
	var out []Warning
	for _, d := range detectDialects(value) {
		for _, kind := range fileKinds {
			support, known := dialectByKind[kind]
			if !known {
				continue // kiro/agy: unmeasured, never claim a mismatch we haven't measured
			}
			if support.supports(d) {
				continue
			}
			if kind == "opencode" && d == DialectDollarEnvBrace {
				out = append(out, Warning{
					Severity: "red", Code: CodeDialectBroken,
					File: file, Server: server, Key: key, Kind: kind, Dialect: d,
				})
				continue
			}
			out = append(out, Warning{
				Severity: "yellow", Code: CodeDialectMismatch,
				File: file, Server: server, Key: key, Kind: kind, Dialect: d,
			})
		}
	}
	return out
}

// serverDialectWarnings scans every string-bearing field of s for placeholder
// dialects the file's kinds will not expand.
func serverDialectWarnings(s Server, file string, fileKinds []string) []Warning {
	var out []Warning
	out = append(out, dialectWarningsForValue(s.Command, file, s.Name, "command", fileKinds)...)
	for _, a := range s.Args {
		out = append(out, dialectWarningsForValue(a, file, s.Name, "args", fileKinds)...)
	}
	out = append(out, dialectWarningsForValue(s.URL, file, s.Name, "url", fileKinds)...)
	for k, v := range s.Env {
		out = append(out, dialectWarningsForValue(v, file, s.Name, k, fileKinds)...)
	}
	for k, v := range s.Headers {
		out = append(out, dialectWarningsForValue(v, file, s.Name, k, fileKinds)...)
	}
	return out
}
