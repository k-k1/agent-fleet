package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/notice"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// Session liveness via claude hooks. claude fires UserPromptSubmit when work
// starts and Stop when it finishes a response; we wire those to
// `workspace-agent session-status <state>`, which records {state, ts} keyed by our
// deterministic slot sid (claude's hook session_id, normalized — see below).
// wireSession surfaces the state so
// the Console can badge 進行中 / 応答あり and notify on arrival. Robust and cheap:
// driven by claude's own events, no TUI parsing or transcript polling. With
// --dangerously-skip-permissions there is no tool-approval QA state, so the two
// meaningful states are working and idle(=response ready / awaiting input).
//
// 状態と pending ペイロードのストア本体は internal/status（docs/23 残① Wave A）、
// claude settings への hook 配線（EnsureStatusHooks）は internal/agents/claude
// （同 Wave F）; このファイルは session-status サブコマンドの入口だけを持つ。

// runSessionStatusHook is `workspace-agent session-status <state> [sid] [codex]`.
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
	// badge 進行中 forever. Two guards: (1) skip source=="compact", which resumes the SAME
	// in-flight turn after an auto-compaction — idling it there would false-idle live work;
	// (2) never recordSessionNotification, since a stale working→idle reset here is not a
	// real answer-ready and must not fire the 応答あり notification.
	if state == "boot" {
		if h.source == "compact" {
			return
		}
		// 消す前に持ち越す（docs/75 §75.6.3 の契機 3）。ここは「モーダルを出したまま
		// 畳まれたセッションが再開した」瞬間で、直後の applyPendingPayloads が
		// pending-question/plan/perm を消してしまう — 一覧も halt も通らなかった経路
		// （SIGKILL 直後の再開など）の最後の受け皿。既に昇格済みなら上書きしない。
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
	previous, _ := status.Read(sid)
	// Capture the turn's streamed text BEFORE applyPendingPayloads clears it on idle:
	// it becomes the full-text bridge body (docs/37). The operator report itself
	// carries no excerpt (docs/30: fact-only, uniform with managed).
	turnText, _ := status.ReadPendingText(sid)
	if state == "idle" {
		// Stop フックの idle は「ターンが終わった」という主張。docs/51 のリコンサイラは
		// この 1bit を完了の証拠に使う（boot の idle リセットと区別するため）。
		status.PersistTurnEnd(sid, state)
	} else {
		status.Persist(sid, state)
	}
	applyPendingPayloads(sid, state, h)
	notifyState, notifyText := turnEndLabel(sid, state, turnText)
	recordSessionNotification(sid, previous.State, notifyState, notifyText)
}

// claudeAbortInfo is the transcript-tail verdict (docs/47), replaceable in tests.
var claudeAbortInfo = claude.AbortInfo

// turnEndLabel refines the Stop hook's "idle" into what actually ENDED the turn.
//
// Stop の idle は「ターンが終わった」としか言わない。**どう**終わったかは転写の末尾に
// あり、docs/47 はそれを読む実装（claude.HealIdle）を既に持っている — が、呼ばれるのは
// ペイン由来の自己修復経路、つまり「Stop が鳴らなかったとき」だけだった。前提は「API
// エラーでターンが落ちると claude は Stop を鳴らさない」で、接続断ではそのとおりだった。
//
// ところが利用上限の 429 では claude はターンを**完了として畳む**（turn_duration を書き、
// Brewed for … を出し、Stop も鳴らす）。実測 2026-08-05 s6no6jv（モデル別上限）。すると
// マーカーは先に idle になり、自己修復の `state != "idle"` ガードで HealIdle は素通り、
// 上限で落ちたターンが「応答が完了」として通知される。agents.StateFailed を作った理由
// （残高切れが完了と見分けられなかった）と同じ穴が、TUI 経路にだけ残っていた。
//
// 他 kind では素通しになる: 判別材料は claude の jsonl 形式に固有で、AbortInfo は
// claude の ConfigDir 配下から sid.jsonl を探すので、opencode / codex の sid では何も
// 見つからず ok=false を返す。
func turnEndLabel(sid, state, turnText string) (string, string) {
	if state != "idle" {
		return state, turnText
	}
	a, ok := claudeAbortInfo(sid)
	if !ok {
		return state, turnText
	}
	// 理由を excerpt に載せるのは managed の MarkTurnEndErr と同じ契約 — 報告（reason）と
	// 全文ブリッジ（body）はここから失敗の理由を読む。ターンの本文ではなく理由を渡す。
	if a.Retryable {
		return agents.StateAborted, a.Msg
	}
	return agents.StateFailed, a.Msg
}

func recordSessionNotification(sid, previous, state, turnText string) {
	kind := ""
	reason := ""
	switch {
	// A turn that ENDED: the marker said "working", or it is GONE. The pane-based idle
	// heal (WireLive / liveStateOf) does status.Remove(sid) whenever the TUI *looks* like
	// it is back at the ready prompt, which a footer-string drift makes fire mid-turn. Its
	// reverse-heal only restores "working" if a poll happens to catch IsBusy, so the
	// marker can simply stay gone — and keying answer-ready on previous=="working" alone
	// then dropped the real Stop on the floor: no 応答あり notification and no operator
	// report (docs/30), while the session still read idle (LiveState defaults to idle with
	// no file). A completion must not hinge on a tmux-string heuristic never misfiring, so
	// an absent marker counts as a turn that ended. Interim states (question/plan/
	// permission) stay excluded — they are not completions and must not consume the arm.
	case state == "idle" && (previous == "working" || previous == ""):
		kind = reportKindAnswerReady
	// A managed turn that ended in a provider-side error (agents.StateFailed). As an
	// EVENT it is the same terminal completion as answer-ready — the session is back at
	// 入力待ち and the instruction's one report must fire and consume the arm — but the
	// report has to say it errored: a silent 応答が完了 is exactly how an exhausted
	// opencode Zen balance passed for a finished turn. turnText holds the driver's
	// one-line reason, so it also rides the full-text bridge body below.
	case state == agents.StateFailed && (previous == "working" || previous == ""):
		kind, reason = reportKindAnswerReady, reportReasonTurnFailed
	// A turn CUT OFF before it answered, by something that clears on its own (接続断・
	// 一時的なレート制限). Same terminal event as above — the session is at 入力待ち and
	// the instruction's one report must fire — but here re-running the turn is the right
	// next move, so the report says so instead of "原因を直すまで再送するな" (docs/47).
	case state == agents.StateAborted && (previous == "working" || previous == ""):
		kind, reason = reportKindAnswerReady, reportReasonTurnAborted
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
				holdReport = abortResumeHolds(m.Name, a, time.Now())
			case "idle", agents.StateFailed:
				managedAbortSignals.Remove(m.Name)
				abortResumeStates.Remove(m.Name)
				resetAutoResume(m.Name)
			}
		}
		ev := notice.New(kind, m.Name, m.Kind, session.Display(m))
		// 全文ブリッジ (docs/37 将来の方向): carry the turn's final prose on the
		// answer-ready event so a full-text-mode provider can post it. Only
		// answer-ready — interim attention events (question/plan/permission) have
		// no completed turn body. Capped like the operator report excerpt; the
		// provider scrubs secrets and chunks before any wire.
		if kind == reportKindAnswerReady {
			// Head-first and generously capped (docs/37 Fix ③): the full-text bridge
			// stands in for the Console, so the WHOLE answer rides along (chunkMessage
			// splits it), not a 2000-rune tail like the operator report excerpt.
			if body := headRunes(turnText, bridgeBodyCap); body != "" {
				ev.Payload["body"] = body
			}
		}
		// P2b (docs/37): carry the pending AskUserQuestion payload on the question
		// event so an interact-capable provider can render option buttons.
		if kind == "question" {
			if q, ok := status.ReadPendingQuestion(sid); ok && len(q) > 0 {
				ev.Payload["questions"] = q
			}
		}
		_ = notice.Put(ev)
		// One-shot session report to the operator conversation that armed this
		// session (docs/30). Only TERMINAL events CONSUME the arm: an instruction's
		// one report must be its COMPLETION, so an interim attention event must not
		// disarm — otherwise the eventual completion never reaches the operator
		// (that was the observed bug). A pending QUESTION / PLAN is additionally
		// reported WITHOUT consuming the arm (handleChatReport keeps it armed for
		// interim kinds), so the operator can relay/answer (answer_session_question)
		// or drive the plan review loop (respond_session_plan); permission stays
		// notification-center only.
		// docs/51 Phase 1: この kick は終端イベントでは「配送」ではなく**起床ヒント**。
		// 消費してよいかの判定はリコンサイラが状態をレベルで見て決める（この関数は
		// 「何が起きたか」を伝えるだけで、「もう報告してよいか」は決めない）。
		if !holdReport && (kind == reportKindAnswerReady || kind == "question" || kind == "plan-approval") && sessionReportPending(m.Name) {
			kickSessionReport(m.Name, kind, reason)
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
}

func decodeHookStdin() hookInput {
	var in struct {
		SessionID        string `json:"session_id"`
		Message          string `json:"message"`           // Notification
		NotificationType string `json:"notification_type"` // Notification
		Delta            string `json:"delta"`             // MessageDisplay (streaming text chunk)
		ToolName         string `json:"tool_name"`         // PreToolUse
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
// (PostToolUse→working, idle).
func applyPendingPayloads(sid, state string, h hookInput) {
	if state == "question" && len(h.questions) > 0 {
		status.WritePendingQuestion(sid, h.questions)
	} else if state != "permission" {
		status.RemovePendingQuestion(sid)
	}
	if state == "plan" && h.plan != "" {
		status.WritePendingPlan(sid, h.plan)
	} else if state != "permission" {
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

// effectiveModal は status.EffectiveModal（question > plan > permission の優先順位）
// の別名。判定そのものは status パッケージが持つ — 表示側（claude の WireLive /
// driveState）も同じ解決を通す必要があり、そちらは package main を import できない。
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
