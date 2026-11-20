package kiro

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
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

// LiveStateTraits is agents.LiveStater: the TUI string contract (LiveState); kiro has no Stop hook
// (2.14.1's triggers are AgentSpawn/PrePrompt/PreToolUse/PostToolUse only, measured in docs/log/43
// §5-1), so that poll is where a Terminal turn ends. A pending approval reads "question", not idle;
// before the footer is drawn it has no opinion.
//
// StaleWorkingIdle: a pane has no position to pin an input to (no TurnMark/InputSeen), so an
// input that never reaches the CLI cannot be told from a slow start. Without the trait DriveState
// keeps the stored "working" for as long as the pane says nothing, while the list reads idle once
// the warm-up ends.
func (agentImpl) LiveStateTraits() agents.LiveStateTraits {
	return agents.LiveStateTraits{State: LiveState, PolledTurnEnd: true, StaleWorkingIdle: true}
}

// UsageMeasured is agents.UsageMeasurer: usagex.MeasuredNone: no tokens in the transcript.
func (agentImpl) UsageMeasured() string { return usagex.MeasuredNone }

// NoHeadlessChat is agents.NoHeadlessChat: chatx has no provider for this kind.
func (agentImpl) NoHeadlessChat() {}

// TUIInterrupt is agents.TUIInterrupter: Esc interrupts a running turn (docs/log/133 §1).
func (agentImpl) TUIInterrupt(func() bool) agents.TUIInterruptTraits {
	return agents.TUIInterruptEscape()
}

// WarmStatus is agents.StatusWarmer: this package's WarmStatus().
func (agentImpl) WarmStatus() { WarmStatus() }

// ProgressSignals is agents.ProgressSignaler: ProgressPaneOnly; its Terminal sessions have no status hook.
func (agentImpl) ProgressSignals() agents.ProgressTraits {
	return agents.ProgressTraits{Signal: agents.ProgressPaneOnly, HooklessStatus: true}
}

// SwitchNeedsStopped is agents.TerminalSwitchGuard: kiro switches from a live session.
func (agentImpl) SwitchNeedsStopped() bool { return false }

// DropForTerminalSwitch is agents.TerminalSwitchGuard: the per-sid `.lock` guards the session
// across processes, so wait bounded for the child to exit and release it; otherwise the TUI's
// `--resume-id` relaunch races it into an error or a split-brain new sid.
func (agentImpl) DropForTerminalSwitch(name string) bool {
	DropHandleWait(name, 5*time.Second)
	return true
}
