package sessionx

// Chat-bridge inbound (docs/log/37 P2a): the receive half's landing point in package main.
// The Gateway supervisor lives in internal/bridge, but the session-injection primitives
// live here — so bridge takes an Inject callback (avoids an import cycle) and this file
// supplies it. A reply the bound user posts in a session's Discord thread arrives here as
// injectSessionPrompt(name, text); the session's answer rides the existing answer-ready
// notification back to the same thread (P1 send path) — no extra wiring.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

var (
	ErrInjectEmpty           = errors.New("empty prompt")
	errInjectNotRunning      = errors.New("session is not running")
	errInjectQuestionPending = errors.New("a question is awaiting an answer; it can't be free-texted")
	// A plan approval / permission prompt is a DECISION menu: free text would be
	// swallowed and its Enter would confirm the highlighted row (= approve / allow).
	errInjectDecisionPending = errors.New("a decision is awaiting an answer; it can't be free-texted")
)

// injectSessionPrompt delivers a free-text prompt into a running session with no HTTP layer —
// the in-process entry point behind chat-bridge inbound (docs/log/37 P2a). It mirrors
// HandleSessionInput's {prompt} branch (session_io.go) but returns errors instead of writing
// an HTTP response, and reuses the same primitives (typeLineAndSubmit / driver Send /
// markSessionWorking) so behavior can't drift. It refuses to free-text a session with a
// pending interaction — typed text mis-answers an AUQ, and in the plan / permission
// dialogs it is swallowed while the Enter confirms approve / allow (same guard as
// submitPromptTUI); P2b will map such answers to buttons instead.
func injectSessionPrompt(name, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ErrInjectEmpty
	}
	if !session.ValidName(name) {
		return fmt.Errorf("invalid session name %q", name)
	}
	if st := promptBlocker(name); st != "" {
		if st == "question" {
			return errInjectQuestionPending
		}
		return errInjectDecisionPending
	}
	if meta, ok := session.ReadMeta(name); ok && meta.DriverKind() == session.DriverManaged {
		return injectManagedPrompt(meta, prompt)
	}
	tn := session.TmuxName(name)
	if !tmuxx.HasSession(tn) {
		return errInjectNotRunning
	}
	pane := tmuxx.SessionPaneID(tn)
	if pane == "" {
		return fmt.Errorf("could not resolve session pane for %q", name)
	}
	if err := typeLineAndSubmit(name, pane, prompt); err != nil {
		return err
	}
	// A slash command isn't a turn — don't optimistically mark working (mirrors submitPromptTUI).
	if !slashCmdRe.MatchString(prompt) {
		markSessionWorking(name)
	}
	return nil
}

// injectManagedPrompt is the managed-session (no tmux pane) counterpart — the non-HTTP core
// of handleManagedInputPrompt (session_io.go).
func injectManagedPrompt(meta session.Meta, prompt string) error {
	d, ok := driverOf(meta)
	if !ok {
		return fmt.Errorf("managed driver unavailable for kind %s", meta.Kind)
	}
	h, err := d.Resume(meta)
	if err != nil {
		return err
	}
	if err := h.Send(agents.TurnInput{Prompt: prompt}); err != nil {
		return err
	}
	markSessionWorking(meta.Name)
	return nil
}

// StartBridgeReceiver wires chat-bridge inbound to the injection primitive and starts the
// Gateway supervisor. Each injected message is recorded with its origin so the mirror badges
// the resulting user turn distinctly from self-typed input (docs/log/37 additional requirements, docs/log/30 ②).
func StartBridgeReceiver() {
	bridge.StartReceiver(bridge.ReceiverDeps{
		Inject: func(sessionName, text, source string) (string, error) {
			if err := injectSessionPrompt(sessionName, text); err != nil {
				return injectFailureReason(err), err
			}
			recordInjection(sessionName, text, source)
			return "", nil
		},
		// P2b: a button click (AskUserQuestion pick / permission / plan decision) is
		// applied structurally (bridge_answer.go), never as free text (contract 6).
		Answer: answerInteraction,
		// P3, brought forward: a reply in the dedicated operator thread runs a turn on the built-in
		// operator assistant conversation (bridge_operator.go); the reply is posted back.
		Operator: runOperatorTurn,
	})
}

// injectFailureReason maps an injectSessionPrompt error to a short, localized line the
// receiver posts back into the thread so the user knows why their reply was dropped.
// Known cases get a specific hint; anything else is wrapped generically so no dev-facing
// error text leaks to chat. Locale follows the connection's notification language.
func injectFailureReason(err error) string {
	en := BridgeAnswerEN()
	switch {
	case errors.Is(err, errInjectQuestionPending):
		return Fb(en, "⚠️ 質問への回答待ちです。テキストではなくボタン（または Console）で回答してください",
			"⚠️ A question is awaiting an answer — reply with the buttons (or the Console), not free text")
	case errors.Is(err, errInjectDecisionPending):
		return Fb(en, "⚠️ 承認/許可の判断待ちです。テキストは判断メニューに飲まれてしまうため、Console のカードから決めてください",
			"⚠️ A plan/permission decision is pending — decide it from the Console card; free text would be swallowed by the menu")
	case errors.Is(err, errInjectNotRunning):
		return Fb(en, "⚠️ セッションが停止しています。開始してから返信してください",
			"⚠️ The session isn't running — start it, then reply")
	case errors.Is(err, ErrInjectEmpty):
		return "" // nothing typed — no need to explain
	}
	return Fb(en, "⚠️ 返信をセッションに届けられませんでした", "⚠️ Couldn't deliver your reply to the session")
}
