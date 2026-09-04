// Package bridge is the chat-bridge delivery layer (docs/log/37, ADR 0020): it
// mirrors notification-center events out to chat providers (P1: Discord,
// send-only push; Slack lands on the same abstraction next).
//
// Two halves, split on purpose:
//   - Enqueue — a plain file write into an on-disk queue. notice.Put and
//     record-exit run in short-lived hook SUBPROCESSES (session-status /
//     record-exit), so the write path must not do network I/O, must never
//     block, and must never fail the outbox (docs/log/37 contract 4: the outbox is
//     the source of truth, the chat side is a copy).
//   - StartSender — a single goroutine in the long-running Agent daemon that
//     drains the queue, formats the message, and sends through every
//     send-capable provider with bounded retries (drop + log beyond that).
package bridge

import "encoding/json"

// Caps are a provider's capability flags (docs/log/37 contract 1). P1 providers are
// send-only in practice (only Send is wired); CanReceive/CanInteract exist so
// the Console and P2's inbound routing can discriminate without a type switch
// — a future Teams provider is CanSend-only by design.
type Caps struct {
	CanSend     bool
	CanReceive  bool
	CanInteract bool
}

// Provider is one configured chat destination. Implementations are constructed
// fresh from the secrets store on every drain (no long-lived state here — the
// P2 WSS connections will own their lifecycle separately).
type Provider interface {
	Name() string
	Caps() Caps
	// Wants reports whether the user enabled this event group (EventKeys).
	Wants(eventKey string) bool
	Send(m Message) error
}

// ResumableSender is an optional Provider capability: deliver a message starting
// at a sub-message index and report how many sub-messages have been delivered so
// far, so the sender can RESUME a partial delivery across ticks WITHOUT re-posting
// what already landed (docs/log/37 §duplicate suppression = idempotent delivery).
// One notification fans into several posts (mention chunk + body chunks + P2b buttons +
// a thread starter); a failure after some succeed — a 429 that slips discordDo's inline
// retry, a dropped connection mid-batch — used to re-post the whole entry on the
// next tick and duplicate. A provider implementing this returns the count reached
// and the error; the sender persists it and calls SendFrom again with that count as
// `from`. Providers that don't implement it fall back to whole-message Send.
type ResumableSender interface {
	SendFrom(m Message, from int) (delivered int, err error)
}

// Message is the provider-independent notification payload. It carries only
// display data — never tokens, keys, or raw logs (docs/log/37 §secret exposure).
type Message struct {
	Kind        string `json:"kind"`        // notice kind or "exit"
	SessionName string `json:"sessionName"` // slug ("" for chat-scoped events)
	SessionKind string `json:"sessionKind"` // claude / codex / ...
	DisplayName string `json:"displayName"`
	Detail      string `json:"detail,omitempty"` // e.g. exit reason (oom/crashed/killed)
	CreatedAt   string `json:"createdAt"`
	// Body is the final assistant turn prose for the full-text bridge (docs/log/37
	// §future direction). Populated only for answer-ready; rendered only when the
	// provider's creds opt into full-text mode. Still display data — never tool logs,
	// thinking, or raw transcripts, and secret-scrubbed before it reaches a wire.
	Body string `json:"body,omitempty"`
	// Questions is the pending AskUserQuestion payload (claude's tool_input.questions
	// array, verbatim) for P2b button rendering (docs/log/37). Populated only for the
	// "question" kind; an interact-capable provider renders one option button per
	// choice. Nil for every other kind (plan-approval / permission-request use fixed
	// allow/deny buttons that need no payload).
	Questions json.RawMessage `json:"questions,omitempty"`
}

// EventKeys are the user-toggleable notification groups of docs/log/37 P1. The
// Connections card renders one toggle per key; an empty stored selection means
// all of them.
var EventKeys = []string{"answer-ready", "question", "permission-request", "exit", "session-report"}

// eventKeyFor maps a notice kind to its toggle group; "" means the kind is not
// bridged at all (the chat-* context housekeeping events stay Console-only —
// docs/log/37 P1 scopes the push to attention/terminal events).
func eventKeyFor(kind string) string {
	switch kind {
	case "answer-ready":
		return "answer-ready"
	case "question", "plan-approval":
		return "question"
	case "permission-request":
		return "permission-request"
	case "exit":
		return "exit"
	case "session-report":
		return "session-report"
	}
	return ""
}

// EventEnabled is the shared toggle semantics (empty selection = everything on)
// used by providers and echoed by the connections status endpoint.
func EventEnabled(selected []string, key string) bool {
	if len(selected) == 0 {
		return true
	}
	for _, s := range selected {
		if s == key {
			return true
		}
	}
	return false
}
