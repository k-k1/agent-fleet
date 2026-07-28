package mcpreg

// opencode's native MCP config: the `mcp` map in ~/.config/opencode/opencode.jsonc.
//
// Measured on opencode 1.18.7 — `opencode mcp add` in an isolated HOME writes:
//
//	"mcp": {
//	  "loc": {"type":"local","command":["/bin/echo","a","b"],"environment":{"FOO":"bar"}},
//	  "rem": {"type":"remote","url":"https://…","headers":{"Authorization":"Bearer …"}}
//	}
//
// i.e. the shape OpencodeServers already builds for the assistant chat, which also
// sets "enabled": true — opencode's default, said out loud.
//
// Three opencode-specific facts, all measured on 1.18.7:
//
//   - opencode reads BOTH opencode.jsonc and opencode.json and MERGES them. So af has
//     to commit to exactly one file; writing "the other one" would leave a second copy
//     of every server behind. It edits whichever exists (.jsonc first — what
//     `opencode mcp add` creates in a fresh HOME, and what entrypoint.sh:416 seeds),
//     and creates .jsonc when neither does.
//   - the file is .jsonc BY NAME, so it may legally carry comments that
//     encoding/json cannot read. The shared rule then refuses it, which is the same
//     bargain entrypoint.sh already makes for the permission block: a config af cannot
//     parse is a config af must not reformat away.
//   - unknown members are ignored rather than rejected, but `mcp add` has no timeout
//     flag and writes no timeout key, so a definition's TimeoutMS is dropped here
//     instead of being written somewhere with no evidence it does anything.

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// opencodeConfigNames are the config file names opencode merges, in the order af
// prefers to edit them.
var opencodeConfigNames = []string{"opencode.jsonc", "opencode.json"}

func opencodeConfigPath() string {
	dir := paths.OpencodeConfigDir()
	for _, name := range opencodeConfigNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(dir, opencodeConfigNames[0])
}

var opencodeConfig = jsonConfig{
	path:    opencodeConfigPath,
	key:     "mcp",
	entries: OpencodeServers,
	// The schema line is what opencode's own writers put at the top of a file they
	// create; a config af conjured without it would lose editor completion.
	seed: map[string]any{"$schema": "https://opencode.ai/config.json"},
}

func materializeOpencode(defs []ServerDef, prev []string) (written, removed []string, changed bool, err error) {
	return opencodeConfig.materialize(defs, prev)
}
