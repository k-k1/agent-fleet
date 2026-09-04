package agents

// The seam that lets a kind report, as structure, the modal currently on screen waiting on a
// human (docs/log/75 P5).
//
// Why it exists: promoting a carried interaction is trivial for claude — read pending-question
// / pending-plan / pending-perm out of the status store, which its hooks write to disk at ask
// time. No other kind has that hook, and the pending state is scattered:
//
//   - the last step in the conversation DB (agy)
//   - an unfinished permission.requested in events.jsonl (copilot)
//   - the pane footer (kiro's TUI approval panel)
//   - a handle in the driver's memory (session/request_permission for the three ACP kinds)
//   - an unanswered record in the native store (the question tools of codex / opencode)
//
// Folding a session (halt / stop) should not have to know any of that, so it all collapses
// into one method asked exactly once, immediately before the fold.
//
// Some implementations can only answer while the session is still alive (the ACP handle, the
// pane), so callers must call this BEFORE folding — see promoteCarriedFor in
// session_carried.go and the ordering in its callers halt / gracefulShutdown.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// PendingModal is one kind's answer to "is something waiting on a human right now, and what
// is it?".
type PendingModal struct {
	// Kind is "question" or "permission". plan is claude-specific (ExitPlanMode) and never
	// appears here.
	//
	// The two are kept apart because what can still be done after a resume differs. A
	// question's answer means something delivered as text, whereas an approval decision
	// cannot reach a dead tool call: the ACP JSON-RPC id and the TUI modal both die with
	// the process. All a permission can carry over is the fact of what was asked
	// (docs/log/75 §75.6.4).
	//
	// Getting this wrong does real damage: carrying a permission as a question makes the
	// Console draw a Yes/No card and then believe it sent the chosen answer to a
	// destination that no longer exists. To the user it looks like they approved something
	// that never ran, or the other way round.
	Kind string
	// Questions is the answer form used when Kind=="question". The Console renders it, the
	// user picks, and the chosen label becomes the text delivered after the resume.
	Questions []transcript.Question
	// Detail is what was being asked when Kind=="permission" (something like "Bash · npm ci").
	// It is not always available — copilot's events.jsonl sometimes carries only a requestId
	// — and is then empty, leaving the card to state the bare fact.
	Detail string
	// Text is the prose immediately preceding the question, empty when there is none.
	Text string
}

// ModalReporter is implemented by the kinds whose pending modal is NOT in the status
// store. claude does not implement it: there the pending-* entries written by its hooks are
// authoritative, and the same fact must not be claimed from two places.
type ModalReporter interface {
	PendingModal(m session.Meta) (PendingModal, bool)
}
