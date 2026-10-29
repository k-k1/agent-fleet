package muse

import "github.com/k-k1/agent-fleet/workspace/agent/internal/agents"

// Optional agents capabilities of the muse kind, each delegating to this package's own function.

// ListModels is agents.ModelLister: Models(), unshaped — `model/list` over the session protocol
// (ADR 0095 decision 10): muse has no `models` subcommand, so the catalog is a wire query against
// a host. Empty until the binary is installed AND a credential is stored.
func (agentImpl) ListModels(agents.ModelQuery) agents.ModelListing {
	return agents.ModelListing{Models: Models(), Enumerated: -1}
}

// SkillsLayout is agents.SkillsLayouter: the session's own host answers skill/list over MSP (ADR
// 0095 P2-23) — bundled, the member's ~/.config/muse/skills, plugin skills and the working copy's
// .agents/skills. The repository Skills view shows the one tree muse documents.
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{
		Native:      agents.SkillsHosted,
		Invoke:      "/",
		Project:     []agents.SkillRoot{{Rel: ".agents/skills", Form: "skills"}},
		NativeConvs: []string{".agents/skills"},
		Shared:      &agents.SharedSkillRoot{Origin: ".muse", Dir: SkillsDir},
	}
}

// ManagedLifecycle is agents.ManagedLifecycler: this package's ReconcileManaged, AbortManaged
// and Shutdown.
func (agentImpl) ManagedLifecycle() agents.ManagedLifecycle {
	return agents.ManagedLifecycle{Reconcile: ReconcileManaged, Abort: AbortManaged, Shutdown: Shutdown}
}

// ConnectionStatus is agents.ConnectionStatuser: this package's Status(). supported is false until
// the on-demand install lands the proprietary binary (ADR 0095); connected reads
// ~/.config/muse/auth.json, so it needs no subprocess.
func (agentImpl) ConnectionStatus(agents.StatusInputs) map[string]any { return Status() }
