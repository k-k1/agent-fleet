package main

// Chat-bridge P2b inbound (docs/37): apply a Discord button click to a session's
// pending AskUserQuestion / plan / permission — structurally, never via free text
// (ADR0020 契約6). The Gateway supervisor decodes the click and hands it here as a
// bridge.ParsedInteraction; this is the package-main half (session mutation) wired
// into bridge via ReceiverDeps.Answer, the same import-cycle dodge as Inject.
//
// Scope (v1): the claude/TUI interactive states — the ones the claude hooks record
// with a pending payload (session_status.go) and answer with the SAME key sequences
// the Console mirror uses (questionKeys.ts / MirrorView), so behaviour can't drift.
// A managed session's answer needs the live driver interaction id and is deferred
// to the Console (feedback below). Multi-question single-select forms accumulate one
// pick per question (bridge-answers store) and submit once every question is picked.

import (
	"encoding/json"
	"fmt"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fstore"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// bridgeAnswerState accumulates one pick per question for a multi-question
// single-select form (a button click answers a single question; the claude modal
// submits all at once). Keyed by session name; reset when the fingerprint changes
// (a new question form) and cleared on submit / staleness.
type bridgeAnswerState struct {
	Fp    string      `json:"fp"`
	Picks map[int]int `json:"picks"` // question index → chosen option index
}

var bridgeAnswers = fstore.JSON[bridgeAnswerState](paths.AgentConfigDir, "bridge-answers", ".json")

func clearBridgeAnswer(name string) { bridgeAnswers.Remove(name) }

// answerInteraction is bridge.ReceiverDeps.Answer: apply a decoded button click and
// return a short outcome line the receiver shows on the (now button-less) message.
// Errors are returned for logging; a user-visible outcome always comes back as text.
func answerInteraction(pi bridge.ParsedInteraction) (string, error) {
	en := bridgeAnswerEN()
	if !session.ValidName(pi.Session) {
		return "", fmt.Errorf("invalid session name %q", pi.Session)
	}
	meta, ok := session.ReadMeta(pi.Session)
	if !ok {
		return fb(en, "セッションが見つかりません", "Session not found"), nil
	}
	// v1: buttons only ever attach to claude (tui) prompts. A managed answer needs
	// the live driver interaction (follow-up) — steer the user to the Console rather
	// than drive a tmux pane the managed session doesn't have.
	if meta.DriverKind() == session.DriverManaged {
		return fb(en, "このセッションは Console で回答してください", "Answer this session from the Console"), nil
	}
	switch pi.Kind {
	case "q":
		return answerClaudeQuestion(meta, pi, en)
	case "p":
		return answerClaudeDecision(meta, "permission", permKeys(pi.Choice),
			decisionFeedback(pi.Choice, en), en)
	case "pl":
		return answerClaudeDecision(meta, "plan", planKeys(pi.Choice),
			decisionFeedback(pi.Choice, en), en)
	}
	return "", nil
}

// answerClaudeQuestion records a button's option pick and, once every question of
// the form is answered, drives the claude AskUserQuestion modal with the same
// single-select key sequence the Console builds (buildClaudeSeq). Staleness (the
// pending question is gone or changed) is rejected so a late click can't mis-answer.
func answerClaudeQuestion(meta session.Meta, pi bridge.ParsedInteraction, en bool) (string, error) {
	sid := session.UUID(meta.Dir, meta.Name)
	raw, ok := status.ReadPendingQuestion(sid)
	if !ok || len(raw) == 0 {
		clearBridgeAnswer(meta.Name)
		return fb(en, "この質問はもう回答済みです", "This question was already answered"), nil
	}
	if bridge.QuestionFingerprint(raw) != pi.Fp {
		clearBridgeAnswer(meta.Name)
		return fb(en, "質問の内容が変わりました", "The question changed — re-check in the Console"), nil
	}
	var qs []transcript.Question
	if err := json.Unmarshal(raw, &qs); err != nil || len(qs) == 0 {
		return "", fmt.Errorf("parse pending questions for %s: %w", meta.Name, err)
	}
	if pi.QI < 0 || pi.QI >= len(qs) || pi.OI < 0 || pi.OI >= len(qs[pi.QI].Options) {
		return fb(en, "選択肢が範囲外です", "Option out of range"), nil
	}
	picked := qs[pi.QI].Options[pi.OI].Label

	st, _ := bridgeAnswers.Read(meta.Name)
	if st.Fp != pi.Fp || st.Picks == nil {
		st = bridgeAnswerState{Fp: pi.Fp, Picks: map[int]int{}}
	}
	st.Picks[pi.QI] = pi.OI
	if len(st.Picks) < len(qs) {
		_ = bridgeAnswers.Write(meta.Name, st)
		return questionWaitFeedback(pi.QI, len(qs), picked, en), nil
	}
	// Every question answered — build and send the full single-select sequence.
	pane, err := claudePane(meta.Name)
	if err != nil {
		return "", err
	}
	if err := sendNamedKeys(pane, buildClaudeSingleSelectKeys(len(qs), st.Picks)); err != nil {
		return "", err
	}
	markSessionWorking(meta.Name)
	clearBridgeAnswer(meta.Name)
	return questionDoneFeedback(pi.QI, len(qs), picked, en), nil
}

// answerClaudeDecision drives a claude allow/deny or approve/reject modal. It guards
// on the live state (permission / plan) so a stale button click — after the prompt
// was already handled — can't inject stray navigation keys into a live composer.
func answerClaudeDecision(meta session.Meta, wantState string, keys []string, feedback string, en bool) (string, error) {
	sid := session.UUID(meta.Dir, meta.Name)
	if st, _ := status.Read(sid); st.State != wantState {
		return fb(en, "この確認はもう完了しています", "This prompt is no longer active"), nil
	}
	pane, err := claudePane(meta.Name)
	if err != nil {
		return "", err
	}
	if err := sendNamedKeys(pane, keys); err != nil {
		return "", err
	}
	markSessionWorking(meta.Name)
	return feedback, nil
}

// buildClaudeSingleSelectKeys reproduces console questionKeys.ts buildClaudeSeq for
// the single-select (no free-text) path: per question, Down×index then Enter (Enter
// selects and auto-advances the tab), then a trailing Enter for the review page's
// "Submit answers". Named keys only — no typed text — so sendNamedKeys suffices.
func buildClaudeSingleSelectKeys(n int, picks map[int]int) []string {
	var keys []string
	for qi := 0; qi < n; qi++ {
		for i := 0; i < picks[qi]; i++ {
			keys = append(keys, "Down")
		}
		keys = append(keys, "Enter")
	}
	return append(keys, "Enter")
}

// permKeys / planKeys mirror the exact sequences MirrorView drives (the version-
// verified option order): permission Allow = Enter, Deny = Down Down Enter; plan
// Approve = Enter, Reject = Down Down Down Enter (the "tell what to change" option).
func permKeys(choice string) []string {
	if choice == "deny" {
		return []string{"Down", "Down", "Enter"}
	}
	return []string{"Enter"} // allow
}

func planKeys(choice string) []string {
	if choice == "reject" {
		return []string{"Down", "Down", "Down", "Enter"}
	}
	return []string{"Enter"} // approve
}

// claudePane resolves the running session's tmux pane, erroring like injectSessionPrompt.
func claudePane(name string) (string, error) {
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		return "", errInjectNotRunning
	}
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return "", fmt.Errorf("could not resolve session pane for %q", name)
	}
	return pane, nil
}

// bridgeAnswerEN reports whether the Discord connection's notification language is
// English (feedback lines follow the same locale as the notifications).
func bridgeAnswerEN() bool {
	if s, err := secrets.Load(); err == nil && s.Discord != nil {
		return s.Discord.Lang == "en"
	}
	return false
}

func fb(en bool, ja, ens string) string {
	if en {
		return ens
	}
	return ja
}

// decisionFeedback is the outcome line for an allow/deny/approve/reject click.
func decisionFeedback(choice string, en bool) string {
	switch choice {
	case "allow":
		return fb(en, "✓ 許可しました", "✓ Allowed")
	case "deny":
		return fb(en, "✓ 拒否しました", "✓ Denied")
	case "approve":
		return fb(en, "✓ 承認しました", "✓ Approved")
	case "reject":
		return fb(en, "✓ 却下しました（修正指示待ち）", "✓ Rejected (awaiting revision)")
	}
	return fb(en, "✓ 送信しました", "✓ Submitted")
}

func questionWaitFeedback(qi, total int, label string, en bool) string {
	pos := fmt.Sprintf("[%d/%d] ", qi+1, total)
	return pos + fb(en, "✓ 選択: "+label+"（他の質問の回答待ち）",
		"✓ Chose: "+label+" (waiting for the other questions)")
}

func questionDoneFeedback(qi, total int, label string, en bool) string {
	pos := ""
	if total > 1 {
		pos = fmt.Sprintf("[%d/%d] ", qi+1, total)
	}
	return pos + fb(en, "✓ 選択: "+label+" — 回答を送信しました",
		"✓ Chose: "+label+" — answers submitted")
}
