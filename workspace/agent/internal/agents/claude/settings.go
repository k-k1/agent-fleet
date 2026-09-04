package claude

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Claude settings: read/write a curated subset of the workspace's claude
// settings.json (Remote Control, push notifications, and the RTK PreToolUse hook)
// so the Console can toggle them. Changes take effect for NEW claude sessions
// (claude reads settings at startup). Unknown keys in the file are preserved.

// ConfigDir resolves where claude reads/writes its state. P3-5 stage 2 relocates
// plaintext claude state out of home via CLAUDE_CONFIG_DIR; when unset it is the
// classic ~/.claude. Both settings.json and projects/*.jsonl live under this dir,
// so session resume detection must agree with it (see SessionJSONLExists).
func ConfigDir() string { return paths.ClaudeConfigDir() }

func settingsPath() string {
	return filepath.Join(ConfigDir(), "settings.json")
}

// claudeJSONPath is claude's per-user state file (project trust, onboarding, …).
// With CLAUDE_CONFIG_DIR set claude reads/writes it under that dir — NOT home.
func claudeJSONPath() string {
	return filepath.Join(ConfigDir(), ".claude.json")
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

// settingsMu serializes read-modify-write cycles on settings.json inside this process, so
// concurrent PUTs cannot drop one side's change. Against claude's own writes the defence is
// writeSettings' tmp+rename, which keeps a torn JSON file from ever appearing.
var settingsMu sync.Mutex

func readSettings() map[string]any {
	m := map[string]any{}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeSettings(m map[string]any) error {
	p := settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// tmp+rename, the same practice as ensureFolderTrusted: dying halfway leaves no partial JSON.
	tmp := p + ".af-tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
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

// RTKAvailable reports whether the rtk binary is in the image (shared with the
// codex/opencode rtk toggle in package main's agent_rtk.go).
func RTKAvailable() bool {
	_, err := exec.LookPath("rtk")
	return err == nil
}

func settingsBody(m map[string]any) map[string]any {
	return map[string]any{
		"remoteControlAtStartup":            settingBool(m, "remoteControlAtStartup"),
		"agentPushNotifEnabled":             settingBool(m, "agentPushNotifEnabled"),
		"skipDangerousModePermissionPrompt": settingBool(m, "skipDangerousModePermissionPrompt"),
		"rtk_enabled":                       rtkEnabled(m),
		"rtk_available":                     RTKAvailable(),
	}
}

// HandleSettingsGet serves GET /claude/settings for the Console toggles.
func HandleSettingsGet(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, settingsBody(readSettings()))
}

type settingsReq struct {
	RemoteControlAtStartup *bool `json:"remoteControlAtStartup"`
	AgentPushNotifEnabled  *bool `json:"agentPushNotifEnabled"`
	RTK                    *bool `json:"rtk"`
}

// HandleSettingsPut serves PUT /claude/settings.
func HandleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req settingsReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	m := readSettings()
	if req.RemoteControlAtStartup != nil {
		m["remoteControlAtStartup"] = *req.RemoteControlAtStartup
	}
	if req.AgentPushNotifEnabled != nil {
		m["agentPushNotifEnabled"] = *req.AgentPushNotifEnabled
	}
	if req.RTK != nil {
		setRTK(m, *req.RTK)
	}
	if err := writeSettings(m); err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "write_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, settingsBody(m))
}
