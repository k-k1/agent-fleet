// Package status はセッションの live 状態（working/idle/question/…）と pending
// ペイロード（質問・プラン・許可・ストリーミングテキスト・直近ツール）の per-sid
// ファイルストア。claude の hooks / opencode プラグイン / codex フック（配線は
// package main の session_status.go）が書き、sessions リストと /messages が読む。
// package main からの抽出（docs/23 残① Wave A）— ディスク上のレイアウト
// （~/.config/agent-fleet/…）と JSON タグはバイト同一を維持すること。
package status

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type SessionStatus struct {
	State string `json:"state"` // "working" | "idle"
	TS    string `json:"ts"`    // RFC3339
	// TurnEnd marks an idle that was written by a turn's ACTUAL end (Stop hook /
	// MarkTurnEnd completed|failed|aborted) — as opposed to an idle written because we
	// do NOT know what is going on: the SessionStart reset, or a managed driver that
	// lost its runtime handle mid-turn (TurnUnknown). 状態文字列だけでは「終わった」と
	// 「分からない」が同じ "idle" になり、レベル判定（docs/51 のリコンサイラ）は不明を
	// 完了と読んでしまう。証拠側を positive に倒す1bit — フラグの無い idle は不明として
	// 扱われ、報告は次の本物の終端まで待つ（取りこぼしは遅延、誤りは誤配送）。
	TurnEnd bool `json:"turnEnd,omitempty"`
}

// ExitInfo records WHY a session's agent process terminated, so the sessions list can
// tell a normal quit apart from a crash or an OOM kill. Written by the pane exit
// recorder (`workspace-agent record-exit`, wired by startSessionTmux) once the agent
// CLI exits; keyed by session NAME (not sid) since that's what the pane wrapper carries.
// It is a SEPARATE per-session file from Meta on purpose — record-exit and the API
// handlers both write session state, and Meta is a single JSON blob they'd clobber;
// a dedicated store sidesteps that race (same reasoning as SessionStatus above).
//
// At launch startSessionTmux writes a baseline row (Reason=="" , OOMBase=current
// container oom_kill) which doubles as clearing any prior death record — so a resumed
// session starts clean. record-exit then overwrites it with the interpreted Reason.
type ExitInfo struct {
	Reason  string `json:"reason,omitempty"`  // exited|crashed|killed|oom|stopped ; "" = running/baseline
	Code    int    `json:"code,omitempty"`    // raw pane wait status ($?); 128+N on a signal kill
	Signal  int    `json:"signal,omitempty"`  // N when Code>=128 (SIGKILL=9 → Code 137), else 0
	At      string `json:"at,omitempty"`      // RFC3339, when the exit was recorded
	OOMBase uint64 `json:"oomBase,omitempty"` // container oom_kill counter captured at session start
}

// Per-sid file stores (internal/fstore). pending-question holds the raw tool_input
// payload; last-tool shares pending-perm's dir under a different extension. exitFiles
// is keyed by session NAME (see ExitInfo), the others by sid.
var (
	statusFiles      = fstore.JSON[SessionStatus](paths.AgentConfigDir, "session-status", ".json")
	exitFiles        = fstore.JSON[ExitInfo](paths.AgentConfigDir, "session-exit", ".json")
	pendingQuestions = fstore.Raw(paths.AgentConfigDir, "pending-question", ".json")
	pendingPlans     = fstore.Strings(paths.AgentConfigDir, "pending-plan", ".md")
	pendingPerms     = fstore.Strings(paths.AgentConfigDir, "pending-perm", ".txt")
	lastTools        = fstore.Strings(paths.AgentConfigDir, "pending-perm", ".tool")
	pendingTexts     = fstore.Strings(paths.AgentConfigDir, "pending-text", ".txt")
	carriedFiles     = fstore.JSON[Carried](paths.AgentConfigDir, "carried-interaction", ".json")
)

// PersistExit / ReadExit / RemoveExit manage the per-session exit record (keyed by
// session name). PersistExit is called both at launch (baseline) and at exit (result).
func PersistExit(name string, e ExitInfo)   { _ = exitFiles.Write(name, e) }
func ReadExit(name string) (ExitInfo, bool) { return exitFiles.Read(name) }
func RemoveExit(name string)                { exitFiles.Remove(name) }

// cgroupDir is the container's own cgroup v2 root. Overridable for tests.
func cgroupDir() string {
	if v := os.Getenv("AF_CGROUP_DIR"); v != "" {
		return v
	}
	return "/sys/fs/cgroup"
}

// OOMKillCount reads the cumulative oom_kill counter from the container's own
// cgroup v2 memory.events. From inside the container /sys/fs/cgroup is
// cgroup-namespaced to this container, so this is our own count. Reports !ok when
// unreadable (a non cgroup-v2 host, a different layout, etc.) so callers degrade
// instead of guessing OOM.（record_exit.go の containerOOMKill を移設 — docs/27
// §10.2-2 で opencode supervisor も使うため package main から下ろした。）
func OOMKillCount() (uint64, bool) {
	b, err := os.ReadFile(cgroupDir() + "/memory.events")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "oom_kill" {
			v, err := strconv.ParseUint(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// ExitReasonFor interprets a wait status into a cause the Console can show
// （record_exit.go の exitReason を移設・共用化 — pane ラッパー（tui）と daemon
// supervisor（managed、docs/27 §10.2-2）が同じ reason enum を書くため）:
//   - 0                       → exited  (normal quit)
//   - SIGKILL(9) + OOM        → oom     (memory cgroup / host OOM killer)
//   - SIGKILL(9) no OOM       → killed  (a SIGKILL from something other than an OOM)
//   - SIGHUP/INT/TERM(1/2/15) → stopped (graceful signals: quit, shutdown, a kill leak)
//   - other signal            → crashed (SIGSEGV/ABRT/… = an application fault)
//   - other non-zero (<128)   → crashed (the CLI itself exited non-zero)
func ExitReasonFor(code, sig int, oom bool) string {
	if code == 0 {
		return "exited"
	}
	if sig == 9 {
		if oom {
			return "oom"
		}
		return "killed"
	}
	switch sig {
	case 1, 2, 15: // SIGHUP, SIGINT, SIGTERM
		return "stopped"
	}
	return "crashed"
}

// Persist writes {state, ts} keyed by sid. Errors are logged (not
// swallowed): a failed write leaves the Console's 進行中/応答あり badge silently
// stale, so a log line is the only breadcrumb the write ever failed.
func Persist(sid, state string) { persist(sid, SessionStatus{State: state}) }

// PersistTurnEnd is Persist for a write that IS a turn's end (the Stop hook's idle,
// MarkTurnEnd の completed/failed/aborted)。TurnEnd を立てるのはこの入口だけ — 「今の
// 状態」を書くだけの Persist と、「ターンが終わった」を主張する書込みを分けておく。
func PersistTurnEnd(sid, state string) { persist(sid, SessionStatus{State: state, TurnEnd: true}) }

func persist(sid string, s SessionStatus) {
	s.TS = time.Now().Format(time.RFC3339)
	if err := statusFiles.Write(sid, s); err != nil {
		log.Printf("session-status: write %s: %v", statusFiles.Path(sid), err)
	}
}

func Read(sid string) (SessionStatus, bool) { return statusFiles.Read(sid) }

func Remove(sid string) {
	statusFiles.Remove(sid)
	RemovePendingQuestion(sid)
	RemovePendingPlan(sid)
	RemovePendingPermission(sid)
}

// LiveState reads the status file written by the agent's hooks/plugin,
// defaulting a live session with no recorded event to idle (sitting at the prompt).
func LiveState(sid string) string {
	state := "idle"
	if st, ok := Read(sid); ok {
		state = st.State
	}
	return state
}

// EffectiveModal resolves what the claude TUI is ACTUALLY showing, which the raw
// status state can lie about. AskUserQuestion / ExitPlanMode fire their OWN
// permission_prompt Notification between the tool's PreToolUse and its PostToolUse, so
// the stored state flips to "permission" while the terminal still shows the question
// menu / the plan approval dialog. applyPendingPayloads (package main) deliberately
// KEEPS the captured payload through that state, which makes the payload — not the
// state — the truth.
//
// EVERY reader has to apply this, display included. Judging by the raw state made
// /plan-respond answer no_plan for a plan that was plainly pending（2026-08-10 報告）,
// and left the AUQ 中のセッションのバッジを「許可待ち」と表示させた — 出ているカードは
// 質問なのにチップだけ許可を名乗る、という食い違い。
//
// Non-permission states pass through unchanged, so callers keep switching on it as
// before. Only the claude/codex hook route writes these payloads, so no other kind can
// hit the override.
func EffectiveModal(sid, state string) string {
	if state != "permission" {
		return state
	}
	// A question outranks a plan (surfacePendingPayloads shows the question card first,
	// and its keys must reach the question menu).
	if raw, ok := ReadPendingQuestion(sid); ok && len(raw) > 0 {
		return "question"
	}
	if plan, ok := ReadPendingPlan(sid); ok && plan != "" {
		return "plan"
	}
	return state
}

// last-tool: the tool about to run, recorded by the permtool PreToolUse hook and read
// when a permission prompt fires, to give the permission block a concrete subject.
func WriteLastTool(sid, detail string) {
	if detail == "" {
		return
	}
	_ = lastTools.Write(sid, detail)
}

func ReadLastTool(sid string) (string, bool) { return lastTools.Read(sid) }
func RemoveLastTool(sid string)              { lastTools.Remove(sid) }

// A pending AskUserQuestion (the tool_input.questions array), kept only while the
// session is in the question state so the Console can render and answer it.
func WritePendingQuestion(sid string, questions json.RawMessage) {
	_ = pendingQuestions.Write(sid, questions)
}

func ReadPendingQuestion(sid string) (json.RawMessage, bool) {
	b, ok := pendingQuestions.Read(sid)
	return json.RawMessage(b), ok
}

func RemovePendingQuestion(sid string) { pendingQuestions.Remove(sid) }

// pending-text: the assistant's streaming text for the in-flight turn, accumulated from
// the MessageDisplay hook. Kept only long enough for a pending AskUserQuestion to show
// the prose that preceded it (the turn's text lands in the transcript only after the
// question is answered). Reset each turn — see applyPendingPayloads (package main).
//
// pendingTextCap bounds the buffer: the prose before a question is small, so a runaway
// stream shouldn't grow an unbounded file (it's reset every turn regardless).
const pendingTextCap = 16 << 10

func AppendPendingText(sid, delta string) {
	if delta == "" {
		return
	}
	if err := os.MkdirAll(pendingTexts.Dir(), 0o700); err != nil {
		return
	}
	path := pendingTexts.Path(sid)
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

func ReadPendingText(sid string) (string, bool) { return pendingTexts.Read(sid) }
func RemovePendingText(sid string)              { pendingTexts.Remove(sid) }

// A pending ExitPlanMode plan (the tool_input.plan markdown), kept only while the
// session waits for plan approval so the Console can show it / open it in a pane.
func WritePendingPlan(sid, plan string)         { _ = pendingPlans.Write(sid, plan) }
func ReadPendingPlan(sid string) (string, bool) { return pendingPlans.Read(sid) }
func RemovePendingPlan(sid string)              { pendingPlans.Remove(sid) }

// A pending tool-permission prompt (the Notification message), kept while the session
// is blocked awaiting an allow/deny decision so the Console can approve it inline.
func WritePendingPermission(sid, message string)      { _ = pendingPerms.Write(sid, message) }
func ReadPendingPermission(sid string) (string, bool) { return pendingPerms.Read(sid) }
func RemovePendingPermission(sid string)              { pendingPerms.Remove(sid) }
