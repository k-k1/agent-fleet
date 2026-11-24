package agy

// The wire of `agy --input-format stream-json --output-format stream-json`, as the Managed
// driver (managed.go) reads it. Measured on agy 1.3.5 (docs/log/32, "stream-json での Managed
// 方式の再評価"): stdin takes one `{"event":"user",…}` line per turn, stdout answers in NDJSON
// with one `init` per process (after the first message), `step_update` per step and one `result`
// per turn. The process stays up between turns.
//
// `result.usage` and `result.denied_actions` are cumulative over the PROCESS, not the turn, so
// a turn's own numbers are derived here: usage from the steps, denials from two independent
// signals (turnTracker.finish).

import (
	"encoding/json"
	"strings"
)

// streamEvent is one stdout line. Only the fields the driver reads are decoded.
type streamEvent struct {
	Event string `json:"event"`
	// ConversationID rides on the envelope of init and of every step_update.
	ConversationID string `json:"conversation_id"`
	Init           struct {
		ConversationID string `json:"conversation_id"`
		Model          string `json:"model"`
		PermissionMode string `json:"permission_mode"`
	} `json:"init"`
	Step   streamStep   `json:"step_update"`
	Result streamResult `json:"result"`
}

type streamStep struct {
	StepIndex int          `json:"step_index"`
	State     string       `json:"state"`     // ACTIVE | DONE | ERROR
	StepType  string       `json:"step_type"` // user_input | agent_response | tool | system_message | unknown
	TextDelta string       `json:"text_delta"`
	ToolName  string       `json:"tool_name"`
	Usage     *StreamUsage `json:"usage"`
	ToolInfo  struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"tool_info"`
}

type streamResult struct {
	ConversationID string         `json:"conversation_id"`
	Status         string         `json:"status"` // SUCCESS | ERROR | CANCELED
	Response       string         `json:"response"`
	Error          string         `json:"error"`
	NumTurns       int            `json:"num_turns"`
	Usage          StreamUsage    `json:"usage"`
	DeniedActions  []deniedAction `json:"denied_actions"`
}

// StreamUsage is agy's token count. input_tokens excludes the cached share (measured on the
// image route: input + output = total, with cache_read outside it).
type StreamUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

func (u *StreamUsage) add(o StreamUsage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.ThinkingTokens += o.ThinkingTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.TotalTokens += o.TotalTokens
}

type deniedAction struct {
	Action      string `json:"action"`
	DisplayName string `json:"display_name"`
}

// parseStreamLine decodes one stdout line. ok=false for a blank or non-JSON line (agy prints
// nothing else on stdout in this mode, but a line that does not parse must not end a turn).
func parseStreamLine(line []byte) (streamEvent, bool) {
	var ev streamEvent
	if len(strings.TrimSpace(string(line))) == 0 || json.Unmarshal(line, &ev) != nil || ev.Event == "" {
		return streamEvent{}, false
	}
	return ev, true
}

// conversationID is the id an event carries, wherever it put it.
func (ev streamEvent) conversationID() string {
	for _, id := range []string{ev.ConversationID, ev.Init.ConversationID, ev.Result.ConversationID} {
		if id != "" {
			return id
		}
	}
	return ""
}

// userLine is the stdin line for one turn. The prompt never goes on the command line, where
// every process listing on the host would show it.
func userLine(prompt string) []byte {
	b, _ := json.Marshal(map[string]any{
		"event":   "user",
		"message": map[string]any{"content": prompt},
	})
	return append(b, '\n')
}

// Denial sources.
const (
	DenialFromResult = "result" // a new entry in result.denied_actions
	DenialFromStep   = "step"   // a tool step that failed on a permission check
)

// Denial is one permission refusal inside a turn. A turn with any is still completed: agy
// tells the model and the model carries on (measured), so the caller reads this field to learn
// that the read-only preset stopped something, and the Console warns with it. There is no
// approval to give instead.
type Denial struct {
	Source string `json:"source"` // DenialFromResult | DenialFromStep
	// Action is agy's permission class from denied_actions ("command"); empty for a step.
	Action string `json:"action,omitempty"`
	// Tool is the tool's name: denied_actions' display_name, or the step's tool_name.
	Tool string `json:"tool,omitempty"`
	// Message is the step's error text; empty for a result entry.
	Message string `json:"message,omitempty"`
}

// TurnOutcome is what one finished turn reported.
type TurnOutcome struct {
	ConversationID string
	Status         string // result.status, "" when the child died without one
	Error          string // result.error
	Response       string
	// Usage is the turn's own: the sum of its agent_response steps (result.usage is
	// cumulative over the process).
	Usage            StreamUsage
	PermissionDenied []Denial
}

// permissionDeniedText reports a tool error that is a permission refusal rather than an
// ordinary failure (a missing file, an MCP server that did not answer). Only whole messages of
// the shapes agy 1.3.5 was measured to write count, never a phrase found somewhere inside one:
// an ordinary error quotes its argument, and a path can hold any words.
//
//   - the soft-deny of a tool outside the allow list: "permission check failed … user denied
//     permission …";
//   - a configured deny rule: the sentence "Matches user-configured deny rule." on its own, or
//     ending a message that opens with "permission check failed".
//
// A deny-rule refusal worded any other way is missed here; the denied_actions delta is then the
// only signal for it.
func permissionDeniedText(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	const rule = "matches user-configured deny rule."
	if m == rule {
		return true
	}
	if !strings.HasPrefix(m, "permission check failed") {
		return false
	}
	return strings.Contains(m, "user denied permission") || strings.HasSuffix(m, rule)
}

// turnTracker follows one child's turns. It lives as long as the child: denied_actions is
// cumulative over the process, so the baseline its delta is taken from is the child's, and a
// respawn starts a new tracker.
type turnTracker struct {
	// resumed: the child was started with --conversation. Whether its first result's
	// denied_actions already holds the earlier conversation's denials is unmeasured, so that
	// turn's denials come from its steps alone and its result only sets the baseline.
	resumed bool
	results int            // results seen on this child
	denied  []deniedAction // denied_actions of the previous result

	// the turn in flight
	text  strings.Builder
	usage StreamUsage
	steps []Denial
}

func newTurnTracker(resumed bool) *turnTracker { return &turnTracker{resumed: resumed} }

// begin starts a turn's accounting.
func (t *turnTracker) begin() {
	t.text.Reset()
	t.usage = StreamUsage{}
	t.steps = nil
}

// step folds one step_update into the turn.
func (t *turnTracker) step(s streamStep) {
	switch s.StepType {
	case "agent_response":
		t.text.WriteString(s.TextDelta)
		// usage arrives once, on the step's DONE (measured).
		if s.Usage != nil && s.State != "ACTIVE" {
			t.usage.add(*s.Usage)
		}
	case "tool":
		if s.State == "ERROR" && s.ToolInfo.Error != nil && permissionDeniedText(s.ToolInfo.Error.Message) {
			t.steps = append(t.steps, Denial{Source: DenialFromStep, Tool: s.ToolName, Message: s.ToolInfo.Error.Message})
		}
	}
}

// finish closes the turn on its result. The turn's denials are the OR of two independent
// signals: the steps that failed on a permission check (which see only this turn), and the
// growth of denied_actions over the previous result. The delta alone would miss a repeat: if
// agy keeps denied_actions as a set, the same command denied in two turns adds nothing the
// second time.
func (t *turnTracker) finish(r streamResult) TurnOutcome {
	out := TurnOutcome{
		ConversationID: r.ConversationID,
		Status:         r.Status,
		Error:          r.Error,
		Response:       r.Response,
		Usage:          t.usage,
	}
	if out.Response == "" {
		out.Response = strings.TrimRight(t.text.String(), "\n")
	}
	var fromResult []Denial
	if !(t.resumed && t.results == 0) {
		for _, d := range deniedDelta(t.denied, r.DeniedActions) {
			fromResult = append(fromResult, Denial{Source: DenialFromResult, Action: d.Action, Tool: d.DisplayName})
		}
	}
	out.PermissionDenied = append(fromResult, t.steps...)
	t.denied = append([]deniedAction(nil), r.DeniedActions...)
	t.results++
	t.begin()
	return out
}

// abandon drops a turn that ended without a result (the child died, or was stopped before
// answering).
func (t *turnTracker) abandon() TurnOutcome {
	out := TurnOutcome{Response: strings.TrimRight(t.text.String(), "\n"), Usage: t.usage, PermissionDenied: t.steps}
	t.begin()
	return out
}

// deniedDelta is cur minus prev as multisets: whatever cur holds more often than prev. It is
// exact when agy appends, and still finds new actions when agy keeps a set.
func deniedDelta(prev, cur []deniedAction) []deniedAction {
	seen := map[deniedAction]int{}
	for _, d := range prev {
		seen[d]++
	}
	var out []deniedAction
	for _, d := range cur {
		if seen[d] > 0 {
			seen[d]--
			continue
		}
		out = append(out, d)
	}
	return out
}
