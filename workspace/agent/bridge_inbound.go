package main

// Chat-bridge inbound (docs/37 P2a): the receive half's landing point in package main.
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
	errInjectEmpty           = errors.New("empty prompt")
	errInjectNotRunning      = errors.New("session is not running")
	errInjectQuestionPending = errors.New("a question is awaiting an answer; it can't be free-texted")
)

// injectSessionPrompt delivers a free-text prompt into a running session with no HTTP layer —
// the in-process entry point behind chat-bridge inbound (docs/37 P2a). It mirrors
// handleSessionInput's {prompt} branch (session_io.go) but returns errors instead of writing
// an HTTP response, and reuses the same primitives (typeLineAndSubmit / driver Send /
// markSessionWorking) so behavior can't drift. It refuses to free-text a session with an
// awaiting question — typed text there mis-answers the AUQ (same guard as submitPromptTUI);
// P2b will map such answers to buttons instead.
func injectSessionPrompt(name, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errInjectEmpty
	}
	if !session.ValidName(name) {
		return fmt.Errorf("invalid session name %q", name)
	}
	if questionPending(name) {
		return errInjectQuestionPending
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

// startBridgeReceiver wires chat-bridge inbound to the injection primitive and starts the
// Gateway supervisor. Each injected message is recorded with its origin so the mirror badges
// the resulting user turn distinctly from self-typed input (docs/37 追加要件, docs/30 ②).
func startBridgeReceiver() {
	bridge.StartReceiver(bridge.ReceiverDeps{
		Inject: func(sessionName, text, source string) error {
			if err := injectSessionPrompt(sessionName, text); err != nil {
				return err
			}
			recordInjection(sessionName, text, source)
			return nil
		},
	})
}
