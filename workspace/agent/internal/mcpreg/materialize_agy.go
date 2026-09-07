package mcpreg

// agy's native MCP config: the `mcpServers` object in
// ~/.gemini/config/mcp_config.json. agy hardcodes ~/.gemini off $HOME (its gemini-cli
// lineage), and its MCP config is GLOBAL-ONLY — there is no project-scoped file and no
// launch flag, which is precisely why the assistant chat has to give agy a whole
// isolated HOME per conversation (chat_providers.go chatAgyHome).
//
// An INTERACTIVE agy session is the easy case by comparison: it runs in the user's
// real HOME, so this writes the one file agy reads, and AgyServers needs no env
// overlay — a stdio child already inherits the right HOME instead of the chat's
// isolated one.
//
// The shape here is docs/log/32's — the one the chat path writes and has been
// live-verified through — and it is NOT re-measured against `agy mcp list`, so if agy's
// config form moves this is the kind that finds out last. That gap used to be forced (the
// development host could not start agy at all); since hostcaps learned to mask the CPU's
// withdrawn RDRAND it is merely unclaimed, and a drift test like the other kinds' is
// buildable here now.

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func agyMCPConfigPath() string {
	return filepath.Join(paths.GeminiHome(), "config", "mcp_config.json")
}

var agyConfig = jsonConfig{
	path:    agyMCPConfigPath,
	key:     "mcpServers",
	entries: func(defs []ServerDef) map[string]any { return AgyServers(defs, nil) },
}

func materializeAgy(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	return agyConfig.materialize(defs, prev)
}
