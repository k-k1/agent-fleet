// Driver-layer types (docs/log/27 §3-§6). The seam for adding thread-level control (write)
// and subscription (live) on top of the read layer (the Agent interface) while leaving that
// layer intact. These types plus the HTTP entry points (POST /sessions/{name}/turn and
// /respond, session_turn.go in package main) are the whole contract the managed drivers
// (opencode serve, codex app-server) implement.
//
// The TUI route (the classic TUI inside tmux) does not implement ThreadHandle — a TUI has no
// Events/Snapshot to offer, so rather than force the interface onto it the /turn handler
// delegates straight to the existing tmux path (type+submit / send-keys in session_io.go).
// Console makes the same call for either driver.
package agents

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// TurnInput is the payload of Send (the turn/start equivalent) and Steer (turn/steer).
type TurnInput struct {
	Prompt string
	// Attachments are absolute paths of files attached to this turn (docs/log/27 §10: managed
	// attaches through the API instead of pasting into tmux). On the TUI route Console still
	// weaves the paths into the prompt body, so only a managed driver interprets this field
	// (codex turn/start input items, opencode serve file parts).
	Attachments []string
	// ClientMessageID is the AF-issued idempotency key (§4): it makes a resend, or a double
	// submission after a reconnect, idempotent. The ledger that backs it holds operational
	// metadata only, never conversation content (§9.5).
	ClientMessageID string
}

// ThreadSettings is a dynamic settings update (§9.4-3: changing the model/effort of a
// running session is possible only under managed). An empty field means "leave unchanged".
type ThreadSettings struct {
	Model       string
	Effort      string
	Mode        string // "plan" | "normal" (same vocabulary as TranscriptData.Mode)
	ClearModel  bool   // explicit reset to the runtime/provider default
	ClearEffort bool   // explicit reset to the selected model's default
}

// Interaction generalises approval, question and plan confirmation (§5). Only questions are
// in scope: all three kinds run with approvals bypassed, so questions are the only thing that
// reaches an operator. The form itself is carried as a transcript.Question list, the same as
// the existing Pending UI — claude's AskUserQuestion puts several questions in one modal, so
// that shape fits reality better than the single Options of design §5.
type Interaction struct {
	ID        string
	Kind      string // "question" (future: "approval" | "plan")
	Prompt    string // explanation that ran before the question (the mirror's pendingText)
	Questions []transcript.Question
}

// Decision is the reply verb for an Interaction (§5).
type Decision string

const (
	DecisionAllow  Decision = "allow"
	DecisionDeny   Decision = "deny"
	DecisionCancel Decision = "cancel"
	DecisionAnswer Decision = "answer" // question kind: Answers carries the body
)

// Scope is how long an allow/deny decision sticks (§5).
type Scope string

const (
	ScopeOnce   Scope = "once"
	ScopeTurn   Scope = "turn"
	ScopeThread Scope = "thread"
)

// InteractionAnswer is one question's answer inside a reply. A multi-question form
// (claude AskUserQuestion) replies with one entry per question, in order.
type InteractionAnswer struct {
	Text    string `json:"text,omitempty"`    // free text (the "Type something" entry)
	Options []int  `json:"options,omitempty"` // option indexes (several when multi-select)
}

// InteractionReply is the wire body of POST /sessions/{name}/respond and the
// argument of ThreadHandle.Respond.
type InteractionReply struct {
	ID       string              `json:"id"`
	Decision Decision            `json:"decision"`
	Scope    Scope               `json:"scope,omitempty"`
	Answers  []InteractionAnswer `json:"answers,omitempty"`
}

// TurnState is a state of the turn state machine (§4). It is projected onto the existing
// WireLive vocabulary (working / idle / question / compacting); the wire contract is unchanged.
type TurnState string

const (
	TurnQueued             TurnState = "queued" // ClientMessageID assigned, not yet handed to the runtime
	TurnStarting           TurnState = "starting"
	TurnRunning            TurnState = "running"
	TurnWaitingInteraction TurnState = "waiting_interaction"
	TurnInterrupting       TurnState = "interrupting"
	TurnCompleted          TurnState = "completed"
	TurnFailed             TurnState = "failed"
	TurnCancelled          TurnState = "cancelled"
	TurnUnknown            TurnState = "unknown" // the honest state on a disconnect — settled by the §6 procedure
	// TurnAborted is a turn that died before producing an answer but can carry on if it is
	// resent (dropped connection, transient rate limit). It is kept apart from TurnFailed
	// because a failure a resend cannot fix (out of credit, prompt too long) and an
	// interruption a resend does fix call for opposite actions from the operator
	// (docs/log/47).
	TurnAborted TurnState = "aborted"
)

// Event is a live notification from a managed runtime. The vocabulary is deliberately broad,
// since it settles alongside the subscription implementations. Events are never replayed (no
// EventReplay) — a gap is recovered by Snapshot reconciliation (§6).
type Event struct {
	Kind        string // "turn_state" | "interaction" | "settings" (extended as drivers land)
	TurnState   TurnState
	Interaction *Interaction
	Settings    *ThreadSettings
}

// ThreadSnapshot is the reconciliation (§6) view of where a thread stands: the material for
// settling the turn state against the native history (the read layer) after a disconnect or a
// daemon restart.
type ThreadSnapshot struct {
	TurnState   TurnState
	Interaction *Interaction // its contents while waiting_interaction
	Settings    ThreadSettings
}

// ThreadHandle is the per-thread write/subscribe surface (§3). Driver.Resume returns it, and
// it does not know where the process runs (app-server / serve / which generation).
type ThreadHandle interface {
	Send(in TurnInput) error  // the turn/start equivalent
	Steer(in TurnInput) error // the turn/steer equivalent (extra input into a running turn)
	Interrupt() error         // the turn/interrupt equivalent
	UpdateSettings(s ThreadSettings) error
	Respond(reply InteractionReply) error
	Events() <-chan Event
	Snapshot() (ThreadSnapshot, error)
}

// Capabilities is the capability declaration Console renders from (§3.1). Console holds no
// `kind == "codex"` branch; it derives the affordances from here, the same discipline that
// folded 50 kind branches in agents.go into Caps.
type Capabilities struct {
	ProcessModel    string // "shared-daemon" | "per-session-child" | "tui"
	Steer           bool
	Fork            bool
	DynamicModel    bool
	DynamicEffort   bool
	DynamicMode     bool
	Permissions     bool // supported Interaction kind (approval)
	Questions       bool // supported Interaction kind (question)
	EventReplay     bool // expected false for all kinds → recovery is snapshot reconciliation (§6)
	EphemeralThread bool // isolated one-shot thread (room for chat integration, §9.3)
	TUIAttach       bool // OpenCode only (attach a TUI to serve without stopping it)
}

// Driver is the per-kind managed implementation (§3): it inherits the read layer (Agent) as
// is and adds thread-level control and subscription.
type Driver interface {
	Agent
	Capabilities() Capabilities
	Resume(m session.Meta) (ThreadHandle, error) // starts a new thread when there is none
}
