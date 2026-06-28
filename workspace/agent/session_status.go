package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session liveness via claude hooks. claude fires UserPromptSubmit when work
// starts and Stop when it finishes a response; we wire those to
// `workspace-agent session-status <state>`, which records {state, ts} keyed by the
// claude session_id (== our deterministic sid). wireSession surfaces the state so
// the Console can badge 進行中 / 応答あり and notify on arrival. Robust and cheap:
// driven by claude's own events, no TUI parsing or transcript polling. With
// --dangerously-skip-permissions there is no tool-approval QA state, so the two
// meaningful states are working and idle(=response ready / awaiting input).

func sessionStatusDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "session-status")
}

func sessionStatusPath(sid string) string {
	return filepath.Join(sessionStatusDir(), sid+".json")
}

type sessionStatus struct {
	State string `json:"state"` // "working" | "idle"
	TS    string `json:"ts"`    // RFC3339
}

// runSessionStatusHook is `workspace-agent session-status <state> [sid]`. claude
// calls it with just <state> and supplies session_id via its hook JSON on stdin;
// the opencode plugin calls it with the sid as a second arg (it has no stdin JSON).
func runSessionStatusHook(args []string) {
	state := "idle"
	if len(args) > 0 {
		state = args[0]
	}
	sid := ""
	if len(args) > 1 {
		sid = args[1] // opencode plugin path
	}
	if sid == "" {
		var in struct {
			SessionID string `json:"session_id"`
		}
		_ = json.NewDecoder(os.Stdin).Decode(&in)
		sid = in.SessionID
	}
	if sid == "" {
		return
	}
	_ = os.MkdirAll(sessionStatusDir(), 0o700)
	b, _ := json.Marshal(sessionStatus{State: state, TS: time.Now().Format(time.RFC3339)})
	_ = os.WriteFile(sessionStatusPath(sid), b, 0o600)
}

func readSessionStatus(sid string) (sessionStatus, bool) {
	var s sessionStatus
	b, err := os.ReadFile(sessionStatusPath(sid))
	if err != nil {
		return s, false
	}
	if json.Unmarshal(b, &s) != nil {
		return s, false
	}
	return s, true
}

func removeSessionStatus(sid string) { _ = os.Remove(sessionStatusPath(sid)) }

// statusHookCmd is the absolute command claude runs for an event (absolute so it
// resolves in claude's hook context regardless of PATH).
func statusHookCmd(state string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "/usr/local/bin/workspace-agent"
	}
	return exe + " session-status " + state
}

// ensureStatusHooks makes settings.json carry the hooks that feed session state,
// merging without disturbing the rtk PreToolUse/Bash hook or other settings.
// Idempotent; called at agent startup (before sessions launch). States:
//   UserPromptSubmit → working   (user sent a prompt)
//   Stop             → idle      (response done / awaiting user)
//   PreToolUse(AskUserQuestion)  → question (claude is asking the user; QA来た)
//   PostToolUse(AskUserQuestion) → working  (question answered, continuing)
func ensureStatusHooks() {
	m := readClaudeSettings()
	hooks := hooksMap(m)
	changed := false

	// Simple (matcher-less) events.
	for event, state := range map[string]string{"UserPromptSubmit": "working", "Stop": "idle"} {
		if b, _ := json.Marshal(hooks[event]); !strings.Contains(string(b), "session-status") {
			hooks[event] = []any{map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": statusHookCmd(state)}},
			}}
			changed = true
		}
	}
	// AskUserQuestion: a distinct "needs your answer" state (not suppressed by
	// --dangerously-skip-permissions, unlike tool-permission prompts).
	if !preToolUseHasMatcher(hooks, "AskUserQuestion") {
		ensurePreToolUseMatcher(hooks, "AskUserQuestion", statusHookCmd("question"))
		changed = true
	}
	if b, _ := json.Marshal(hooks["PostToolUse"]); !strings.Contains(string(b), "session-status") {
		hooks["PostToolUse"] = []any{map[string]any{
			"matcher": "AskUserQuestion",
			"hooks":   []any{map[string]any{"type": "command", "command": statusHookCmd("working")}},
		}}
		changed = true
	}

	if changed {
		m["hooks"] = hooks
		_ = writeClaudeSettings(m)
	}
}
