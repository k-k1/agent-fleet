package muse

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
)

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

// LiveStateTraits is agents.LiveStater: the live handle's pending prompt (LiveState), since the
// status file holds only turn start and end; and the handle's last turn error (IsRateLimited).
func (agentImpl) LiveStateTraits() agents.LiveStateTraits {
	return agents.LiveStateTraits{State: LiveState, RateLimited: IsRateLimited}
}

// UsageLimitTraits is agents.UsageLimiter: the same shape as codex's, a subscription window that
// waiting clears, read from the handle's last turn error in memory (IsRateLimited, ResetAt).
// muse has only the managed driver, so there is no Terminal session to exclude.
func (agentImpl) UsageLimitTraits() agents.UsageLimitTraits {
	return agents.UsageLimitTraits{
		AtLimit: func(m session.Meta) (agents.LimitKind, bool) {
			return agents.LimitWindow, IsRateLimited(m.Name)
		},
		ResetAt: func(m session.Meta, _ bool, now time.Time) (time.Time, string, bool) {
			return ResetAt(m.Name, now)
		},
	}
}

// UsageMeasured is agents.UsageMeasurer: usagex.MeasuredPartial.
// muse (ADR 0095 decision 10): the wire's own per-turn numbers are clean (cached input is
// separated, the cumulative block never needs differencing, a failed turn reports nothing and
// a resume replays none of it), so partial is about OWNERSHIP, not precision: subagent and
// observer model calls are never folded into `session/tokenUsage` (gate B1 measured 88,077
// prompt tokens on the wire against 116,816 in the host's own log across six calls, two of
// them a subagent's). AF's clamps turn subagents and observers off, but a clamp is a setting
// and this is a declaration about the SOURCE, so it stays partial.
func (agentImpl) UsageMeasured() string { return usagex.MeasuredPartial }
