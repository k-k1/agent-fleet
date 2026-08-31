package mcpreg

// kiro's native MCP config: the `mcpServers` object in ~/.kiro/settings/mcp.json
// (kiro's "global" scope; the workspace scope is a .kiro/settings/mcp.json under the
// repo, which belongs to the user, not to af).
//
// Measured on kiro-cli 2.14.2. `kiro-cli mcp add` REQUIRES A LOGIN — so does every
// other `mcp` subcommand — which is why docs/log/48 §8.1 could only quote the flag help
// until now. Run against a logged-in CLI, `mcp add --scope global` produces:
//
//	"mcpServers": {
//	  "loc": {"command":"/bin/echo","args":["a","b"],"env":{"FOO":"bar"},"timeout":12000},
//	  "rem": {"url":"https://…","timeout":12000}
//	}
//
// There is no `type` discriminator: `url` means remote, `command` means stdio, exactly
// like agy and cursor.
//
// The remote half needed more than `mcp add`, which has no header flag. `headers` was
// confirmed END TO END: a hand-written entry pointed at a header-logging listener, and
// a real `kiro-cli chat --no-interactive` turn arrived carrying both headers plus
// `MCP-Protocol-Version: 2025-06-18`. That matters because tenant-distributed servers
// are remote-only (ADR0031 決定 2) and authenticate with a header — a kiro that
// dropped headers would take exactly the servers an admin distributes and nothing else.
//
// `timeout` is in milliseconds, like copilot's.

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func kiroMCPConfigPath() string {
	return filepath.Join(paths.KiroHome(), "settings", "mcp.json")
}

// kiroServers builds kiro's `mcpServers` map. `disabled` is left out: kiro defaults it
// to false, and af only ever materializes servers that are enabled in the registry.
func kiroServers(defs []ServerDef) map[string]any {
	out := map[string]any{}
	for _, d := range defs {
		e := map[string]any{}
		if d.TimeoutMS > 0 {
			e["timeout"] = d.TimeoutMS
		}
		if d.Transport == TransportHTTP {
			e["url"] = d.URL
			if len(d.Headers) > 0 {
				e["headers"] = anyMap(d.Headers)
			}
			out[d.Name] = e
			continue
		}
		e["command"] = d.Command
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

var kiroConfig = jsonConfig{
	path:    kiroMCPConfigPath,
	key:     "mcpServers",
	entries: kiroServers,
}

func materializeKiro(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	return kiroConfig.materialize(defs, prev)
}
