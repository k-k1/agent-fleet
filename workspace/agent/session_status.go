package main

import (
	"encoding/json"
	"log"
	"os"
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

type sessionStatus struct {
	State string `json:"state"` // "working" | "idle"
	TS    string `json:"ts"`    // RFC3339
}

// Per-sid file stores (fstore.go). pending-question holds the raw tool_input
// payload; last-tool shares pending-perm's dir under a different extension.
var (
	statusFiles      = jsonStore[sessionStatus]("session-status", ".json")
	pendingQuestions = rawStore("pending-question", ".json")
	pendingPlans     = stringStore("pending-plan", ".md")
	pendingPerms     = stringStore("pending-perm", ".txt")
	lastTools        = stringStore("pending-perm", ".tool")
	pendingTexts     = stringStore("pending-text", ".txt")
)

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
	state, sid, codexMarker := parseStatusHookArgs(args)
	// Read stdin when claude needs the sid from it, or when codex wants to capture its
	// own session id for resume (status itself is keyed by the baked-in slot sid).
	var h hookInput
	if sid == "" || codexMarker {
		in := decodeHookStdin()
		if codexMarker {
			// codex: status stays keyed by the baked-in slot sid; only capture codex's
			// own session id for `codex resume`. The question/plan/permission payloads
			// are claude-only, so they are intentionally NOT carried over here.
			if in.sessionID != "" {
				codexSids.write(sid, in.sessionID)
			}
		} else {
			h = in             // claude: sid + pending payloads come from stdin
			sid = in.sessionID // claude's session_id == our deterministic sid
		}
	}
	if sid == "" {
		return
	}
	// message: the MessageDisplay hook fires as the assistant's text streams (before
	// the turn's tool_use — verified: the prose reaches the pending card). We accumulate
	// the chunks so a pending AskUserQuestion can show the prose that preceded it, which
	// isn't in the transcript until the question is answered. Never touches the status.
	if state == "message" {
		appendPendingText(sid, h.delta)
		return
	}
	// permtool: a PreToolUse hook for edit/command tools that just records what is
	// about to run (for the permission block's detail) — it fires in EVERY mode, so
	// it must not change the session status.
	if state == "permtool" {
		writeLastTool(sid, h.toolDetail)
		return
	}
	// The Notification hook fires for several reasons (idle, permission, …); only
	// "permission_prompt" means the session is blocked awaiting a tool-permission
	// decision. Ignore the others so they don't clobber the real state.
	if state == "permission" && h.ntype != "permission_prompt" {
		return
	}
	persistSessionStatus(sid, state)
	applyPendingPayloads(sid, state, h)
}

// parseStatusHookArgs decodes the `session-status <state> [sid] [codex]` positional
// args into the state, the (possibly empty) sid, and whether the codex marker is set.
func parseStatusHookArgs(args []string) (state, sid string, codexMarker bool) {
	state = "idle"
	if len(args) > 0 {
		state = args[0]
	}
	if len(args) > 2 && args[2] == "codex" {
		return state, args[1], true // codex: slot sid baked into the hook command
	}
	if len(args) > 1 {
		return state, args[1], false // opencode plugin path
	}
	return state, "", false // claude: sid comes from stdin
}

// hookInput is the subset of a claude/codex hook's stdin JSON we consume. For the
// AskUserQuestion PreToolUse hook (state=question) the stdin carries the pending
// tool_input.questions — captured so the Console can show/answer the question before
// it lands in the transcript (the tool_use is written only after it's answered).
type hookInput struct {
	sessionID  string
	questions  json.RawMessage
	plan       string
	message    string
	ntype      string
	toolDetail string
	delta      string // MessageDisplay: a streaming chunk of the assistant's text
}

func decodeHookStdin() hookInput {
	var in struct {
		SessionID        string `json:"session_id"`
		Message          string `json:"message"`           // Notification
		NotificationType string `json:"notification_type"` // Notification
		Delta            string `json:"delta"`             // MessageDisplay (streaming text chunk)
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
	return hookInput{
		sessionID:  in.SessionID,
		questions:  in.ToolInput.Questions,
		plan:       in.ToolInput.Plan,
		message:    in.Message,
		ntype:      in.NotificationType,
		delta:      in.Delta,
		toolDetail: permToolDetail(in.ToolName, in.ToolInput.FilePath, in.ToolInput.NotebookPath, in.ToolInput.Path, in.ToolInput.Command),
	}
}

// persistSessionStatus writes {state, ts} keyed by sid. Errors are logged (not
// swallowed): a failed write leaves the Console's 進行中/応答あり badge silently
// stale, so a log line is the only breadcrumb the write ever failed.
func persistSessionStatus(sid, state string) {
	s := sessionStatus{State: state, TS: time.Now().Format(time.RFC3339)}
	if err := statusFiles.write(sid, s); err != nil {
		log.Printf("session-status: write %s: %v", statusFiles.path(sid), err)
	}
}

// applyPendingPayloads persists/clears the pending AskUserQuestion / ExitPlanMode /
// permission payloads alongside the status (each cleared whenever the session isn't
// in that state) — except that a permission prompt must NOT clear a pending
// question/plan. When AskUserQuestion (or ExitPlanMode) needs approval, its
// permission_prompt fires between that tool's PreToolUse (which captured the question)
// and its PostToolUse, overwriting state to "permission" with an empty questions
// payload. Clearing here would destroy the captured question, so the Console loses the
// options. The question/plan is instead cleared by its own lifecycle
// (PostToolUse→working, idle).
func applyPendingPayloads(sid, state string, h hookInput) {
	if state == "question" && len(h.questions) > 0 {
		writePendingQuestion(sid, h.questions)
	} else if state != "permission" {
		removePendingQuestion(sid)
	}
	if state == "plan" && h.plan != "" {
		writePendingPlan(sid, h.plan)
	} else if state != "permission" {
		removePendingPlan(sid)
	}
	if state == "permission" {
		message := h.message
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
	// The MessageDisplay text buffer belongs to the in-flight turn: reset it at turn
	// start (UserPromptSubmit→working) and drop it once the turn moves past the question
	// (answered→working, Stop→idle). Kept during "question"/"permission" so a pending
	// question can still surface the prose that preceded it.
	if state == "working" || state == "idle" {
		removePendingText(sid)
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
func writeLastTool(sid, detail string) {
	if detail == "" {
		return
	}
	_ = lastTools.write(sid, detail)
}

func readLastTool(sid string) (string, bool) { return lastTools.read(sid) }
func removeLastTool(sid string)              { lastTools.remove(sid) }

// A pending AskUserQuestion (the tool_input.questions array), kept only while the
// session is in the question state so the Console can render and answer it.
func writePendingQuestion(sid string, questions json.RawMessage) {
	_ = pendingQuestions.write(sid, questions)
}

func readPendingQuestion(sid string) (json.RawMessage, bool) {
	b, ok := pendingQuestions.read(sid)
	return json.RawMessage(b), ok
}

func removePendingQuestion(sid string) { pendingQuestions.remove(sid) }

// pending-text: the assistant's streaming text for the in-flight turn, accumulated from
// the MessageDisplay hook. Kept only long enough for a pending AskUserQuestion to show
// the prose that preceded it (the turn's text lands in the transcript only after the
// question is answered). Reset each turn — see applyPendingPayloads.
//
// pendingTextCap bounds the buffer: the prose before a question is small, so a runaway
// stream shouldn't grow an unbounded file (it's reset every turn regardless).
const pendingTextCap = 16 << 10

func appendPendingText(sid, delta string) {
	if delta == "" {
		return
	}
	if err := os.MkdirAll(pendingTexts.dir(), 0o700); err != nil {
		return
	}
	path := pendingTexts.path(sid)
	if fi, err := os.Stat(path); err == nil && fi.Size() >= pendingTextCap {
		return // already at the cap; drop further chunks
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(delta)
}

func readPendingText(sid string) (string, bool) { return pendingTexts.read(sid) }
func removePendingText(sid string)              { pendingTexts.remove(sid) }

// A pending ExitPlanMode plan (the tool_input.plan markdown), kept only while the
// session waits for plan approval so the Console can show it / open it in a pane.
func writePendingPlan(sid, plan string)         { _ = pendingPlans.write(sid, plan) }
func readPendingPlan(sid string) (string, bool) { return pendingPlans.read(sid) }
func removePendingPlan(sid string)              { pendingPlans.remove(sid) }

// A pending tool-permission prompt (the Notification message), kept while the session
// is blocked awaiting an allow/deny decision so the Console can approve it inline.
func writePendingPermission(sid, message string)      { _ = pendingPerms.write(sid, message) }
func readPendingPermission(sid string) (string, bool) { return pendingPerms.read(sid) }
func removePendingPermission(sid string)              { pendingPerms.remove(sid) }

func readSessionStatus(sid string) (sessionStatus, bool) { return statusFiles.read(sid) }

func removeSessionStatus(sid string) {
	statusFiles.remove(sid)
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
//
//	UserPromptSubmit → working   (user sent a prompt)
//	Stop             → idle      (response done / awaiting user)
//	PreToolUse(AskUserQuestion)  → question (claude is asking the user; QA来た)
//	PostToolUse(AskUserQuestion) → working  (question answered, continuing)
func ensureStatusHooks() {
	m := readClaudeSettings()
	hooks := hooksMap(m)
	changed := false

	// Simple (matcher-less) events. MessageDisplay fires as the assistant's text streams
	// (before the turn's tool_use); we buffer it (state "message" never changes status)
	// so a pending AskUserQuestion can surface the prose that preceded it.
	for event, state := range map[string]string{"UserPromptSubmit": "working", "Stop": "idle", "MessageDisplay": "message"} {
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
