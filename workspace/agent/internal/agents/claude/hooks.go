package claude

import (
	"encoding/json"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// Session liveness via claude hooks. claude fires UserPromptSubmit when work
// starts and Stop when it finishes a response; we wire those to
// `workspace-agent session-status <state>`, which records {state, ts} keyed by our
// deterministic slot sid (claude's hook session_id, normalized — sid.go).
// wireSession surfaces the state so
// the Console can badge 進行中 / 応答あり and notify on arrival. Robust and cheap:
// driven by claude's own events, no TUI parsing or transcript polling. With
// --dangerously-skip-permissions there is no tool-approval QA state, so the two
// meaningful states are working and idle(=response ready / awaiting input).
//
// 状態と pending ペイロードのストア本体は internal/status（docs/log/23 残① Wave A）;
// このファイルは claude settings への hook 配線だけを持つ。session-status
// サブコマンドの入口（hook stdin の解読）は package main の session_status.go。

// statusHookCmd is the absolute command claude runs for an event (absolute so it
// resolves in claude's hook context regardless of PATH — and never a volatile path,
// see paths.ConfigExePath).
func statusHookCmd(state string) string {
	return paths.ConfigExePath() + " session-status " + state
}

// permToolMatcher is the PreToolUse regex for edit/command tools whose permission
// prompts we surface (with the file/command) in the Console.
const permToolMatcher = "Write|Edit|MultiEdit|NotebookEdit|Bash"

// EnsureStatusHooks makes settings.json carry the hooks that feed session state,
// merging without disturbing the rtk PreToolUse/Bash hook or other settings.
// Idempotent; called at agent startup (before sessions launch). States:
//
//	UserPromptSubmit → working   (user sent a prompt)
//	Stop             → idle      (response done / awaiting user)
//	SessionStart     → boot      (fresh/resumed → idle; skips auto-compact)
//	PreToolUse(AskUserQuestion)  → question (claude is asking the user; QA来た)
//	PostToolUse(*)   → working   (every completed tool re-asserts working — heartbeat)
func EnsureStatusHooks() {
	m := readSettings()
	hooks := hooksMap(m)
	changed := repairStatusHookExe(hooks)

	// Simple (matcher-less) events. MessageDisplay fires as the assistant's text streams
	// (before the turn's tool_use); we buffer it (state "message" never changes status)
	// so a pending AskUserQuestion can surface the prose that preceded it.
	for event, state := range map[string]string{"UserPromptSubmit": "working", "Stop": "idle", "MessageDisplay": "message", "SessionStart": "boot"} {
		if b, _ := json.Marshal(hooks[event]); !strings.Contains(string(b), "session-status") {
			// APPEND to whatever the user already has there — replacing the array
			// wholesale would silently drop their own hooks for the event.
			list, _ := hooks[event].([]any)
			hooks[event] = append(list, map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": statusHookCmd(state)}},
			})
			changed = true
		}
	}
	// AskUserQuestion → question, ExitPlanMode → plan: distinct "needs your input"
	// states (not suppressed by --dangerously-skip-permissions). Their tool_use is
	// written to the transcript only after it's resolved, so the hook is how the
	// Console learns about the pending question / plan.
	if !preToolUseHasMatcher(hooks, "AskUserQuestion") {
		ensurePreToolUseMatcher(hooks, "AskUserQuestion", statusHookCmd("question"))
		changed = true
	}
	if !preToolUseHasMatcher(hooks, "ExitPlanMode") {
		ensurePreToolUseMatcher(hooks, "ExitPlanMode", statusHookCmd("plan"))
		changed = true
	}
	// Edit/command tools: record what's about to run so the permission block (below)
	// can name the file/command. Matcher is a regex over the tool name; permtool never
	// changes status, so it's harmless when no prompt follows (bypass/accept modes).
	if !preToolUseHasMatcher(hooks, permToolMatcher) {
		ensurePreToolUseMatcher(hooks, permToolMatcher, statusHookCmd("permtool"))
		changed = true
	}
	// PostToolUse(*) → working: a catch-all heartbeat. Every completed tool re-asserts
	// "working", so a long turn can't false-idle if the working status file is lost
	// mid-turn (a transient prompt frame's self-heal, or bypass mode where MessageDisplay/
	// permtool don't Persist). It also subsumes the old per-tool resumes — an answered
	// AskUserQuestion / approved ExitPlanMode / granted permission all fire their tool's
	// PostToolUse on resolution, landing back on working. Empty matcher matches all tools.
	// Migrate older settings that carried the two specific matchers instead.
	// 判定は「自コマンド入りの matcher 無しエントリの有無」で行う: `"matcher":""`
	// の有無だけ見ると、ユーザー自身の matcher 無しエントリを自分のものと誤認して
	// ハートビートが永久に未インストールのままになる（false-idle 対策が黙って欠落）。
	// インストール時は自コマンド入りの旧エントリ（レガシー個別 matcher 形）だけを
	// 除去し、ユーザーのエントリは保持する。
	if !postToolUseHasAF(hooks) {
		hooks["PostToolUse"] = append(stripSessionStatusEntries(hooks["PostToolUse"]),
			map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("working")}}})
		changed = true
	}
	// Notification → permission: fires when claude is blocked on a tool-permission
	// prompt (notification_type=permission_prompt). The handler ignores other
	// notification types, so this matcher-less hook is safe to always set.
	if b, _ := json.Marshal(hooks["Notification"]); !strings.Contains(string(b), "session-status") {
		list, _ := hooks["Notification"].([]any)
		hooks["Notification"] = append(list, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("permission")}},
		})
		changed = true
	}

	if changed {
		m["hooks"] = hooks
		_ = writeSettings(m)
	}
}

// postToolUseHasAF reports whether PostToolUse already carries OUR catch-all
// heartbeat: a matcher-less entry whose command runs session-status.
func postToolUseHasAF(hooks map[string]any) bool {
	arr, _ := hooks["PostToolUse"].([]any)
	for _, e := range arr {
		em, _ := e.(map[string]any)
		if em == nil {
			continue
		}
		if matcher, _ := em["matcher"].(string); matcher != "" {
			continue
		}
		if b, _ := json.Marshal(em["hooks"]); strings.Contains(string(b), "session-status") {
			return true
		}
	}
	return false
}

// repairStatusHookExe repoints OUR hook commands whose agent binary can no longer
// run — deleted, or sitting in a volatile directory. Every install check in
// EnsureStatusHooks matches on the CONTENT ("session-status"), which is what kept the
// exponential re-wrapping of the statusLine out of the hooks; the flip side is that a
// stale path passes those checks forever, so a hook written by a build that has since
// been removed would stay in settings.json and fail on every event (no working/idle,
// no question — the session looks frozen in the Console). Reports whether it changed
// anything. A user's own hooks are untouched: only `<exe> session-status <state>` matches.
func repairStatusHookExe(hooks map[string]any) bool {
	want := paths.ConfigExePath()
	changed := false
	for _, ev := range hooks {
		entries, _ := ev.([]any)
		for _, e := range entries {
			em, _ := e.(map[string]any)
			list, _ := em["hooks"].([]any)
			for _, h := range list {
				hm, _ := h.(map[string]any)
				cmd, _ := hm["command"].(string)
				if next, ok := repointStatusHookCmd(cmd, want); ok {
					hm["command"] = next
					changed = true
				}
			}
		}
	}
	return changed
}

// repointStatusHookCmd rewrites `<exe> session-status <state>` onto want when the
// recorded exe is unusable. "" / false when the command isn't ours or is still fine
// (a different but working path — e.g. a host install — is left alone).
func repointStatusHookCmd(cmd, want string) (string, bool) {
	f := strings.Fields(cmd)
	if len(f) < 2 || f[1] != "session-status" || f[0] == want || !paths.ExeUnusable(f[0]) {
		return "", false
	}
	f[0] = want
	return strings.Join(f, " "), true
}

// stripSessionStatusEntries removes OUR entries (command contains session-status)
// from an event's hook array, keeping the user's own.
func stripSessionStatusEntries(cur any) []any {
	arr, _ := cur.([]any)
	var out []any
	for _, e := range arr {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "session-status") {
			continue
		}
		out = append(out, e)
	}
	return out
}
