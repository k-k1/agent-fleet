package agents

import (
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Optional Agent capabilities that replace the last two-branch kind literals outside this
// package (ADR 0124 Phase 3). Each is resolved by exact registry key; a kind without the
// interface gets the documented default.

// ModelInfoSuppressor: the picker says nothing about some of the kind's model ids, because
// the id is an alias whose real model depends on the installed CLI. Default: nothing is
// suppressed.
type ModelInfoSuppressor interface {
	SuppressesModelInfo(id string) bool
}

// ModelRetirer: the kind's own catalog says a model is going away. Default: none is.
type ModelRetirer interface {
	ModelRetirement(id string) (ModelRetiring, bool)
}

// PlaceholderDialectWriter: the placeholder syntax the kind's own MCP config writer uses,
// which is the target when a config is translated for it ("" = no native dialect, so nothing
// can be translated into it). The value is an mcpproj Dialect* string.
type PlaceholderDialectWriter interface {
	NativeDialect() string
}

// ChatModelDefaulter: the assistant chat runs the kind on the deployment-wide chat model
// (AF_CHAT_MODEL) when nothing pins one, and an auto turn's model override belongs to it
// alone. Default: the conversation's own model is passed as is.
type ChatModelDefaulter interface {
	UsesDeploymentChatModel() bool
}

// ChatFallbackModeler: the model the assistant chat estimates its context window from when
// nothing resolves one. Default: the deployment-wide chat model.
type ChatFallbackModeler interface {
	ChatFallbackModel() string
}

// MessageLimit is a usage limit read out of bare error text.
type MessageLimit struct {
	Kind LimitKind
	// PerModel: the limit names one model's window, not the whole kind's.
	PerModel bool
	// ResetAt is zero when the text names no instant.
	ResetAt time.Time
}

// MessageLimitReader: the kind reads its own provider's limit wording out of a failed call's
// error text. Default: only the generic markers apply.
type MessageLimitReader interface {
	LimitInMessage(msg string, now time.Time) (MessageLimit, bool)
}

// DeliveryBaseliner: the kind has a cheap submit ground truth, the sizes of its conversation
// and subagent transcripts, so a send can check that a user turn was appended. Default: the
// send is not verified that way.
type DeliveryBaseliner interface {
	DeliveryBaseline(sid string) (logs, subagents map[string]int64)
}

// SubmitRejectionConfirmer: the kind's Terminal TUI can hand a just-submitted prompt back, and
// the agent can see that on the pane. Default: no such check.
type SubmitRejectionConfirmer interface {
	ConfirmsTerminalSubmit() bool
}

// TranscriptBusyReporter: the kind's transcripts carry subagent and main-turn freshness the
// report reconciler reads, and its TUI spinner contract is the one tmuxx.IsBusy reads.
// Default: neither signal applies.
type TranscriptBusyReporter interface {
	ReportsTranscriptBusy() bool
}

// LiveContext is a running Managed session's context window, read from the live handle.
type LiveContext struct {
	Tokens       int
	Window       int
	WindowSource string // "estimated" or "recorded"
	Pct          float64
	Model        string
	// Credits is the metered credit spend; nil when the kind meters none, so a reported 0 is
	// told apart from "not metered".
	Credits *float64
}

// LiveContextReader: a running Managed session of the kind holds its context usage in memory.
// ok is false without a live handle or before the first report. Default: nothing live to add.
type LiveContextReader interface {
	LiveContext(name string) (LiveContext, bool)
}

// AttentionHooker: the kind's attention events (answer-ready, question, permission) already
// reach the outbox through structured hooks, so a terminal OSC notification from it reports
// the same moment a second time and is dropped. Default: the OSC notification is the only
// signal and is kept.
type AttentionHooker interface {
	AttentionViaHooks() bool
}

// ManagedMoveMirrorer: a Managed session of the kind is only a mirror of a CLI run, so a
// move from it warns about that and is refused while the run is stopped. Default: a Managed
// session moves like any other.
type ManagedMoveMirrorer interface {
	ManagedMoveIsMirror() bool
}

// FullTranscriptReader: the kind reads a conversation whole from its own log, because the
// generic transcript collector keeps only a tail and leaves ordinary tool results out.
// Default: the kind's Transcript is the whole conversation.
type FullTranscriptReader interface {
	// FullTurns is the whole conversation.
	FullTurns(m session.Meta) []transcript.Turn
	// FullMoveSource adds what a move needs: the prompts typed into the turn a limit cut off
	// (queued, never run) and the files the conversation edited.
	FullMoveSource(m session.Meta) (turns []transcript.Turn, queued []string, edits []transcript.FileEdit)
}
