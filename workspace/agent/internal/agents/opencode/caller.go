package opencode

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// CallerPluginInstalled reports whether the AF caller plugin (workspace/opencode-plugin/
// agent-fleet-caller.js, seeded by the entrypoint) is where opencode loads it from. Only while
// it is can a stamped caller id be believed: the plugin overwrites whatever the model wrote,
// and without it the model's own value reaches the af server unchanged (#989).
func CallerPluginInstalled() bool {
	_, err := os.Stat(filepath.Join(paths.HomeDir(), ".config", "opencode", "plugin", "agent-fleet-caller.js"))
	return err == nil
}
