package agents

import "github.com/k-k1/agent-fleet/workspace/agent/internal/session"

// PendingQuestionAnswerer is an optional Agent capability: a Terminal session of the kind
// records a structured pending-question payload (through hooks), so the Console can answer it
// without typing into the pane. A kind without it must be answered from the pane. Resolved by
// exact registry key.
type PendingQuestionAnswerer interface {
	AnswersPendingQuestion() bool
}

// PlanResponder is an optional Agent capability: the kind has a plan mode whose approval a
// plan-respond request can answer. A kind without it has no plan approval to answer. Resolved
// by exact registry key.
type PlanResponder interface {
	RespondsToPlan() bool
}

// TerminalSwitchGuard is an optional Agent capability: how a Managed -> Terminal switch of the
// kind must wait for the Managed runtime to let go of the session. Resolved by exact registry
// key; a kind without it switches without waiting.
type TerminalSwitchGuard interface {
	// SwitchNeedsStopped: the switch is refused unless the session is stopped, because the
	// Terminal CLI cannot open a thread the shared Managed runtime still has loaded.
	SwitchNeedsStopped() bool
	// DropForTerminalSwitch stops the Managed runtime and, when the kind guards the session
	// with a cross-process lock, waits (bounded) for it to be released. It reports false when
	// the kind has no such wait and the caller should drop the runtime as usual.
	DropForTerminalSwitch(name string) bool
}

// SlotSessionIDer is an optional Agent capability: the provider-side session id a slot is
// mapped to, which the af MCP server matches against the id a caller plugin stamps on a call.
// A kind without it has no such mapping.
type SlotSessionIDer interface {
	SlotSessionID(m session.Meta) string
}
