package opencode

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// callerPluginSrc is the plugin as the image ships it; the entrypoint copies it into
// opencode's plugin directory on every start. A variable so tests can stand one in.
var callerPluginSrc = "/usr/local/share/agent-fleet/opencode-plugin/agent-fleet-caller.js"

// CallerPluginCurrent reports whether opencode is loading the AF caller plugin exactly as the
// image ships it. Only then can a stamped caller id be believed: that plugin overwrites whatever
// the model wrote, while a missing, emptied or rewritten one lets the model's own value reach
// the af server unchanged (#989).
//
// The plugin directory is the HOME-based one every af writer uses (paths.OpencodeConfigDir).
// opencode itself follows XDG_CONFIG_HOME, so when that points elsewhere the file checked here
// is not the one opencode loads, and the answer is no.
func CallerPluginCurrent() bool {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.Clean(x) != filepath.Join(paths.HomeDir(), ".config") {
		return false
	}
	want, err := os.ReadFile(callerPluginSrc)
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := os.ReadFile(filepath.Join(paths.OpencodeConfigDir(), "plugin", "agent-fleet-caller.js"))
	return err == nil && bytes.Equal(got, want)
}

// SetCallerPluginSrcForTest points CallerPluginCurrent at a stand-in for the shipped plugin.
func SetCallerPluginSrcForTest(p string) (restore func()) {
	old := callerPluginSrc
	callerPluginSrc = p
	return func() { callerPluginSrc = old }
}
