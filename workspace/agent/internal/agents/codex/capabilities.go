package codex

import (
	"path/filepath"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
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

// SkillsLayout is agents.SkillsLayouter. codex resolves project skills from the CWD up to the
// git root (.codex/skills and .agents/skills), then $CODEX_HOME/skills, with the bundled .system
// as a root of its own marked "cli"; invocation is a "$name" mention (measured on 0.145 and
// 0.154, docs/log/50 §7, §10).
func (agentImpl) SkillsLayout() agents.SkillsLayout {
	return agents.SkillsLayout{
		Native:      agents.SkillsScan,
		Invoke:      "$",
		Project:     []agents.SkillRoot{{Rel: ".codex/skills", Form: "skills"}, {Rel: ".agents/skills", Form: "skills"}},
		ProjectDirs: agents.SkillDirsCWDToGitRoot,
		User: []agents.UserSkillRoot{
			{Dir: func() string { return filepath.Join(paths.CodexHome(), "skills") }, Source: "user", Form: "skills"},
			{Dir: func() string { return filepath.Join(paths.CodexHome(), "skills", ".system") }, Source: "cli", Form: "skills"},
		},
		NativeConvs: []string{".codex/skills", ".agents/skills"},
		Shared:      &agents.SharedSkillRoot{Origin: ".codex", Dir: func() string { return filepath.Join(paths.CodexHome(), "skills") }},
	}
}

// TerminalTraits is agents.TerminalTraiter: codex 0.145+ shows no mode in its footer (so the
// mirror's mode changes are remembered), its startup update menu takes keystrokes ("Update now"
// exits the process), and its hooks miss an Esc interrupt's turn end and the
// request_user_input dialog, both read off the rollout instead.
func (agentImpl) TerminalTraits() agents.TerminalTraits {
	return agents.TerminalTraits{
		RemembersMode: true,
		TerminalScreen: func(pane string) string {
			if PaneScreen(pane) == "update" {
				return "update"
			}
			return ""
		},
		MissedTurnEnd:    MissedTurnEnd,
		TerminalQuestion: TerminalModal,
	}
}

// ManagedLifecycle is agents.ManagedLifecycler: this package's ReconcileManaged, AbortManaged
// and Shutdown.
func (agentImpl) ManagedLifecycle() agents.ManagedLifecycle {
	return agents.ManagedLifecycle{Reconcile: ReconcileManaged, Abort: AbortManaged, Shutdown: Shutdown}
}

// ConnectionStatus is agents.ConnectionStatuser: this package's Status().
func (agentImpl) ConnectionStatus(agents.StatusInputs) map[string]any { return Status() }

// LiveStateTraits is agents.LiveStater: a Managed turn that failed with usageLimitExceeded
// (IsRateLimited).
func (agentImpl) LiveStateTraits() agents.LiveStateTraits {
	return agents.LiveStateTraits{RateLimited: IsRateLimited, RateLimitedManagedOnly: true}
}

// UsageLimitTraits is agents.UsageLimiter: Managed only, since a TUI codex leaves nothing that
// says this session's turn died on the limit. codex names one usage limit (usageLimitExceeded),
// the kind waiting clears; its error vocabulary has no spend counterpart, so nothing here is
// agents.LimitSpend. The evidence is the managed handle's last turn error (in memory), else the
// rollout's last task_complete once the handle is gone (IsRateLimitedMeta), and
// the reset instant is the account's own windows (ResetAt).
func (agentImpl) UsageLimitTraits() agents.UsageLimitTraits {
	return agents.UsageLimitTraits{
		ManagedOnly: true,
		AtLimit: func(m session.Meta) (agents.LimitKind, bool) {
			return agents.LimitWindow, IsRateLimitedMeta(m)
		},
		ResetAt: func(_ session.Meta, _ bool, now time.Time) (time.Time, string, bool) {
			return ResetAt(now)
		},
	}
}
