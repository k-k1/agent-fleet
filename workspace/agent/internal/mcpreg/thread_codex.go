package mcpreg

// codex's THREAD-scoped MCP configuration — the `mcp_servers` map handed to
// `thread/start` / `thread/resume` / `thread/fork` (docs/27 §9.3.1).
//
// Why this exists beside materializeCodex, which writes the very same servers into
// config.toml: a managed session's MCP child is spawned by the ONE shared app-server
// the Agent started, so nothing per-session can ride the process environment the way
// AF_SESSION_NAME does for tmux sessions. The thread config is the only channel that
// varies per session, and it was measured to reach the spawned child
// (drift_mcp_identity_test.go, codex-cli 0.147.0).
//
// Two measured facts shape everything here:
//
//   - A thread-local map REPLACES the global one; it does not merge (docs/27 §9.3).
//     So this must emit the WHOLE effective set, not just the af entry — otherwise
//     configuring a session name would silently strip the user's own MCP servers from
//     every managed codex session. An empty result therefore means "send no map at
//     all" (inherit config.toml), never "send {}" (which is a working deny).
//   - `env_vars` still forwards from the daemon's environment at thread scope, so the
//     builtins' credentials keep travelling the way they do today — by name.
//
// This is deliberately the JSON twin of codexServerBlocks: same fields, same values,
// same literal-vs-forwarded split. The ONE difference is the af entry's session name.
// Keeping them equivalent is what makes "managed and TUI see the same servers" true
// by construction rather than by review.

import "sort"

// CodexThreadOpts carries what only the thread knows.
type CodexThreadOpts struct {
	// SessionName is stamped on the af builtin as AF_SESSION_NAME — the variable
	// mcpOwningSession/af_report read to name their owner. Empty leaves it off, and
	// the session-side server falls back to guessing from cwd.
	SessionName string
}

// CodexThreadServers builds the `mcp_servers` map for a thread config. ok is false
// when there is nothing to say, and the caller must then OMIT the key: an empty map
// would deny the servers config.toml already provides.
func CodexThreadServers(defs []ServerDef, opts CodexThreadOpts) (servers map[string]any, ok bool) {
	if len(defs) == 0 {
		return nil, false
	}
	servers = make(map[string]any, len(defs))
	for _, d := range defs {
		e := map[string]any{}
		if d.Transport == TransportHTTP {
			e["url"] = d.URL
			if len(d.Headers) > 0 {
				e["http_headers"] = anyMap(d.Headers)
			}
		} else {
			e["command"] = d.Command
			if len(d.Args) > 0 {
				e["args"] = anySlice(d.Args)
			}
			env := map[string]string{}
			for k, v := range d.Env {
				env[k] = v
			}
			forward := extraEnvVars(d)
			if name := threadSessionName(d, opts); name != "" {
				env[sessionNameVar] = name
				// Setting it literally and forwarding the same variable are two
				// answers to one question. Drop the forward: the shared daemon has no
				// AF_SESSION_NAME to forward anyway, so the literal is the only real
				// value and codex never has to arbitrate.
				forward = without(forward, sessionNameVar)
			}
			if len(forward) > 0 {
				sort.Strings(forward)
				e["env_vars"] = anySlice(forward)
			}
			if len(env) > 0 {
				e["env"] = anyMap(env)
			}
		}
		if d.TimeoutMS > 0 {
			// Seconds as a float, like config.toml's startup_timeout_sec.
			e["startup_timeout_sec"] = float64(d.TimeoutMS) / 1000
		}
		servers[d.Name] = e
	}
	return servers, true
}

// sessionNameVar is the variable a session-side af server reads to learn its owner.
const sessionNameVar = "AF_SESSION_NAME"

// threadSessionName returns the name to stamp, and only for the af builtin: it is the
// one server that talks back to the Agent about its own session. A user-registered
// server has no such contract, and handing it the session name would be leaking the
// fleet's topology into somebody else's process for no purpose.
func threadSessionName(d ServerDef, opts CodexThreadOpts) string {
	if d.Origin != OriginBuiltin || d.ID != BuiltinAF {
		return ""
	}
	return opts.SessionName
}

func without(list []string, drop string) []string {
	var out []string
	for _, v := range list {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}
