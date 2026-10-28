package opencode

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Optional agents capabilities of the opencode kind, each delegating to this package's own
// functions.

// ListModels is agents.ModelLister: Models() shaped by the billing route (CatalogWithRoute).
// One key can open both the Zen (pay-as-you-go) and Go (subscription) providers, so the same
// model name appears under both; whether Zen is shown follows q.CatalogPref. An explicit model
// is never swallowed here: session create validates it against the full, unshaped catalog.
// The route is reported because the empty-menu rescue may have re-shaped with Zen, and the
// Console has to be able to say so (docs/log/103).
func (agentImpl) ListModels(q agents.ModelQuery) agents.ModelListing {
	ids := Models()
	list, route := CatalogWithRoute(ids, q.CatalogPref)
	return agents.ModelListing{Models: list, Enumerated: len(ids), Listed: ids, Route: route}
}

// PaneMode is agents.PaneModeReader: this package's PaneMode.
func (agentImpl) PaneMode(pane string) string { return PaneMode(pane) }

// SendWaitsForComposer is agents.PaneModeReader: false, a Terminal send is typed without
// waiting for the composer.
func (agentImpl) SendWaitsForComposer() bool { return false }

// PromptModal is agents.ModalProbe: the question in opencode's store (TerminalModal); its plugin
// reports working/idle at most. Terminal route only: the managed driver refuses free text itself
// (ErrQuestionPending).
func (agentImpl) PromptModal(m session.Meta) string { return TerminalModal(m) }
