package sessionx

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// transcriptTraits holds the per-kind facts the messages handler needs that
// cannot live behind internal/agents: terminalState reads the pane through
// tmuxx helpers in this package, and the rest are wire vocabulary of this
// handler's response. A kind with no entry gets the zero value (generic
// windower, no terminal state, "Default" as the normal-mode label).
type transcriptTraits struct {
	// ownMessagesPath: the kind keeps a line-cursor reader of its own jsonl
	// instead of the generic windower over transcript.Turns.
	ownMessagesPath bool
	// terminalState reports a terminal-only state the transcript never shows
	// (codex's startup update menu); "" when none.
	terminalState func(name string) string
	// normalModeLabel is the managed mode label for TranscriptData.Mode
	// "normal"; "" means "Default".
	normalModeLabel string
	// planInMeta: the footer no longer prints the plan mode, so meta.Mode ==
	// "plan" is the only record of it and wins over a "Default" pane reading.
	planInMeta bool
}

var transcriptTraitTable = map[string]transcriptTraits{
	session.KindClaude:   {ownMessagesPath: true},
	session.KindCodex:    {terminalState: codexTerminalState, planInMeta: true},
	session.KindOpencode: {normalModeLabel: "Build"}, // same vocabulary as Console's defaultModeLabel
}

func transcriptTraitsOf(kind string) transcriptTraits { return transcriptTraitTable[kind] }

// normalModeLabelOf is the managed "normal" mode label for the kind.
func normalModeLabelOf(kind string) string {
	if l := transcriptTraitsOf(kind).normalModeLabel; l != "" {
		return l
	}
	return "Default"
}
