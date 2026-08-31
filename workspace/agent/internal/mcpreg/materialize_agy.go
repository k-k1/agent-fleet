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
// Not re-measured for P5: agy will not run on this host at all (no RDRAND — the
// FIPS/BoringCrypto binary aborts before it prints its version), so `agy --version`
// is as far as it gets. The shape is docs/log/32's and the one the chat path writes and
// has been live-verified through; there is no drift test for agy for the same reason.
// If agy's config form moves, this is the kind that finds out last.

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
