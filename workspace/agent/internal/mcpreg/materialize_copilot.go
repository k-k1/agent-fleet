package mcpreg

// copilot's native MCP config: the `mcpServers` object in
// $COPILOT_HOME/mcp-config.json (~/.copilot/mcp-config.json by default) — its own
// file, not the config.json that carries trustedFolders.
//
// Measured on GitHub Copilot CLI 1.0.75 — `copilot mcp add` in an isolated
// COPILOT_HOME writes the file at 0600:
//
//	"mcpServers": {
//	  "loc": {"tools":["*"],"type":"local","command":"/bin/echo","args":["a","b"],"env":{"FOO":"bar"}},
//	  "rem": {"tools":["*"],"timeout":12000,"type":"http","url":"https://…","headers":{"Authorization":"Bearer …"}}
//	}
//
// Two keys are copilot's own:
//
//   - `tools` is a per-server tool filter and `["*"]` is what `mcp add` defaults to
//     (`--tools ""` means none). af writes the same default: a server the user
//     attached is meant to be usable, so leaving the filter to an undocumented default
//     is the one way this could silently do nothing.
//   - `timeout` is in MILLISECONDS, which is what a definition already carries — no
//     conversion, unlike codex's startup_timeout_sec.

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func copilotMCPConfigPath() string {
	return filepath.Join(paths.CopilotHome(), "mcp-config.json")
}

// copilotServers builds copilot's `mcpServers` map. Session-only: the assistant chat
// has no copilot provider, so unlike ClaudeServers this has one consumer.
func copilotServers(defs []ServerDef) map[string]any {
	out := map[string]any{}
	for _, d := range defs {
		e := map[string]any{"tools": []any{"*"}}
		if d.TimeoutMS > 0 {
			e["timeout"] = d.TimeoutMS
		}
		if d.Transport == TransportHTTP {
			e["type"], e["url"] = "http", d.URL
			if len(d.Headers) > 0 {
				e["headers"] = anyMap(d.Headers)
			}
			out[d.Name] = e
			continue
		}
		e["type"], e["command"] = "local", d.Command
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

var copilotConfig = jsonConfig{
	path:    copilotMCPConfigPath,
	key:     "mcpServers",
	entries: copilotServers,
}

func materializeCopilot(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	return copilotConfig.materialize(defs, prev)
}
