package muse

// Integration (MCP) servers, on the wire (ADR 0095 decision 11).
//
// muse is the first kind whose servers AF does not WRITE anywhere. Every other kind gets a
// native config file that `mcpreg` materialises; here they ride `session/start.config.mcpServers`,
// which gate B1-4 measured end to end: with `sessionMcp` requested and granted, a stdio server
// passed only on the wire logged muse's own handshake (`clientInfo {"name":"tbh"}`, MCP
// 2025-06-18), received `MUSE_SESSION_ID` and the config's `env` additions, and its tool reached
// the model as `mcp__<server>.<tool>`. `MUSE_ENABLE_SESSION_MCP` is not needed.
//
// Three consequences, all of them decision 11's:
//
//   - the settings writer carries clamps only and writes no `mcp_servers` block, so the
//     lost-update surface of that file stays small and a member's own block there is theirs;
//   - the server set is PER SESSION, which a user-wide file cannot express;
//   - ⚠️ servers start at the FIRST TURN, not at `session/start` (measured: three session-only
//     runs spawned nothing). Any health check at creation time would read "not connected"
//     forever, so none is made.

import (
	"os"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sessionMCPServers renders the registry's session-scope definitions for this kind as MSP's
// own config map, stamped with the name of the session they are for. An error from the registry yields no servers and is reported to the caller,
// which LOGS and launches anyway — the same posture materialisation takes for every other
// kind: a broken integration must not cost the member their session.
func sessionMCPServers(owner string) (map[string]msp.SessionMCPServerConfig, error) {
	defs, err := mcpreg.ForSession(session.KindMuse)
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, nil
	}
	out := make(map[string]msp.SessionMCPServerConfig, len(defs))
	for _, d := range defs {
		out[d.Name] = mcpServerConfig(d, owner)
	}
	return out, nil
}

// mcpServerConfig converts one definition.
//
// EVERY server goes out as `mode: optional`, with no member-facing choice. The wire default is
// `required`, and a required server that fails to start aborts the whole run — so a tenant
// server whose endpoint is down would stop the agent from starting at all. The registry has
// nowhere to put the choice either (`secrets.MCPServer` has `enabled`, `targets`, `kinds` and
// `timeoutMs` and no `mode`), and decision 11 names adding one as out of scope rather than
// leaving it to be discovered here.
func mcpServerConfig(d mcpreg.ServerDef, owner string) msp.SessionMCPServerConfig {
	mode := msp.SessionMCPServerModeOptional
	cfg := msp.SessionMCPServerConfig{Mode: &mode}
	if d.Transport == mcpreg.TransportHTTP {
		// 🔴 The wire's spelling is NOT the settings file's. Decision 11 documents the
		// `mcp_servers` block's transport as `streamable_http`, and the wire union's arm is
		// `streamableHttp` — and that union is CLOSED, so an undeclared value does not
		// degrade to "this server did not start", it fails `session/start` decode and takes
		// the whole session with it. Hence the generated constant rather than a literal.
		cfg.Transport = msp.SessionMCPServerConfigTransportStreamableHTTP
		url := d.URL
		cfg.URL = &url
		if len(d.Headers) > 0 {
			cfg.Headers = copyMap(d.Headers)
		}
		return cfg
	}
	cfg.Transport = msp.SessionMCPServerConfigTransportStdio
	cmd := d.Command
	cfg.Command = &cmd
	if len(d.Args) > 0 {
		cfg.Args = append([]string(nil), d.Args...)
	}
	if len(d.Env) > 0 {
		cfg.Env = copyMap(d.Env)
	}
	// 🔴 muse scrubs an MCP child's environment, exactly as codex does — measured live, and the
	// symptom is the worst kind: AF's own `af` server STARTS, its tools reach the model, and
	// every call that writes back to the Agent answers
	// `401 missing or invalid agent token`, so a muse session is told to call af_report and
	// cannot. The wire's `env` takes VALUES (codex's `env_vars` takes names), so the values are
	// read here from the Agent's own environment.
	//
	// It is the builtins that need this, which is the same rule mcpreg applies for codex
	// (ForwardEnvNames): a user-registered server declares whatever environment it needs in the
	// definition, and that is already copied above.
	//
	// The secrets do not reach disk: measured against the real host (live_test.go), a value
	// passed in this map does not appear anywhere under muse's own store. It does reach the
	// vendor's process — which already holds the whole Agent environment, being AF's own child.
	for _, name := range mcpreg.ForwardEnvNames(d) {
		if name == agents.SessionNameEnvVar {
			// The Agent's own environment is no session's: the name is this session's, below.
			continue
		}
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		if cfg.Env == nil {
			cfg.Env = map[string]string{}
		}
		// A definition's own value wins: it is the one the member configured.
		if _, taken := cfg.Env[name]; !taken {
			cfg.Env[name] = v
		}
	}
	// The wire is per session, so it is the one place the af server can be told which session
	// it serves (mcpx.mcpOwningSession); without it every session-bound af tool guesses from
	// the working folder, which several sessions routinely share.
	if d.Origin == mcpreg.OriginBuiltin && d.ID == mcpreg.BuiltinAF && owner != "" {
		if cfg.Env == nil {
			cfg.Env = map[string]string{}
		}
		cfg.Env[agents.SessionNameEnvVar] = owner
	}
	return cfg
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
