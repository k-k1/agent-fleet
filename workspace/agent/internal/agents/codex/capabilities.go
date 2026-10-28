package codex

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Optional agents capabilities of the codex kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped.
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}

// PaneMode is agents.PaneModeReader: this package's PaneMode.
func (agentImpl) PaneMode(pane string) string { return PaneMode(pane) }

// SendWaitsForComposer is agents.PaneModeReader: false, a Terminal send is typed without
// waiting for the composer.
func (agentImpl) SendWaitsForComposer() bool { return false }

// PromptModal is agents.ModalProbe: the rollout's question (TerminalModal), else the screens the
// Terminal pane draws outside the conversation — the update menu, the lock screen, the
// model-switch nudge — which take a typed line's keys just the same (TerminalScreen). Those stay
// out of TerminalModal, which the chat chip reads as "question". Its hooks report working/idle
// at most; the managed driver refuses free text itself (ErrQuestionPending).
func (agentImpl) PromptModal(m session.Meta) string {
	if q := TerminalModal(m); q != "" {
		return q
	}
	return TerminalScreen(m)
}
