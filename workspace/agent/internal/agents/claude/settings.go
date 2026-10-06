package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
// A linked git worktree whose .claude/settings.json pre-approves tools (permissions.allow)
// is judged by the MAIN checkout's trust, not its own (measured, 2.1.288/2.1.289: the
// dialog reappears with only the worktree trusted and goes away once the main checkout is).
// So the main checkout is trusted too; an explicit false there is overwritten.
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
	for _, d := range append([]string{dir}, mainCheckoutOf(dir)...) {
		entry, _ := projects[d].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}
		if trusted, _ := entry["hasTrustDialogAccepted"].(bool); !trusted {
			entry["hasTrustDialogAccepted"] = true
			projects[d] = entry
			root["projects"] = projects
			changed = true
		}
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

// mainCheckoutOf returns the main checkout's root when dir is a linked git worktree
// (its .git is a file "gitdir: <common>/worktrees/<name>"), else nil. Read from the files
// rather than exec'd git: this runs on every launch and must not depend on PATH.
//
// The checkout is core.worktree of <common>/config when set (submodules and
// --separate-git-dir, where <common> is not <checkout>/.git); otherwise the parent of a
// <common> named ".git". Anything else (a bare repository) yields nil: guessing would
// write trust for a directory that is not a checkout. Known gap: a --separate-git-dir store
// named ".git" without core.worktree is indistinguishable from a plain checkout, so its
// parent is returned (harmless extra entry; the real main stays untrusted as before).
func mainCheckoutOf(dir string) []string {
	b, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return nil
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return nil
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	gitdir = filepath.Clean(gitdir) // git accepts a trailing "/" or "/."
	worktrees := filepath.Dir(gitdir)
	if filepath.Base(worktrees) != "worktrees" {
		return nil
	}
	common := filepath.Dir(worktrees)
	if wt := coreWorktree(common); wt != "" {
		return []string{wt}
	}
	if filepath.Base(common) != ".git" {
		return nil
	}
	return []string{filepath.Dir(common)}
}

// coreWorktree returns core.worktree from <common>/config as an absolute path, or "". It
// asks git (sections, key case, last-wins, quoting and comments are git's to parse); with
// git absent or the key unset the answer is "" and the caller falls back to the layout rule.
func coreWorktree(common string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "config", "--file", filepath.Join(common, "config"),
		"--includes", "--null", "--get", "core.worktree").Output()
	v := strings.TrimSuffix(string(out), "\x00") // --null: a value may itself end in a newline
	if err != nil || v == "" {
		return ""
	}
	if !filepath.IsAbs(v) {
		v = filepath.Join(common, v)
	}
	return filepath.Clean(v)
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

// hooksMap preserves the user's hook events while Fleet installs its own commands.
func hooksMap(m map[string]any) map[string]any {
	h, _ := m["hooks"].(map[string]any)
	if h == nil {
		h = map[string]any{}
	}
	return h
}

// hookCommandMatches compares command arguments, allowing a working alternate agent
// path for status hooks. Mentions inside scripts or prompt hooks are not ours.
func hookCommandMatches(h any, command string) bool {
	hm, _ := h.(map[string]any)
	if hm["type"] != "command" {
		return false
	}
	cmd, _ := hm["command"].(string)
	got, want := strings.Fields(cmd), strings.Fields(command)
	if len(got) != len(want) || len(want) == 0 {
		return false
	}
	if len(want) == 3 && want[1] == "session-status" {
		return filepath.IsAbs(got[0]) && got[1] == want[1] && got[2] == want[2]
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func preToolUseHasCommand(hooks map[string]any, matcher, command string) bool {
	arr, _ := hooks["PreToolUse"].([]any)
	for _, e := range arr {
		em, _ := e.(map[string]any)
		if em["matcher"] != matcher {
			continue
		}
		list, _ := em["hooks"].([]any)
		for _, h := range list {
			if hookCommandMatches(h, command) {
				return true
			}
		}
	}
	return false
}

func ensurePreToolUseCommand(hooks map[string]any, matcher, command string) {
	if preToolUseHasCommand(hooks, matcher, command) {
		return
	}
	arr, _ := hooks["PreToolUse"].([]any)
	hooks["PreToolUse"] = append(arr, map[string]any{
		"matcher": matcher,
		"hooks":   []any{map[string]any{"type": "command", "command": command}},
	})
}

// removePreToolUseCommand removes individual managed hooks, preserving siblings
// and entry attributes when a user groups commands under the same matcher.
func removePreToolUseCommand(hooks map[string]any, matcher, command string) {
	arr, _ := hooks["PreToolUse"].([]any)
	out := []any{}
	for _, e := range arr {
		em, _ := e.(map[string]any)
		if em["matcher"] != matcher {
			out = append(out, e)
			continue
		}
		list, _ := em["hooks"].([]any)
		kept := []any{}
		removed := false
		for _, h := range list {
			if hookCommandMatches(h, command) {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		if !removed {
			out = append(out, e)
			continue
		}
		if len(kept) != 0 {
			em["hooks"] = kept
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = out
	}
}

const rtkHookCommand = "rtk hook claude"

func rtkEnabled(m map[string]any) bool {
	return preToolUseHasCommand(hooksMap(m), "Bash", rtkHookCommand)
}

// setRTK toggles only RTK's command, preserving user and session-state hooks.
func setRTK(m map[string]any, on bool) {
	hooks := hooksMap(m)
	if on {
		ensurePreToolUseCommand(hooks, "Bash", rtkHookCommand)
	} else {
		removePreToolUseCommand(hooks, "Bash", rtkHookCommand)
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
