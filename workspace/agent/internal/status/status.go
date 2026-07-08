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
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

type SessionStatus struct {
	State string `json:"state"` // "working" | "idle"
	TS    string `json:"ts"`    // RFC3339
}

// Per-sid file stores (internal/fstore). pending-question holds the raw tool_input
// payload; last-tool shares pending-perm's dir under a different extension.
var (
	statusFiles      = fstore.JSON[SessionStatus](paths.AgentConfigDir, "session-status", ".json")
	pendingQuestions = fstore.Raw(paths.AgentConfigDir, "pending-question", ".json")
	pendingPlans     = fstore.Strings(paths.AgentConfigDir, "pending-plan", ".md")
	pendingPerms     = fstore.Strings(paths.AgentConfigDir, "pending-perm", ".txt")
	lastTools        = fstore.Strings(paths.AgentConfigDir, "pending-perm", ".tool")
	pendingTexts     = fstore.Strings(paths.AgentConfigDir, "pending-text", ".txt")
)

// Persist writes {state, ts} keyed by sid. Errors are logged (not
// swallowed): a failed write leaves the Console's 進行中/応答あり badge silently
// stale, so a log line is the only breadcrumb the write ever failed.
func Persist(sid, state string) {
	s := SessionStatus{State: state, TS: time.Now().Format(time.RFC3339)}
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
