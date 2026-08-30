package agy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// settingsMu serializes the settings.json read-modify-write（同時起動で片方の
// trustedWorkspaces 追記が失われると trust プロンプトで固まる）。
var settingsMu sync.Mutex

// agy の settings.json（~/.gemini/antigravity-cli/settings.json）への書き込み口。
// trustedWorkspaces の事前追加と enableTelemetry の固定に使う。agy 自身も同じ
// ファイルを書くため、未知キーは保存し、atomic rename で更新する。

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
// when the user answers "Yes, I trust this folder" (実測: 事前追加でプロンプト
// はスキップされ、メイン画面に直行する). Best-effort and idempotent.
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
// (既定オン — docs/log/32 の採用条件で必ずオフに倒す)。Called at auth completion
// AND on every agy launch (BuildLaunch / usage・context スクレイプ): a one-shot
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
