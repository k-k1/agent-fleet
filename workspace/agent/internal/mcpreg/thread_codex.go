package mcpreg

// codex's THREAD-scoped MCP configuration — the `mcp_servers` map handed to
// `thread/start` / `thread/resume` / `thread/fork` (docs/log/27 §9.3.1).
//
// Why it exists: a managed session's MCP child is spawned by the ONE shared app-server
// the Agent started, so nothing per-session can ride the process environment the way
// AF_SESSION_NAME does for tmux sessions. The thread config is the only channel that
// varies per session, and it was measured to reach the spawned child
// (drift_mcp_identity_test.go, codex-cli 0.147.0).
//
// Why it sends ONLY the af entry — three measurements, all on 0.147.0:
//
//   - A thread map MERGES with the file config layers: $CODEX_HOME/config.toml AND a
//     trusted project's own .codex/config.toml both still apply
//     (TestDriftCodexThreadConfigMergeMatrix). docs/log/27 §9.3's "replace" result was
//     measured on servers supplied by `-c` overrides and does not generalize to files.
//   - For a SHARED name the thread definition WINS
//     (TestDriftCodexThreadConfigOverridesSameNamedFileServer), so af can restate its
//     own entry with the session name added and nothing else changes.
//   - The override is whole-entry, not field-wise: the thread definition must carry
//     command/args/env_vars in full, not just the env delta.
//
// So this deliberately does NOT re-emit the user's servers. Carrying them would put
// their env and header VALUES into an RPC payload for no benefit, and would freeze a
// copy of the registry into every thread — while merging gives the same result for
// free, including servers af does not manage at all (hand-added rows, `codex mcp add`,
// trusted project config).

import "sort"

// CodexThreadOpts carries what only the thread knows.
type CodexThreadOpts struct {
	// SessionName is stamped on the af builtin as AF_SESSION_NAME — the variable
	// mcpOwningSession/af_report read to name their owner. Empty means "nothing to
	// say", and the session-side server falls back to guessing from cwd.
	SessionName string
}

// CodexThreadServers builds the `mcp_servers` map for a thread config. ok is false
// when there is nothing to override, and the caller must then OMIT the key so the
// thread simply inherits every file layer.
func CodexThreadServers(defs []ServerDef, opts CodexThreadOpts) (servers map[string]any, ok bool) {
	if opts.SessionName == "" {
		return nil, false
	}
	for _, d := range defs {
		if d.Origin != OriginBuiltin || d.ID != BuiltinAF || d.Transport != TransportStdio {
			continue
		}
		return map[string]any{d.Name: codexAFThreadEntry(d, opts.SessionName)}, true
	}
	// af is not attached to codex sessions (disabled, or scoped away): there is no
	// session-side server to name, so leave the thread's config untouched.
	return nil, false
}

// codexAFThreadEntry restates the af server the way config.toml already describes it
// (codexServerBlocks), plus the session name. It must be complete: the thread
// definition replaces the file one outright rather than layering on top of it.
func codexAFThreadEntry(d ServerDef, sessionName string) map[string]any {
	e := map[string]any{"command": d.Command}
	if len(d.Args) > 0 {
		e["args"] = anySlice(d.Args)
	}
	env := map[string]string{}
	for k, v := range d.Env {
		env[k] = v
	}
	env[sessionNameVar] = sessionName
	e["env"] = anyMap(env)
	// The credentials keep travelling by NAME, forwarded from the daemon's own
	// environment — measured to still work at thread scope. Setting the session name
	// literally and forwarding the same variable would be two answers to one question,
	// and the shared daemon has no AF_SESSION_NAME to forward anyway.
	if forward := without(extraEnvVars(d), sessionNameVar); len(forward) > 0 {
		sort.Strings(forward)
		e["env_vars"] = anySlice(forward)
	}
	if d.TimeoutMS > 0 {
		e["startup_timeout_sec"] = float64(d.TimeoutMS) / 1000
	}
	return e
}

// sessionNameVar is the variable a session-side af server reads to learn its owner.
const sessionNameVar = "AF_SESSION_NAME"

func without(list []string, drop string) []string {
	var out []string
	for _, v := range list {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}
