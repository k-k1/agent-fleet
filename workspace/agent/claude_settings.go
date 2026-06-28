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

// claudeJSONPath is claude's per-user state file (project trust, onboarding, …).
// With CLAUDE_CONFIG_DIR set claude reads/writes it under that dir — NOT home.
func claudeJSONPath() string {
	return filepath.Join(claudeConfigDir(), ".claude.json")
}

// ensureFolderTrusted prepares .claude.json so an interactive claude session
// starts straight at the prompt: (1) it marks onboarding complete, and (2) it
// pre-accepts the directory-trust dialog for dir. Both are NOT skipped by
// --dangerously-skip-permissions.
//
//	hasCompletedOnboarding: when this is unset, claude re-runs the SETUP WIZARD,
//	  whose first step is "Select login method" — so a .claude.json that lost this
//	  flag (e.g. after a re-login or a workspace recreate) makes every session show
//	  the login screen EVEN WHEN credentials are valid. (This was the real cause of
//	  "claude auth not passing": creds were fine; onboarding was re-prompting login.)
//	hasTrustDialogAccepted: the per-dir "Is this a project you trust?" prompt that
//	  otherwise stalls a fresh dir (every repo, and /home/dev after node→dev).
//
// Writes once, only when something changed, atomically (rename), to minimize racing
// with claude's own writes.
func ensureFolderTrusted(dir string) {
	if dir == "" {
		return
	}
	p := claudeJSONPath()
	root := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &root)
	}
	changed := false

	if v, _ := root["hasCompletedOnboarding"].(bool); !v {
		root["hasCompletedOnboarding"] = true
		changed = true
	}
	if _, ok := root["theme"]; !ok {
		root["theme"] = "dark"
		changed = true
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[dir].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	if trusted, _ := entry["hasTrustDialogAccepted"].(bool); !trusted {
		entry["hasTrustDialogAccepted"] = true
		projects[dir] = entry
		root["projects"] = projects
		changed = true
	}

	if !changed {
		return
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".af-tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
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

// hooks.PreToolUse is an array of {matcher, hooks:[{type,command}]} entries; rtk
// (matcher "Bash") and the session-state AskUserQuestion hook coexist there, so we
// edit by matcher rather than replacing the whole array.
func hooksMap(m map[string]any) map[string]any {
	h, _ := m["hooks"].(map[string]any)
	if h == nil {
		h = map[string]any{}
	}
	return h
}

func preToolUseHasMatcher(hooks map[string]any, matcher string) bool {
	arr, _ := hooks["PreToolUse"].([]any)
	for _, e := range arr {
		if em, _ := e.(map[string]any); em != nil && em["matcher"] == matcher {
			return true
		}
	}
	return false
}

// ensurePreToolUseMatcher sets (replacing any existing) the PreToolUse entry for
// matcher to run command.
func ensurePreToolUseMatcher(hooks map[string]any, matcher, command string) {
	arr, _ := hooks["PreToolUse"].([]any)
	out := []any{}
	for _, e := range arr {
		if em, _ := e.(map[string]any); em != nil && em["matcher"] == matcher {
			continue
		}
		out = append(out, e)
	}
	out = append(out, map[string]any{
		"matcher": matcher,
		"hooks":   []any{map[string]any{"type": "command", "command": command}},
	})
	hooks["PreToolUse"] = out
}

func removePreToolUseMatcher(hooks map[string]any, matcher string) {
	arr, _ := hooks["PreToolUse"].([]any)
	out := []any{}
	for _, e := range arr {
		if em, _ := e.(map[string]any); em != nil && em["matcher"] == matcher {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = out
	}
}

// rtkEnabled treats any "rtk hook" reference in hooks as RTK being on.
func rtkEnabled(m map[string]any) bool {
	b, _ := json.Marshal(m["hooks"])
	return strings.Contains(string(b), "rtk hook")
}

// setRTK toggles only the PreToolUse/Bash entry, leaving the session-state hooks
// (AskUserQuestion, UserPromptSubmit, Stop, PostToolUse) intact.
func setRTK(m map[string]any, on bool) {
	hooks := hooksMap(m)
	if on {
		ensurePreToolUseMatcher(hooks, "Bash", "rtk hook claude")
	} else {
		removePreToolUseMatcher(hooks, "Bash")
	}
	if len(hooks) == 0 {
		delete(m, "hooks")
	} else {
		m["hooks"] = hooks
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
