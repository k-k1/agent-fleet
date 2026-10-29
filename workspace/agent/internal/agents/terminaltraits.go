package agents

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// TerminalTraiter is an optional Agent capability: how the kind's Terminal (CLI) pane behaves
// beyond its composer — the screens and traps a send must not type into, how a prompt is typed,
// and the live-state verdicts the pane and the CLI's own files give. sessionx's input, readiness,
// output and DriveState paths read it. A nil func or a zero field means the kind has no such
// trait, which is what every kind without the capability gets.
type TerminalTraiter interface {
	TerminalTraits() TerminalTraits
}

// TerminalTraits is one kind's Terminal behaviour. Each field names what breaks without it.
type TerminalTraits struct {
	// AuthExpired reports that the CLI's own login has expired: the TUI then accepts the
	// characters and the Enter, and not a single turn starts, so a send is refused and the live
	// state says so instead.
	AuthExpired func() bool
	// UsageLimitMenu reports that the pane is parked on the CLI's usage-limit menu, where typed
	// text becomes a menu selection. A kind with it also reads the menu off the pane in its live
	// state, and a turn cut off by a limit reads as waiting for the reset.
	UsageLimitMenu func(name string) bool
	// AgentsViewActive / LeaveAgentsView: the composer can be bound to a background agent or the
	// agents screen, where typed text never reaches the conversation; a send returns it first.
	AgentsViewActive func(name string) bool
	LeaveAgentsView  func(name string) bool
	// ModalActive, when set, says a bare Left arrow leaves the conversation for the agents
	// screen, so it is accepted only while a dialog is open (ModalActive).
	ModalActive func(name string) bool
	// ShapePrompt rewrites a prompt before it is typed (claude quotes image paths, or its paste
	// handler turns them into [Image #N] and splits the text).
	ShapePrompt func(text string) string
	// PasteLF: tmux turns each LF of a paste into CR, so a CRLF arrives as CR CR and the CLI
	// reads two breaks; send one LF per break.
	PasteLF bool
	// SubmitDelay is the pause between typing and Enter; 0 means the default. A kind whose
	// composer submits reliably back-to-back takes a token pause.
	SubmitDelay time.Duration
	// RemoteControlURL is the session's remote-control URL, "" when it was never used; a stop
	// disconnects it first so the remote end is not left attached to a dead pane.
	RemoteControlURL func(sid string) string
	// HookPending: the kind's hooks capture the pending question and plan payloads, which
	// /status passes on so an operator can relay them.
	HookPending bool
	// TranscriptLines: /output reads the kind's raw transcript lines (claude's jsonl) instead
	// of the generic Transcript() turns.
	TranscriptLines bool
	// RemembersMode: mirror-driven mode changes (/plan to enter, BTab to toggle) are persisted
	// in meta, because the pane shows no mode signal to read back.
	RemembersMode bool
	// TerminalScreen classifies a terminal-only screen in a captured pane (a startup menu,
	// compaction, an update prompt) that keystrokes go to instead of a prompt; "" for none.
	TerminalScreen func(pane string) string
	// HealIdle clears a stale non-idle live state the way the kind needs (claude reports a
	// turn that died on an API error); without it the marker is just removed.
	HealIdle func(sid string)
	// MissedTurnEnd reports that a "working" outlived the turn (an Esc interrupt fires no Stop
	// hook), so the live state reads idle.
	MissedTurnEnd func(m session.Meta) bool
	// TerminalQuestion is a question dialog the hooks never report (they say working/idle at
	// most), so a "working" live state reads "question".
	TerminalQuestion func(m session.Meta) string
}
