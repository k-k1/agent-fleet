package agents

import "time"

// TUIInterrupter is an optional Agent capability: the keys the chat's stop button sends to the
// kind's Terminal pane. Each kind declares only what docs/log/133 §1 measured for it; a kind
// without the capability (agy was never measured; shell, ssm, lcpp, muse) gets a single Escape
// reported as unmeasured. Resolved by exact registry key, never through the claude fallback.
type TUIInterrupter interface {
	// TUIInterrupt takes inSubagentView so a kind whose keys depend on the pane (opencode) asks
	// only when it needs to; every other kind must not call it.
	TUIInterrupt(inSubagentView func() bool) TUIInterruptTraits
}

// TUIInterruptTraits is one kind's stop sequence in a Terminal pane.
type TUIInterruptTraits struct {
	Keys []string
	// Gap separates the keys; it is the whole delay between two presses of the same key.
	Gap time.Duration
	// Measured is false when docs/log/133 holds no observation of this sequence.
	Measured bool
	// NeedsWorking: the keys are harmful on an idle CLI (Ctrl+C can exit it), so they are sent
	// only while the kind's live-state verdict (LiveStateTraits.State) says "working".
	NeedsWorking bool
}

// TUIInterruptDefaultGap is the pause between keys for every sequence that is not a double press.
const TUIInterruptDefaultGap = 90 * time.Millisecond

// TUIInterruptEscape is the measured single Escape (claude, codex, kiro).
func TUIInterruptEscape() TUIInterruptTraits {
	return TUIInterruptTraits{Keys: []string{"Escape"}, Gap: TUIInterruptDefaultGap, Measured: true}
}

// TUIInterruptCtrlC is the measured Ctrl+C that stops a running turn and nothing else (cursor,
// copilot): it needs a working verdict first.
func TUIInterruptCtrlC() TUIInterruptTraits {
	return TUIInterruptTraits{Keys: []string{"C-c"}, Gap: TUIInterruptDefaultGap, Measured: true, NeedsWorking: true}
}
