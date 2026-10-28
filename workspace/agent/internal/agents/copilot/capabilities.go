package copilot

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Optional agents capabilities of the copilot kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — a PTY scrape of the TUI /model picker
// (live, so it reflects the plan — docs/log/36 addendum; Free offers only Auto, i.e. an empty
// list). Unspecified means auto routing.
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}

// PaneMode is agents.PaneModeReader: this package's PaneMode.
func (agentImpl) PaneMode(pane string) string { return PaneMode(pane) }

// SendWaitsForComposer is agents.PaneModeReader: even with trust pre-written there is no
// composer until the tab UI is drawn, and text sent to the boot screen disappears silently.
func (agentImpl) SendWaitsForComposer() bool { return true }

// PromptModal is agents.ModalProbe: an unfinished permission.requested in events.jsonl
// (LiveState) — the Terminal permission menu and managed's Interaction in the same shape.
func (agentImpl) PromptModal(m session.Meta) string { return LiveState(m) }
