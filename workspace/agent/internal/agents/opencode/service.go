package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// ServiceConfigPath is opencode 2's service settings file (`opencode service set …` edits it).
func ServiceConfigPath() string {
	return filepath.Join(paths.OpencodeConfigDir(), "service.json")
}

// EnsureServiceDisabled keeps opencode 2 from starting its shared per-user background service.
//
// Measured on 2.0.28: with the service enabled, even `opencode models` or `opencode run`
// starts it (port 49374 by default), and the service keeps the environment of the
// process that started it. Every later command, whichever session it belongs to, then talks
// to a server holding the first caller's AF_SESSION_SID, engine token and provider keys.
// With `disabled: true` each command runs a private server of its own, which is what af's
// launch line (--standalone) already assumes.
//
// A `disabled` the member set themselves is respected, and so is every other key. A file that
// is not plain JSON is left alone.
func EnsureServiceDisabled() error {
	path := ServiceConfigPath()
	cfg := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		// `null` unmarshals without error into a nil map; only a JSON object is a config.
		if json.Unmarshal(b, &cfg) != nil || cfg == nil {
			return ErrUnreadableConfig
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, set := cfg["disabled"]; set {
		return nil
	}
	cfg["disabled"] = true
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(out, '\n'), 0o644)
}
