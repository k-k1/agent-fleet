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
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// sessionMCPServers renders the registry's session-scope definitions for this kind as MSP's
// own config map. An error from the registry yields no servers and is reported to the caller,
// which LOGS and launches anyway — the same posture materialisation takes for every other
// kind: a broken integration must not cost the member their session.
func sessionMCPServers() (map[string]msp.SessionMCPServerConfig, error) {
	defs, err := mcpreg.ForSession(session.KindMuse)
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, nil
	}
	out := make(map[string]msp.SessionMCPServerConfig, len(defs))
	for _, d := range defs {
		out[d.Name] = mcpServerConfig(d)
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
func mcpServerConfig(d mcpreg.ServerDef) msp.SessionMCPServerConfig {
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
	return cfg
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
