package mcpreg

// cursor's native MCP config: the `mcpServers` object in ~/.cursor/mcp.json, which
// cursor merges with the repo's .cursor/mcp.json (the latter belongs to the user).
//
// Measured on cursor-agent 2026.07.23. cursor-agent has NO `mcp add` — its `mcp`
// subcommand only lists, enables and logs in — so the reference here is cursor reading
// af's file rather than cursor writing its own:
//
//	"mcpServers": {
//	  "loc": {"command":"/bin/echo","args":["a"],"env":{"FOO":"bar"}},
//	  "rem": {"url":"https://…","headers":{"Authorization":"Bearer …"}}
//	}
//
// `cursor-agent mcp list` reported both remote forms — with and without a `type`
// member — as "ready", and a header-logging listener saw the custom headers arrive.
// The bundle agrees: the entry parser keys on `"command" in o` for stdio and `"url"`
// for remote, and reads only `{command,args,env,cwd}` / `{url,headers}`. af writes the
// discriminator-free form, which is the one the parser actually branches on.
//
// Two things deliberately not written:
//
//   - `timeout` — no such member in the parser, so a definition's TimeoutMS is dropped
//     rather than written where nothing reads it.
//   - the approval list. cursor keeps enabled/disabled servers in its OWN state
//     (cli-config.json and a disabled-servers file), and a freshly written server comes
//     up "enabled and approved" already — so af has no reason to reach into a second
//     file it does not own.

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func cursorMCPConfigPath() string {
	return filepath.Join(paths.CursorHome(), "mcp.json")
}

// cursorServers builds cursor's `mcpServers` map. It comes out looking like agy's,
// but it is written separately on purpose: they are two vendors' formats that happen
// to agree today, and folding them together would mean one of them drifting silently
// into the other's file.
func cursorServers(defs []ServerDef) map[string]any {
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
		e := map[string]any{"command": d.Command}
		if len(d.Args) > 0 {
			e["args"] = anySlice(d.Args)
		}
		if env := cursorStdioEnv(d); len(env) > 0 {
			e["env"] = env
		}
		out[d.Name] = e
	}
	return out
}

// cursorStdioEnv is a stdio entry's `env`: the definition's own values, plus a reference to
// every variable a builtin needs from AF (extraEnvVars).
//
// cursor scrubs its MCP children's environment — measured on 2026.09.23, the af child got
// HOME, PATH, SHELL and TERM and nothing else — so without these the af server has no
// AGENT_TOKEN (every call back to the Agent is a 401) and no AF_SESSION_NAME. cursor expands
// `${env:NAME}` in this file from its own process environment (measured), which is how the
// values get through without a secret being written to disk. An unset variable is left as the
// literal `${env:NAME}`; mcpx.dropUnexpandedEnv clears those before anything reads them.
func cursorStdioEnv(d ServerDef) map[string]any {
	out := anyMap(d.Env)
	for _, name := range extraEnvVars(d) {
		if _, taken := out[name]; !taken {
			out[name] = "${env:" + name + "}"
		}
	}
	return out
}

var cursorConfig = jsonConfig{
	path:    cursorMCPConfigPath,
	key:     "mcpServers",
	entries: cursorServers,
}

func materializeCursor(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	return cursorConfig.materialize(defs, prev)
}
