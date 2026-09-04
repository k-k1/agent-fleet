package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// settingsMu serializes the settings.json read-modify-write: if two concurrent launches lose
// one of the trustedWorkspaces appends, that session hangs on the trust prompt.
var settingsMu sync.Mutex

// The write path into agy's settings.json (~/.gemini/antigravity-cli/settings.json), used to
// pre-add trustedWorkspaces and to pin enableTelemetry. agy writes the same file itself, so
// unknown keys are preserved and updates go through an atomic rename.

// readSettings returns the parsed settings.json (empty map when absent/corrupt).
func readSettings() map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// writeSettings persists m atomically, creating the state dir when needed.
func writeSettings(m map[string]any) {
	p := settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".af-tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// EnsureWorkspaceTrusted pre-accepts agy's workspace-trust gate for dir by
// appending it to trustedWorkspaces in settings.json — exactly what agy writes
// when the user answers "Yes, I trust this folder" (measured: with the entry pre-added the
// prompt is skipped and agy goes straight to the main screen). Best-effort and idempotent.
func EnsureWorkspaceTrusted(dir string) {
	if dir == "" {
		return
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	m := readSettings()
	list, _ := m["trustedWorkspaces"].([]any)
	for _, v := range list {
		if s, ok := v.(string); ok && s == dir {
			return
		}
	}
	m["trustedWorkspaces"] = append(list, dir)
	writeSettings(m)
}

// enforceTelemetryOff pins enableTelemetry=false in settings.json. The auth
// flow toggles the Interactions data-collection opt-in off on the ToS screen
// (on by default; the adoption conditions in docs/log/32 require it off). Called at auth
// completion AND on every agy launch (BuildLaunch, and the usage/context scrapes): a one-shot
// pin doesn't survive the key being flipped or dropped later. No-op when
// already false.
func enforceTelemetryOff() {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	m := readSettings()
	if v, ok := m["enableTelemetry"].(bool); ok && !v {
		return
	}
	m["enableTelemetry"] = false
	writeSettings(m)
}
