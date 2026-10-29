package opencode

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
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

// SkillsLayout is agents.SkillsLayouter: command md files in the working copy's .opencode/command
// and .opencode/commands (both spellings exist in the 1.18.8 binary), then the same two under the
// config dir, invoked as "/name". The working copy root only: discovery under a Subdir has not
// been measured (docs/log/50 §10). Its user root is not shared: it holds nothing but the fleet
// skills, and opencode itself already reads ~/.claude/skills.
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{
		Native:  agents.SkillsScan,
		Invoke:  "/",
		Project: []agents.SkillRoot{{Rel: ".opencode/command", Form: "commands"}, {Rel: ".opencode/commands", Form: "commands"}},
		User: []agents.UserSkillRoot{
			{Dir: func() string { return filepath.Join(paths.OpencodeConfigDir(), "command") }, Source: "user", Form: "commands"},
			{Dir: func() string { return filepath.Join(paths.OpencodeConfigDir(), "commands") }, Source: "user", Form: "commands"},
		},
	}
}

// ManagedLifecycle is agents.ManagedLifecycler: this package's ReconcileManaged, AbortManaged
// and Shutdown.
func (agentImpl) ManagedLifecycle() agents.ManagedLifecycle {
	return agents.ManagedLifecycle{Reconcile: ReconcileManaged, Abort: AbortManaged, Shutdown: Shutdown}
}

// ConnectionStatus is agents.ConnectionStatuser: this package's Status(in.Secrets).
func (agentImpl) ConnectionStatus(in agents.StatusInputs) map[string]any { return Status(in.Secrets) }

// LiveStateTraits is agents.LiveStater: opencode's own store (LiveState), since its status plugin
// is unreliable.
func (agentImpl) LiveStateTraits() agents.LiveStateTraits {
	return agents.LiveStateTraits{State: LiveState}
}
