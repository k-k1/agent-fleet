package sessionx

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Session liveness via claude hooks. claude fires UserPromptSubmit when work
// starts and Stop when it finishes a response; we wire those to
// `workspace-agent session-status <state>`, which records {state, ts} keyed by our
// deterministic slot sid (claude's hook session_id, normalized — see below).
// wireSession surfaces the state so
// the Console can badge working / answer-ready and notify on arrival. Robust and cheap:
// driven by claude's own events, no TUI parsing or transcript polling. With
// --dangerously-skip-permissions there is no tool-approval QA state, so the two
// meaningful states are working and idle(=response ready / awaiting input).
//
// The store for the state and the pending payloads is internal/status (docs/log/23 remaining
// item 1, Wave A), and the hook wiring into claude's settings (EnsureStatusHooks) is
// internal/agents/claude (same, Wave F); this file holds only the entry point of the
// session-status subcommand.

// EnsureClaudeSettingsWiring re-asserts what claude's settings.json must carry for us
// — the status hooks and the statusLine that captures rate_limits — right before a
// claude session launches. Both are also wired at agent startup, but settings.json is
// shared and can go stale while the agent keeps running: another build of the agent (a
// dev build under /tmp, an e2e or smoke copy) writes ITS path in, and once that file is
// deleted claude's hooks and statusLine silently stop running — session state freezes
// and the usage capture dies (measured: 6 hours of a fabricated 0% in the usage chip,
// unrecoverable until a workspace restart). Two small JSON reads per launch, writing
// only when something actually changed, repairs it without one.
func EnsureClaudeSettingsWiring(kind string) {
	if kind != session.KindClaude {
		return
	}
	claude.EnsureStatusHooks()
	claude.EnsureStatusLine()
}

// RunSessionStatusHook is `workspace-agent session-status <state> [sid] [codex]`.
// Three callers key the status differently:
//   - claude:   `session-status <state>` — session_id comes from claude's hook JSON
//     on stdin. It is our deterministic sid while claude honours the --session-id we
//     launched with, and an id of claude's own once it has restarted itself;
//     claude.NormalizeHookSID maps it back onto the slot (claude/sid.go).
//   - opencode: `session-status <state> <sid>` — the bundled plugin passes OUR sid
//     directly (no stdin JSON).
//   - codex:    `session-status <state> <sid> codex` — codex generates its OWN
//     session id, so we bake our slot sid into the hook command (-c injection) to
//     key the status, AND read codex's session_id from its hook JSON on stdin to
//     record the slot→codex-id mapping used for `codex resume <id>`.
func RunSessionStatusHook(args []string) {
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
				codex.RememberSid(sid, in.sessionID)
			}
		} else {
			h = in // claude: sid + pending payloads come from stdin
			// claude's session_id is normally our deterministic sid (we launch with
			// --session-id), but claude drops that flag whenever it restarts itself and
			// then announces an id of its own (internal/agents/claude/sid.go). Keying the
			// status by that id makes the session vanish from the Console mid-run — the
			// state, the pending question/plan and the report matching below are all read
			// by slot sid. Pull it back onto the slot (and remember the drift).
			sid = claude.NormalizeHookSID(in.sessionID)
		}
	}
	if sid == "" {
		return
	}
	// boot: the SessionStart hook. Reset a fresh or resumed session to idle so a stale
	// "working" status file (killed mid-turn, then resumed — no Stop ever fired) doesn't
	// badge working forever. Two guards: (1) skip source=="compact", which resumes the SAME
	// in-flight turn after an auto-compaction — idling it there would false-idle live work;
	// (2) never RecordSessionNotification, since a stale working→idle reset here is not a
	// real answer-ready and must not fire the answer-ready notification.
	if state == "boot" {
		if h.source == "compact" {
			return
		}
		// Carry it over before it is erased (docs/log/75 §75.6.3, trigger 3). This is the
		// moment a session that was folded with a modal open resumes, and the
		// applyPendingPayloads just below erases pending-question/plan/perm — the last
		// catch for paths that went through neither the listing nor halt (a resume right
		// after SIGKILL, say). An already-promoted entry is not overwritten.
		status.PromoteCarried(sid, "stopped")
		status.Persist(sid, "idle")
		applyPendingPayloads(sid, "idle", h)
		return
	}
	// message: the MessageDisplay hook fires as the assistant's text streams (before
	// the turn's tool_use — verified: the prose reaches the pending card). We accumulate
	// the chunks so a pending AskUserQuestion can show the prose that preceded it, which
	// isn't in the transcript until the question is answered. Never touches the status.
	if state == "message" {
		status.AppendPendingText(sid, h.delta)
		return
	}
	// permtool: a PreToolUse hook for edit/command tools that just records what is
	// about to run (for the permission block's detail) — it fires in EVERY mode, so
	// it must not change the session status.
	if state == "permtool" {
		status.WriteLastTool(sid, h.toolDetail)
		return
	}
	// The Notification hook fires for several reasons (idle, permission, …); only
	// "permission_prompt" means the session is blocked awaiting a tool-permission
	// decision. Ignore the others so they don't clobber the real state.
	if state == "permission" && h.ntype != "permission_prompt" {
		return
	}
	// A hook carrying agent_id fired INSIDE a subagent, not on the session's own thread
	// (measured on 2.1.252: a subagent's Bash PostToolUse carries agent_id/agent_type and
	// arrives with the SAME session_id as the parent, while the main thread has neither).
	// The working heartbeat on PostToolUse(*) does not tell them apart, so a session that
	// merely has a subagent running in the background claims its own turn is in flight —
	// measured 2026-09-01: sf2ykxk sat at the ready prompt while working was re-stamped
	// every 7-12 seconds, showing as in-progress with a stop button, and the background-work
	// detection (claude.BackgroundWork), which only runs on idle, was never evaluated at all.
	//
	// Re-stamping an EXISTING working is let through: during a foreground subagent's long
	// turn that tool is the only heartbeat, and dropping it brings false-idle back. What is
	// blocked is only a revival from idle — a session working in the background has to stay
	// idle to reach the badge that names what is running (awaiting input · subagent running).
	// Recovering a turn whose working was lost entirely is the job of the pane-reading
	// reverse-heal (agent.go).
	if h.agentID != "" && state == "working" {
		if cur, _ := status.Read(sid); cur.State != "working" {
			return
		}
	}
	previous, _ := status.Read(sid)
	// Capture the turn's streamed text BEFORE applyPendingPayloads clears it on idle:
	// it becomes the full-text bridge body (docs/log/37). The operator report itself
	// carries no excerpt (docs/log/30: fact-only, uniform with managed).
	turnText, _ := status.ReadPendingText(sid)
	if state == "idle" {
		// The Stop hook's idle is a claim that the turn ended. docs/log/51's reconciler uses
		// that one bit as the evidence of completion (to tell it apart from boot's idle
		// reset).
		status.PersistTurnEnd(sid, state)
	} else {
		status.Persist(sid, state)
	}
	applyPendingPayloads(sid, state, h)
	notifyState, notifyText := turnEndLabel(sid, state, turnText)
	RecordSessionNotification(sid, previous.State, notifyState, notifyText)
}

// claudeAbortInfo is the transcript-tail verdict (docs/log/47), replaceable in tests.
var claudeAbortInfo = claude.AbortInfo

// turnEndLabel refines the Stop hook's "idle" into what actually ENDED the turn.
//
// Stop's idle says no more than "the turn ended". HOW it ended is in the transcript tail, and
// docs/log/47 already has the implementation that reads it (claude.HealIdle) — but that was
// called only from the pane-driven self-heal path, i.e. when Stop never fired. The premise was
// "claude does not fire Stop when an API error kills a turn", which held for a dropped
// connection.
//
// On a usage-limit 429, though, claude folds the turn up AS COMPLETE (writes turn_duration,
// prints "Brewed for …", fires Stop). Measured 2026-08-05, s6no6jv (a per-model limit). The
// marker reaches idle first, the self-heal's `state != "idle"` guard lets HealIdle sail past,
// and a turn killed by the limit is notified as a completed response. The very hole
// agents.StateFailed was created for (an exhausted balance being indistinguishable from a
// completion) was still open on the TUI path alone.
//
// Other kinds pass straight through: the evidence is specific to claude's jsonl format, and
// AbortInfo looks for sid.jsonl under claude's ConfigDir, so an opencode / codex sid finds
// nothing and it returns ok=false.
func turnEndLabel(sid, state, turnText string) (string, string) {
	if state != "idle" {
		return state, turnText
	}
	a, ok := claudeAbortInfo(sid)
	if !ok {
		return state, turnText
	}
	// Putting the reason in the excerpt is the same contract as managed's MarkTurnEndErr: the
	// report (reason) and the full-text bridge (body) read the failure's reason from here.
	// Pass the reason, not the turn's body.
	if a.Retryable {
		return agents.StateAborted, a.Msg
	}
	return agents.StateFailed, a.Msg
}

func RecordSessionNotification(sid, previous, state, turnText string) {
	kind := ""
	reason := ""
	switch {
	// A turn that ENDED: the marker said "working", or it is GONE. The pane-based idle
	// heal (WireLive / liveStateOf) does status.Remove(sid) whenever the TUI *looks* like
	// it is back at the ready prompt, which a footer-string drift makes fire mid-turn. Its
	// reverse-heal only restores "working" if a poll happens to catch IsBusy, so the
	// marker can simply stay gone — and keying answer-ready on previous=="working" alone
	// then dropped the real Stop on the floor: no answer-ready notification and no operator
	// report (docs/log/30), while the session still read idle (LiveState defaults to idle with
	// no file). A completion must not hinge on a tmux-string heuristic never misfiring, so
	// an absent marker counts as a turn that ended. Interim states (question/plan/
	// permission) stay excluded — they are not completions and must not consume the arm.
	case state == "idle" && (previous == "working" || previous == ""):
		kind = chatx.ReportKindAnswerReady
	// A managed turn that ended in a provider-side error (agents.StateFailed). As an
	// EVENT it is the same terminal completion as answer-ready — the session is back to
	// awaiting input and the instruction's one report must fire and consume the arm — but
	// the report has to say it errored: a silent "the response completed" is exactly how an
	// exhausted opencode Zen balance passed for a finished turn. turnText holds the driver's
	// one-line reason, so it also rides the full-text bridge body below.
	case state == agents.StateFailed && (previous == "working" || previous == ""):
		kind, reason = chatx.ReportKindAnswerReady, chatx.ReportReasonTurnFailed
	// A turn CUT OFF before it answered, by something that clears on its own (a dropped
	// connection, a temporary rate limit). Same terminal event as above — the session is
	// awaiting input and the instruction's one report must fire — but here re-running the turn
	// is the right next move, so the report says so instead of "do not resend until the cause
	// is fixed" (docs/log/47).
	case state == agents.StateAborted && (previous == "working" || previous == ""):
		kind, reason = chatx.ReportKindAnswerReady, chatx.ReportReasonTurnAborted
	case state == "question" && previous != "question":
		kind = "question"
	case state == "plan" && previous != "plan":
		kind = "plan-approval"
	case state == "permission" && previous != "permission":
		// AskUserQuestion/ExitPlanMode may emit an intermediate permission hook.
		if _, ok := status.ReadPendingQuestion(sid); ok {
			return
		}
		if _, ok := status.ReadPendingPlan(sid); ok {
			return
		}
		kind = "permission-request"
	}
	if kind == "" {
		return
	}
	for _, m := range session.ListMetas() {
		if session.UUID(m.Dir, m.Name) != sid {
			continue
		}
		holdReport := false
		if m.DriverKind() == session.DriverManaged && (m.Kind == session.KindCodex || m.Kind == session.KindOpencode) {
			switch state {
			case agents.StateAborted:
				sig, ok := managedAbortSignals.Read(m.Name)
				if !ok {
					sig.At = time.Now().Format(time.RFC3339)
				}
				sig.Msg = turnText
				_ = managedAbortSignals.Write(m.Name, sig)
				a, _ := abortInfoFor(m)
				holdReport = AbortResumeHolds(m.Name, a, time.Now())
			case "idle", agents.StateFailed:
				managedAbortSignals.Remove(m.Name)
				abortResumeStates.Remove(m.Name)
				chatx.ResetAutoResume(m.Name)
			}
		}
		ev := notice.New(kind, m.Name, m.Kind, session.Display(m))
		// Full-text bridge (docs/log/37, the future direction): carry the turn's final
		// prose on the answer-ready event so a full-text-mode provider can post it. Only
		// answer-ready — interim attention events (question/plan/permission) have
		// no completed turn body. Capped like the operator report excerpt; the
		// provider scrubs secrets and chunks before any wire.
		if kind == chatx.ReportKindAnswerReady {
			// Head-first and generously capped (docs/log/37 Fix 3): the full-text bridge
			// stands in for the Console, so the WHOLE answer rides along (chunkMessage
			// splits it), not a 2000-rune tail like the operator report excerpt.
			if body := chatx.HeadRunes(turnText, chatx.BridgeBodyCap); body != "" {
				ev.Payload["body"] = body
			}
		}
		// P2b (docs/log/37): carry the pending AskUserQuestion payload on the question
		// event so an interact-capable provider can render option buttons.
		if kind == "question" {
			if q, ok := status.ReadPendingQuestion(sid); ok && len(q) > 0 {
				ev.Payload["questions"] = q
			}
		}
		_ = notice.Put(ev)
		// One-shot session report to the operator conversation that armed this
		// session (docs/log/30). Only TERMINAL events CONSUME the arm: an instruction's
		// one report must be its COMPLETION, so an interim attention event must not
		// disarm — otherwise the eventual completion never reaches the operator
		// (that was the observed bug). A pending QUESTION / PLAN is additionally
		// reported WITHOUT consuming the arm (handleChatReport keeps it armed for
		// interim kinds), so the operator can relay/answer (answer_session_question)
		// or drive the plan review loop (respond_session_plan); permission stays
		// notification-center only.
		// docs/log/51 Phase 1: on a terminal event this kick is not DELIVERY but a
		// wake-up hint. Whether the arm may be consumed is decided by the reconciler
		// reading the state by level (this function only says what happened, never
		// whether it is time to report).
		if !holdReport && (kind == chatx.ReportKindAnswerReady || kind == "question" || kind == "plan-approval") && chatx.SessionReportPending(m.Name) {
			chatx.KickSessionReport(m.Name, kind, reason)
		}
		return
	}
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
	source     string // SessionStart: startup | resume | clear | compact
	toolName   string // PreToolUse/PostToolUse: which tool fired this hook ("" = not a tool event)
	// agentID is claude's agent_id: set only when the hook fired from within a subagent
	// (its own docs: "Use this field (not agent_type) to distinguish subagent calls from
	// main-thread calls"), and empty on the session's own thread — including --agent
	// sessions, where agent_type IS set. So agent_type must not be used in its place.
	agentID string
}

func decodeHookStdin() hookInput {
	var in struct {
		SessionID        string `json:"session_id"`
		Message          string `json:"message"`           // Notification
		NotificationType string `json:"notification_type"` // Notification
		Delta            string `json:"delta"`             // MessageDisplay (streaming text chunk)
		ToolName         string `json:"tool_name"`         // PreToolUse
		AgentID          string `json:"agent_id"`          // set only inside a subagent
		Source           string `json:"source"`            // SessionStart (startup/resume/clear/compact)
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
		source:     in.Source,
		toolName:   in.ToolName,
		agentID:    in.AgentID,
		toolDetail: permToolDetail(in.ToolName, in.ToolInput.FilePath, in.ToolInput.NotebookPath, in.ToolInput.Path, in.ToolInput.Command),
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
// (PostToolUse→working, idle) — see clearsInteraction for WHICH working counts.
func applyPendingPayloads(sid, state string, h hookInput) {
	if state == "question" && len(h.questions) > 0 {
		status.WritePendingQuestion(sid, h.questions)
	} else if state != "permission" && clearsInteraction(h.toolName, "AskUserQuestion") {
		status.RemovePendingQuestion(sid)
	}
	if state == "plan" && h.plan != "" {
		status.WritePendingPlan(sid, h.plan)
	} else if state != "permission" && clearsInteraction(h.toolName, "ExitPlanMode") {
		status.RemovePendingPlan(sid)
	}
	if state == "permission" {
		message := h.message
		// Prefer the specific tool detail captured just before the prompt.
		if detail, ok := status.ReadLastTool(sid); ok && detail != "" {
			message = detail
		} else if message == "" {
			message = "Claude needs your permission"
		}
		status.RemoveLastTool(sid)
		status.WritePendingPermission(sid, message)
	} else {
		status.RemovePendingPermission(sid)
	}
	// The MessageDisplay text buffer belongs to the in-flight turn: reset it at turn
	// start (UserPromptSubmit→working) and drop it once the turn moves past the question
	// (answered→working, Stop→idle). Kept during "question"/"permission" so a pending
	// question can still surface the prose that preceded it.
	if state == "working" || state == "idle" {
		status.RemovePendingText(sid)
	}
}

// clearsInteraction reports whether the hook event that carried toolName may clear the
// captured payload of the interaction tool `own` (AskUserQuestion / ExitPlanMode).
//
// PostToolUse(*) is the heartbeat that re-stamps working for every completed tool
// (claude/hooks.go), and since the modal was folded by that same tool's PostToolUse, clearing
// it is fine — as long as the turn runs on a single track. A background subagent or Workflow
// breaks that premise: measured (2026-08-24, claude 2.1.241), a subagent's tools fire
// PreToolUse/PostToolUse with the SAME session_id as the parent.
//
//	{"ev":"pre", tool:"Agent"} → {"ev":"pre", tool:"Bash"} → {"ev":"post", tool:"Bash"} → …
//
// So even with the question modal still up, working arrived every time a background tool
// finished and erased the pending question payload. The payload is written exactly once, at
// AskUserQuestion's PreToolUse (the only call to WritePendingQuestion), so once erased it never
// comes back and the Console is left with nothing but the INERT transcript-derived card — "I
// cannot answer it" (reported by a user).
//
// Clearing is allowed only from that modal's own PostToolUse, or from a state transition that
// carries no tool (UserPromptSubmit's working / Stop's idle / SessionStart's boot).
func clearsInteraction(toolName, own string) bool {
	return toolName == "" || toolName == own
}

// effectiveModal is an alias for status.EffectiveModal (priority question > plan >
// permission). The decision itself belongs to the status package: the display side (claude's
// WireLive / DriveState) has to go through the same resolution and cannot import package main.
func effectiveModal(sid, state string) string { return status.EffectiveModal(sid, state) }

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
