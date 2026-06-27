package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Claude settings: read/write a curated subset of the workspace's claude
// settings.json (Remote Control, push notifications, and the RTK PreToolUse hook)
// so the Console can toggle them. Changes take effect for NEW claude sessions
// (claude reads settings at startup). Unknown keys in the file are preserved.

// claudeConfigDir resolves where claude reads/writes its state. P3-5 段2 relocates
// plaintext claude state out of home via CLAUDE_CONFIG_DIR; when unset it is the
// classic ~/.claude. Both settings.json and projects/*.jsonl live under this dir,
// so session resume detection must agree with it (see sessionJSONLExists).
func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".claude")
}

func claudeSettingsPath() string {
	return filepath.Join(claudeConfigDir(), "settings.json")
}

func readClaudeSettings() map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(claudeSettingsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeClaudeSettings(m map[string]any) error {
	p := claudeSettingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

func settingBool(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// The RTK hook: rewrite Bash tool calls through `rtk hook claude` for token
// savings. We treat any "rtk hook" reference in hooks as RTK being on; toggling
// installs/removes the standard PreToolUse/Bash entry.
var rtkHooks = map[string]any{
	"PreToolUse": []any{
		map[string]any{
			"matcher": "Bash",
			"hooks": []any{
				map[string]any{"type": "command", "command": "rtk hook claude"},
			},
		},
	},
}

func rtkEnabled(m map[string]any) bool {
	b, _ := json.Marshal(m["hooks"])
	return strings.Contains(string(b), "rtk hook")
}

func setRTK(m map[string]any, on bool) {
	if on {
		m["hooks"] = rtkHooks
	} else {
		delete(m, "hooks")
	}
}

func rtkAvailable() bool {
	_, err := exec.LookPath("rtk")
	return err == nil
}

func claudeSettingsBody(m map[string]any) map[string]any {
	return map[string]any{
		"remoteControlAtStartup":            settingBool(m, "remoteControlAtStartup"),
		"agentPushNotifEnabled":             settingBool(m, "agentPushNotifEnabled"),
		"skipDangerousModePermissionPrompt": settingBool(m, "skipDangerousModePermissionPrompt"),
		"rtk_enabled":                       rtkEnabled(m),
		"rtk_available":                     rtkAvailable(),
	}
}

func handleClaudeSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, claudeSettingsBody(readClaudeSettings()))
}

type claudeSettingsReq struct {
	RemoteControlAtStartup *bool `json:"remoteControlAtStartup"`
	AgentPushNotifEnabled  *bool `json:"agentPushNotifEnabled"`
	RTK                    *bool `json:"rtk"`
}

func handleClaudeSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req claudeSettingsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	m := readClaudeSettings()
	if req.RemoteControlAtStartup != nil {
		m["remoteControlAtStartup"] = *req.RemoteControlAtStartup
	}
	if req.AgentPushNotifEnabled != nil {
		m["agentPushNotifEnabled"] = *req.AgentPushNotifEnabled
	}
	if req.RTK != nil {
		setRTK(m, *req.RTK)
	}
	if err := writeClaudeSettings(m); err != nil {
		writeErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, claudeSettingsBody(m))
}
