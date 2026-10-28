package cursor

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Optional agents capabilities of the cursor kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — line-parsed from `cursor-agent models`
// (id - display name, tied to the account — docs/log/40).
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}

// PaneMode is agents.PaneModeReader: this package's PaneMode.
func (agentImpl) PaneMode(pane string) string { return PaneMode(pane) }

// SendWaitsForComposer is agents.PaneModeReader: right after boot there is no composer yet,
// and text typed into the empty pane is lost.
func (agentImpl) SendWaitsForComposer() bool { return true }

// PromptModal is agents.ModalProbe: the approval and build menus read off the pane
// (TerminalModal). Terminal route only: the managed driver refuses free text itself
// (ErrQuestionPending).
func (agentImpl) PromptModal(m session.Meta) string { return TerminalModal(m) }
