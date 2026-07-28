package mcpreg

// claude's native MCP config: the `mcpServers` object in
// $CLAUDE_CONFIG_DIR/.claude.json (the user scope — `claude mcp add -s user` writes
// exactly here).
//
// Measured on claude 2.1.220 in an isolated CLAUDE_CONFIG_DIR:
//
//	"mcpServers": {
//	  "srv": {"type":"stdio","command":"/bin/echo","args":["a"],"env":{"K":"v"}},
//	  "rem": {"type":"http","url":"https://…","headers":{"Authorization":"Bearer …"}}
//	}
//
// which is the shape ClaudeServers already builds for the assistant chat — one
// serializer feeds both consumers.
//
// .claude.json is claude's own live state file (onboarding flags, per-project trust,
// history), which is why the shared rules in materialize_json.go are written the way
// they are: refuse an unparseable file rather than cost the user their trust dialogs,
// and write only on a real change, because claude rewrites this file continuously.

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

func claudeJSONPath() string {
	return filepath.Join(paths.ClaudeConfigDir(), ".claude.json")
}

var claudeConfig = jsonConfig{
	path:    claudeJSONPath,
	key:     "mcpServers",
	entries: ClaudeServers,
}

func materializeClaude(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	return claudeConfig.materialize(defs, prev)
}
