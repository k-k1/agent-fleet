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

// runSessionStatusHook is `workspace-agent session-status <state> [sid] [codex]`.
// Three callers key the status differently:
//   - claude:   `session-status <state>` — session_id comes from claude's hook JSON
//     on stdin and equals our deterministic sid (we launch with --session-id).
//   - opencode: `session-status <state> <sid>` — the bundled plugin passes OUR sid
//     directly (no stdin JSON).
//   - codex:    `session-status <state> <sid> codex` — codex generates its OWN
//     session id, so we bake our slot sid into the hook command (-c injection) to
//     key the status, AND read codex's session_id from its hook JSON on stdin to
//     record the slot→codex-id mapping used for `codex resume <id>`.
func runSessionStatusHook(args []string) {
	state := "idle"
	if len(args) > 0 {
		state = args[0]
	}
	sid := ""
	codexMarker := false
	if len(args) > 2 && args[2] == "codex" {
		sid, codexMarker = args[1], true
	} else if len(args) > 1 {
		sid = args[1] // opencode plugin path
	}
	// Read stdin when claude needs the sid from it, or when codex wants to capture
	// its own session id for resume (status itself is keyed by the baked-in slot sid).
	// For the AskUserQuestion PreToolUse hook (state=question) the same stdin carries
	// tool_input.questions — capture it so the Console can show/answer the PENDING
	// question (the tool_use isn't written to the transcript until it's answered).
	var questions json.RawMessage
	var plan, message, ntype, toolDetail string
	if sid == "" || codexMarker {
		var in struct {
			SessionID        string `json:"session_id"`
			Message          string `json:"message"`           // Notification
			NotificationType string `json:"notification_type"` // Notification
			ToolName         string `json:"tool_name"`         // PreToolUse
			ToolInput        struct {
				Questions    json.RawMessage `json:"questions"` // AskUserQuestion
				Plan         string          `json:"plan"`      // ExitPlanMode
				FilePath     string          `json:"file_path"` // Write/Edit
				NotebookPath string          `json:"notebook_path"`
				Path         string          `json:"path"`
				Command      string          `json:"command"` // Bash
			} `json:"tool_input"`
		}
		_ = json.NewDecoder(os.Stdin).Decode(&in)
		if codexMarker {
			if in.SessionID != "" {
				codexSids.write(sid, in.SessionID)
			}
		} else {
			sid = in.SessionID // claude
			questions = in.ToolInput.Questions
			plan = in.ToolInput.Plan
			message = in.Message
			ntype = in.NotificationType
			toolDetail = permToolDetail(in.ToolName, in.ToolInput.FilePath, in.ToolInput.NotebookPath, in.ToolInput.Path, in.ToolInput.Command)
		}
	}
	if sid == "" {
		return
	}
	// permtool: a PreToolUse hook for edit/command tools that just records what is
	// about to run (for the permission block's detail) — it fires in EVERY mode, so
	// it must not change the session status.
	if state == "permtool" {
		writeLastTool(sid, toolDetail)
		return
	}
	// The Notification hook fires for several reasons (idle, permission, …); only
	// "permission_prompt" means the session is blocked awaiting a tool-permission
	// decision. Ignore the others so they don't clobber the real state.
	if state == "permission" && ntype != "permission_prompt" {
		return
	}
	_ = os.MkdirAll(sessionStatusDir(), 0o700)
	b, _ := json.Marshal(sessionStatus{State: state, TS: time.Now().Format(time.RFC3339)})
	_ = os.WriteFile(sessionStatusPath(sid), b, 0o600)
	// Persist/clear the pending AskUserQuestion / ExitPlanMode / permission payloads
	// alongside the status (each cleared whenever the session isn't in that state) —
	// except that a permission prompt must NOT clear a pending question/plan. When
	// AskUserQuestion (or ExitPlanMode) needs approval, its permission_prompt fires
	// between that tool's PreToolUse (which captured the question) and its PostToolUse,
	// overwriting state to "permission" with an empty questions payload. Clearing here
	// would destroy the captured question, so the Console loses the options. The
	// question/plan is instead cleared by its own lifecycle (PostToolUse→working, idle).
	if state == "question" && len(questions) > 0 {
		writePendingQuestion(sid, questions)
	} else if state != "permission" {
		removePendingQuestion(sid)
	}
	if state == "plan" && plan != "" {
		writePendingPlan(sid, plan)
	} else if state != "permission" {
		removePendingPlan(sid)
	}
	if state == "permission" {
		// Prefer the specific tool detail captured just before the prompt.
		if detail, ok := readLastTool(sid); ok && detail != "" {
			message = detail
		} else if message == "" {
			message = "Claude needs your permission"
		}
		removeLastTool(sid)
		writePendingPermission(sid, message)
	} else {
		removePendingPermission(sid)
	}
}

// permToolDetail renders "Tool · target" for the permission block (target = the file
// or the first line of the command); just the tool name when no recognizable arg.
func permToolDetail(name, file, notebook, path, command string) string {
	if name == "" {
		return ""
	}
	arg := firstNonEmpty(file, notebook, path, command)
	arg = strings.TrimSpace(arg)
	if i := strings.IndexByte(arg, '\n'); i >= 0 {
		arg = arg[:i]
	}
	if r := []rune(arg); len(r) > 100 {
		arg = string(r[:100]) + "…"
	}
	if arg == "" {
		return name
	}
	return name + " · " + arg
}

// last-tool: the tool about to run, recorded by the permtool PreToolUse hook and read
// when a permission prompt fires, to give the permission block a concrete subject.
func lastToolPath(sid string) string {
	return filepath.Join(pendingPermDir(), sid+".tool")
}

func writeLastTool(sid, detail string) {
	if detail == "" {
		return
	}
	if err := os.MkdirAll(pendingPermDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(lastToolPath(sid), []byte(detail), 0o600)
}

func readLastTool(sid string) (string, bool) {
	b, err := os.ReadFile(lastToolPath(sid))
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

func removeLastTool(sid string) { _ = os.Remove(lastToolPath(sid)) }

// A pending AskUserQuestion (the tool_input.questions array), kept only while the
// session is in the question state so the Console can render and answer it.
func pendingQuestionDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "pending-question")
}

func pendingQuestionPath(sid string) string {
	return filepath.Join(pendingQuestionDir(), sid+".json")
}

func writePendingQuestion(sid string, questions json.RawMessage) {
	if err := os.MkdirAll(pendingQuestionDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(pendingQuestionPath(sid), questions, 0o600)
}

func readPendingQuestion(sid string) (json.RawMessage, bool) {
	b, err := os.ReadFile(pendingQuestionPath(sid))
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

func removePendingQuestion(sid string) { _ = os.Remove(pendingQuestionPath(sid)) }

// A pending ExitPlanMode plan (the tool_input.plan markdown), kept only while the
// session waits for plan approval so the Console can show it / open it in a pane.
func pendingPlanDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "pending-plan")
}

func pendingPlanPath(sid string) string {
	return filepath.Join(pendingPlanDir(), sid+".md")
}

func writePendingPlan(sid, plan string) {
	if err := os.MkdirAll(pendingPlanDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(pendingPlanPath(sid), []byte(plan), 0o600)
}

func readPendingPlan(sid string) (string, bool) {
	b, err := os.ReadFile(pendingPlanPath(sid))
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

func removePendingPlan(sid string) { _ = os.Remove(pendingPlanPath(sid)) }

// A pending tool-permission prompt (the Notification message), kept while the session
// is blocked awaiting an allow/deny decision so the Console can approve it inline.
func pendingPermDir() string {
	return filepath.Join(homeDir(), ".config", "agent-fleet", "pending-perm")
}

func pendingPermPath(sid string) string { return filepath.Join(pendingPermDir(), sid+".txt") }

func writePendingPermission(sid, message string) {
	if err := os.MkdirAll(pendingPermDir(), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(pendingPermPath(sid), []byte(message), 0o600)
}

func readPendingPermission(sid string) (string, bool) {
	b, err := os.ReadFile(pendingPermPath(sid))
	if err != nil || len(b) == 0 {
		return "", false
	}
	return string(b), true
}

func removePendingPermission(sid string) { _ = os.Remove(pendingPermPath(sid)) }

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

func removeSessionStatus(sid string) {
	_ = os.Remove(sessionStatusPath(sid))
	removePendingQuestion(sid)
	removePendingPlan(sid)
	removePendingPermission(sid)
}

// agentExe is the absolute path to this binary, used to build hook commands that
// resolve in an agent's hook context regardless of PATH.
func agentExe() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "/usr/local/bin/workspace-agent"
	}
	return exe
}

// statusHookCmd is the absolute command claude runs for an event (absolute so it
// resolves in claude's hook context regardless of PATH).
func statusHookCmd(state string) string {
	return agentExe() + " session-status " + state
}

// permToolMatcher is the PreToolUse regex for edit/command tools whose permission
// prompts we surface (with the file/command) in the Console.
const permToolMatcher = "Write|Edit|MultiEdit|NotebookEdit|Bash"

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
	// PostToolUse: both resume to working once answered/approved. Re-set when the
	// ExitPlanMode matcher is missing (older settings only had AskUserQuestion).
	if b, _ := json.Marshal(hooks["PostToolUse"]); !strings.Contains(string(b), "ExitPlanMode") {
		hooks["PostToolUse"] = []any{
			map[string]any{"matcher": "AskUserQuestion", "hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("working")}}},
			map[string]any{"matcher": "ExitPlanMode", "hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("working")}}},
		}
		changed = true
	}
	// Notification → permission: fires when claude is blocked on a tool-permission
	// prompt (notification_type=permission_prompt). The handler ignores other
	// notification types, so this matcher-less hook is safe to always set.
	if b, _ := json.Marshal(hooks["Notification"]); !strings.Contains(string(b), "session-status") {
		hooks["Notification"] = []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": statusHookCmd("permission")}},
		}}
		changed = true
	}

	if changed {
		m["hooks"] = hooks
		_ = writeClaudeSettings(m)
	}
}
