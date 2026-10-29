package kiro

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Optional agents capabilities of the kiro kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — `kiro-cli chat --list-models -f json`
// (fully machine-readable, tied to the account — docs/log/43).
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}

// PaneMode is agents.PaneModeReader: this package's PaneMode.
func (agentImpl) PaneMode(pane string) string { return PaneMode(pane) }

// SendWaitsForComposer is agents.PaneModeReader: right after boot there is no composer yet,
// and text typed into the empty pane is lost.
func (agentImpl) SendWaitsForComposer() bool { return true }

// PromptModal is agents.ModalProbe: the approval panel's text in the pane ("shell requires
// approval"), reported as "question" (LiveState); managed is also guarded by
// ErrQuestionPending.
func (agentImpl) PromptModal(m session.Meta) string { return LiveState(m) }

// SkillsLayout is agents.SkillsLayouter: no user-invocable mechanism confirmed yet (docs/log/50
// §7), so the other conventions' SKILL.md trees are offered as foreign entries only.
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{Native: agents.SkillsForeignOnly}
}

// ManagedLifecycle is agents.ManagedLifecycler: this package's ReconcileManaged, AbortManaged
// and Shutdown.
func (agentImpl) ManagedLifecycle() agents.ManagedLifecycle {
	return agents.ManagedLifecycle{Reconcile: ReconcileManaged, Abort: AbortManaged, Shutdown: Shutdown}
}

// ConnectionStatus is agents.ConnectionStatuser: this package's Status().
func (agentImpl) ConnectionStatus(agents.StatusInputs) map[string]any { return Status() }
