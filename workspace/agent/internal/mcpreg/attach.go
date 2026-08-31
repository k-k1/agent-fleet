package mcpreg

// Per-provider serialization of registry definitions (docs/log/48 §7 / P2). One
// ServerDef list in, the shape each headless CLI's assistant chat expects out — so
// "which servers does this assistant get" is decided once, in the registry, and the
// providers only differ in how they spell it.
//
// The one rule every provider here obeys: a secret VALUE (an env value, a header
// value) must not reach a process ARGV. Argv is readable through /proc for the whole
// uid and can be echoed into a CLI's own crash logs, which is a weaker place than
// the 0600 files docs/log/48 §5.1 commits to. So:
//
//   - claude / opencode / agy take a config FILE the caller writes 0600 (this package
//     only builds the maps),
//   - codex has no config-file override for a single exec, only `-c key=value` argv,
//     so secrets ride the ENVIRONMENT instead: `env_vars` forwards a stdio server's
//     own variables by name, and `env_http_headers` maps a header to a minted
//     variable name. Only the NAMES land in argv.
//
// codex's remote-header support was measured, not assumed (codex-cli 0.145.0,
// 2026-07-27): `codex mcp list --json` round-trips `http_headers` /
// `env_http_headers` on a streamable_http transport, and a live `codex exec` against
// a header-logging listener showed both arriving on the wire. This CONTRADICTS the
// original docs/log/48 §7 note that codex could carry nothing but a bearer token — that
// note described an older CLI and is corrected in the doc.

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// ClaudeServers builds claude's `mcpServers` map for --mcp-config.
func ClaudeServers(defs []ServerDef) map[string]any {
	out := map[string]any{}
	for _, d := range defs {
		if d.Transport == TransportHTTP {
			e := map[string]any{"type": "http", "url": d.URL}
			if len(d.Headers) > 0 {
				e["headers"] = anyMap(d.Headers)
			}
			out[d.Name] = e
			continue
		}
		e := map[string]any{"type": "stdio", "command": d.Command}
		if len(d.Args) > 0 {
			e["args"] = anySlice(d.Args)
		}
		if len(d.Env) > 0 {
			e["env"] = anyMap(d.Env)
		}
		out[d.Name] = e
	}
	return out
}

// AgyServers builds agy's mcp_config.json server map. Same claude-derived shape, but
// without the "type" discriminator (agy's gemini-cli lineage infers it from the
// presence of command/url) and with an env overlay every stdio entry inherits — agy
// runs from an isolated HOME and its spawned servers must be pointed back at the
// real one (chat_providers.go chatAgyHome).
func AgyServers(defs []ServerDef, overlay map[string]string) map[string]any {
	out := map[string]any{}
	for _, d := range defs {
		if d.Transport == TransportHTTP {
			e := map[string]any{"url": d.URL}
			if len(d.Headers) > 0 {
				e["headers"] = anyMap(d.Headers)
			}
			out[d.Name] = e
			continue
		}
		env := map[string]string{}
		for k, v := range overlay {
			env[k] = v
		}
		for k, v := range d.Env {
			env[k] = v // the definition wins: it is the server's own contract
		}
		e := map[string]any{"command": d.Command}
		if len(d.Args) > 0 {
			e["args"] = anySlice(d.Args)
		}
		if len(env) > 0 {
			e["env"] = anyMap(env)
		}
		out[d.Name] = e
	}
	return out
}

// OpencodeServers builds opencode's `mcp` map for an opencode.json. opencode folds
// the command and its arguments into ONE array and calls the env "environment"
// (verified against the existing af entry, chat_providers.go opencodeChatDir).
func OpencodeServers(defs []ServerDef) map[string]any {
	out := map[string]any{}
	for _, d := range defs {
		if d.Transport == TransportHTTP {
			e := map[string]any{"type": "remote", "url": d.URL, "enabled": true}
			if len(d.Headers) > 0 {
				e["headers"] = anyMap(d.Headers)
			}
			out[d.Name] = e
			continue
		}
		e := map[string]any{
			"type":    "local",
			"command": anySlice(append([]string{d.Command}, d.Args...)),
			"enabled": true,
		}
		if len(d.Env) > 0 {
			e["environment"] = anyMap(d.Env)
		}
		out[d.Name] = e
	}
	return out
}

// CodexOpts tunes the codex serialization for its consumer.
type CodexOpts struct {
	// Approve pre-approves the servers' tools. codex has its own MCP approval layer;
	// the headless assistant chat has no UI to answer it, so an attached server would
	// report "user cancelled MCP tool call" on every call. Attaching the server to the
	// assistant IS the user's approval there. An interactive session leaves this off —
	// it has a real approver.
	Approve bool
}

// CodexOverrides builds the `-c mcp_servers.…` argv for a codex exec plus the "K=V"
// environment entries that argv refers to. The caller MUST put env on the codex
// process (cmd.Env) — the args alone describe servers whose credentials are missing.
//
// Measured semantics (codex-cli 0.145.0): a stdio MCP child gets a default-deny core
// environment (HOME, PATH, LANG, LC_ALL, PWD, SHELL, SHLVL, TERM, TZ) — anything else
// must be named in `env_vars` (forwarded from codex's own env) or given literally in
// the `env` table. HOME and PATH being in the core set is why a builtin's mcp-run
// wrapper only needs its store key added.
func CodexOverrides(defs []ServerDef, opts CodexOpts) (args []string, env []string) {
	// Passthrough is keyed by the variable name the CHILD expects, which the
	// definition owns — so two servers wanting the same name with different values
	// cannot both be expressed this way. The loser falls back to the literal `env`
	// table (argv-visible) rather than silently receiving the other's secret.
	pass := map[string]string{}
	for _, d := range defs {
		p := "mcp_servers." + d.Name + "."
		if opts.Approve {
			args = append(args, "-c", p+`default_tools_approval_mode="approve"`)
		}
		if d.Transport == TransportHTTP {
			args = append(args, "-c", p+"url="+tomlStr(d.URL))
			if len(d.Headers) > 0 {
				kv := map[string]string{}
				for _, h := range sortedKeys(d.Headers) {
					name := mintedHeaderVar(d.Name, h)
					kv[h] = name
					env = append(env, name+"="+d.Headers[h])
				}
				args = append(args, "-c", p+"env_http_headers="+tomlInlineTable(kv))
			}
			args = append(args, timeoutArg(p, d)...)
			continue
		}
		args = append(args, "-c", p+"command="+tomlStr(d.Command))
		if len(d.Args) > 0 {
			args = append(args, "-c", p+"args="+tomlStrArray(d.Args))
		}
		var forward []string
		literal := map[string]string{}
		for _, k := range sortedKeys(d.Env) {
			if prev, taken := pass[k]; taken && prev != d.Env[k] {
				literal[k] = d.Env[k]
				continue
			}
			pass[k] = d.Env[k]
			forward = append(forward, k)
		}
		forward = append(forward, extraEnvVars(d)...)
		if len(forward) > 0 {
			sort.Strings(forward)
			args = append(args, "-c", p+"env_vars="+tomlStrArray(forward))
		}
		if len(literal) > 0 {
			args = append(args, "-c", p+"env="+tomlInlineTable(literal))
		}
		args = append(args, timeoutArg(p, d)...)
	}
	for k, v := range pass {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return args, env
}

// extraEnvVars names the variables a definition needs forwarded beyond codex's core
// set. Only the builtins have any: their `mcp-run` wrapper opens the encrypted store
// to inject the user's key, so it needs the store key itself. HOME and PATH — the
// other things mcp-run touches — are already in codex's core set (measured 0.145.0),
// so nothing else has to be listed.
func extraEnvVars(d ServerDef) []string {
	if d.Origin != OriginBuiltin {
		return nil
	}
	if d.ID == BuiltinAF {
		// The session-side af server (self-report + Chromium Attach View) calls the
		// local Agent REST directly. Codex
		// starts stdio MCP children with a default-deny environment, so both the
		// bearer token and a non-default listen address must be forwarded.
		return []string{"AGENT_TOKEN", "AGENT_ADDR", "AF_SESSION_NAME"}
	}
	return []string{"AF_SECRET_KEY"}
}

// timeoutArg maps the definition's timeout onto codex's startup budget. codex takes
// SECONDS as a float; a definition carries milliseconds.
func timeoutArg(prefix string, d ServerDef) []string {
	if d.TimeoutMS <= 0 {
		return nil
	}
	return []string{"-c", fmt.Sprintf("%sstartup_timeout_sec=%.1f", prefix, float64(d.TimeoutMS)/1000)}
}

// mintedHeaderVar names the environment variable a codex remote header reads from.
// The name is derived, not chosen by the user, so it can never collide with a real
// variable or with another server's; the hash disambiguates names that survive
// sanitizing identically ("a-b" and "a_b").
func mintedHeaderVar(server, header string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(server + "\x00" + header))
	return fmt.Sprintf("AF_MCP_%s_%08X", envSanitize(header), h.Sum32())
}

func envSanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// --- TOML value encoding (codex parses each -c value as TOML) ---------------------

func tomlStr(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

func tomlStrArray(vals []string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = tomlStr(v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// tomlInlineTable renders a string map as `{ "k" = "v", … }`. Keys are quoted so a
// header name with a dash stays one key rather than a dotted path.
func tomlInlineTable(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		parts = append(parts, tomlStr(k)+"="+tomlStr(m[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func anyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func anySlice(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}
