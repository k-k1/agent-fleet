package agents

// LaunchPreparer is an optional Agent capability: work to redo right before a session of the
// kind launches, because state outside the session can go stale while the Agent keeps running
// (claude's settings.json hooks, rewritten by another build of the Agent). It must be cheap and
// idempotent. Resolved by exact registry key; a kind without it prepares nothing.
type LaunchPreparer interface {
	PrepareLaunch()
}

// ManagedAbortSignaler is an optional Agent capability: a Managed session of the kind reports a
// turn's abort and idle through the status hook, so the abort-resume signal must be recorded
// and cleared there. A kind without it records none. Resolved by exact registry key.
type ManagedAbortSignaler interface {
	ManagedAbortSignals() bool
}

// PaneStartMarker is an optional Agent capability: the text the kind's CLI leaves in a tmux
// pane's start command. It is the last-resort sniff for an orphan session with no meta, so a
// pane that merely ran the program, or a path holding the word, matches too. The empty string
// opts out.
type PaneStartMarker interface {
	PaneStartMarker() string
}
