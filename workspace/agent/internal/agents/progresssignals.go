package agents

// ProgressSignaler is an optional Agent capability: which live signals the Terminal progress
// reading (sessionx.progressOf) may trust for the kind. A kind without it is unmeasured: its
// Terminal rows never claim progress. Resolved by exact registry key, never through the claude
// fallback; callers normalize an unknown kind to claude first, as the old switches did.
type ProgressSignaler interface {
	ProgressSignals() ProgressTraits
}

// ProgressSignal is how far a kind's Terminal live signals have been measured.
type ProgressSignal int

const (
	// ProgressUnmeasured: no signal is known to be observable (the zero value).
	ProgressUnmeasured ProgressSignal = iota
	// ProgressHooks: the status file, the transcript and the pane all speak for the row.
	ProgressHooks
	// ProgressPaneOnly: the pane repaint is the only measured live signal, and nothing else
	// may stand in for it.
	ProgressPaneOnly
)

// ProgressTraits is one kind's progress measurement.
type ProgressTraits struct {
	Signal ProgressSignal
	// HooklessStatus: the kind has no status hook in a Terminal session, so the status file's
	// mtime cannot stand for the start of the current state. Negative so that the zero value
	// (a kind without the capability) keeps the mtime usable, as before.
	HooklessStatus bool
}
