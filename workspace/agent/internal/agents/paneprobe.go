package agents

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// PaneModeReader is an optional Agent capability for a kind whose Terminal composer can be
// read off its tmux pane. sessionx.PaneMode resolves it by exact registry key; a kind without
// it has no mode chip and never counts as "composer drawn".
type PaneModeReader interface {
	// PaneMode is the composer's mode label (the Console's mode chip: "Plan", "Default",
	// "Build", …) read off the whole captured pane, "" while the composer is not drawn.
	PaneMode(pane string) string
	// SendWaitsForComposer reports that text typed before the composer is drawn is lost (a
	// boot or sign-in screen eats it), so a Terminal send waits for PaneMode to answer.
	SendWaitsForComposer() bool
}

// ModalProbe is an optional Agent capability for a kind whose Terminal modals (question,
// permission, approval menus) never reach the status store: PromptModal is the modal it is
// showing now, "" when none, read from the same source its live state is built on so a refused
// prompt and the Console's badge agree. sessionx's prompt gate refuses typed text while it is
// non-free. Every kind with a Terminal route has it except claude, whose hooks write its
// modals into the status store, and shell / ssm, which have none.
type ModalProbe interface {
	PromptModal(m session.Meta) string
}
