// Package harness is the kind-independent core ADR 0093 phase 1 builds toward a `lcpp`
// session kind: a client that talks to llama-server directly (through the Control Plane's
// engine gateway) instead of driving a vendor CLI. Nothing in this package knows what a
// session, a kind, or a Control Plane is — see the EngineToken/EngineWindow/EngineAvailable
// seams below.
//
// types.go is this package's PUBLIC SEAM: the vocabulary the four segments ADR 0093 decision
// 9 splits phase 1 into all code against, fixed here so they can be written in parallel
// without each inventing its own message/turn shape and colliding at merge time.
//
//   - Segment D (this segment): the LLM client. Client.Send streams ONE turn for a message
//     list a caller already assembled — D does no system-prompt composition, no history
//     trimming, no summarization. D parses ToolCalls out of the response but never RUNS
//     one.
//   - Segment E: the tool loop and built-in tools (read/write/edit/bash/glob/grep),
//     approval. E is what actually EXECUTES a ToolCall and turns its result into a
//     Message with Role==RoleTool. E calls Client.Send in a loop, feeding back tool
//     results as new messages, until a Turn with no ToolCalls comes back.
//   - Segment F: the MCP client. F is what SUPPLIES the []ToolDef a caller passes to
//     Client.Send/Client.InputTokens — resolving tools/list from attached MCP servers,
//     alongside whatever builtins E wants advertised. D and E accept a []ToolDef; neither
//     constructs one.
//   - Segment G: context assembly — system prompt composition, the summarization/compaction
//     judgement (decision 7's `(input_tokens + reserved output) > window * threshold`), and
//     history trimming. G is what BUILDS the []Message a call passes to Client.Send/
//     Client.InputTokens, and what reads EngineWindow to size that judgement. D does not
//     decide when to compact; it only reports the exact InputTokens count.
package harness

import (
	"context"
	"encoding/json"
)

// Role is the OpenAI-compatible chat role llama-server's /v1/chat/completions expects.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn of the conversation, in the shape every segment above D builds and
// reads.
type Message struct {
	Role    Role
	Content string
	// Reasoning is an assistant message's own chain-of-thought (llama-server's
	// reasoning_content), kept apart from Content so a caller can choose whether to
	// replay it into the next request. ADR 0093 decision 3: the display transcript keeps
	// it, the messages sent back to the engine drop it (llama-server's
	// --reasoning-preserve default would otherwise make every past turn's thinking ride
	// every future prompt).
	Reasoning string
	// ToolCalls is set on an assistant message that asked to call tools. D only parses
	// these out of the response (see Turn.ToolCalls, which is what actually carries them
	// out of Send) — this field exists so a REPLAYED assistant message (one already
	// answered, now part of history) can carry its own calls back to the engine, matching
	// what the OpenAI chat format expects for a tool-call turn's history entry.
	ToolCalls []ToolCall
	// ToolCallID pairs a Role==RoleTool message with the ToolCall.ID it answers — the
	// OpenAI convention llama-server's chat template expects for a tool result entry.
	ToolCallID string
}

// ToolCall is one function call the model asked for, as llama-server's own chat-template
// parsing extracts it from the model's output (Qwen/GLM/Llama templates differ in how the
// JSON is embedded server-side; once llama-server has parsed it, the wire shape is the
// same OpenAI `tool_calls` array for all of them, so nothing family-specific lives here).
type ToolCall struct {
	ID   string
	Name string
	// Arguments is the raw JSON arguments string, unparsed — the callee (segment E, or
	// whichever builtin/MCP tool the name resolves to) owns the schema.
	Arguments string
}

// ToolDef is one tool the model may call, in OpenAI's function-calling request shape.
// Segment F builds these from the MCP servers it discovers; segment E builds them for D's
// own builtins. D never constructs one — it only forwards the slice a caller passes.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema, passed through verbatim
}

// FinishReason is why a turn stopped generating, straight from llama-server's
// choices[0].finish_reason.
type FinishReason string

const (
	FinishStop      FinishReason = "stop"
	FinishLength    FinishReason = "length"
	FinishToolCalls FinishReason = "tool_calls"
)

// Usage is the OpenAI-compatible token accounting llama-server reports. It is always
// exact — this package never estimates a token count (ADR 0093 decision 8): a Turn with
// Usage's zero value means the engine reported none, not that nothing was spent.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Turn is one assistant reply: what Client.Send returns once the stream completes.
type Turn struct {
	Content   string
	Reasoning string
	ToolCalls []ToolCall
	Usage     Usage
	Finish    FinishReason
	// Model is the model id llama-server actually answered with (the response's own
	// "model" field), which can differ from what was requested on a router serving
	// several checkpoints.
	Model string
}

// EngineErrorKind classifies a refusal from the Control Plane's engine gateway or from
// llama-server itself, so a caller can decide whether asking again can plausibly help.
type EngineErrorKind string

const (
	// EngineWaking is retryable: the box is coming up, or a model file is still being
	// synced onto it. Ask again later.
	EngineWaking EngineErrorKind = "engine_waking"
	// EngineUnavailable is NOT retryable by waiting: most commonly, the engine has no
	// enabled model at all ("an administrator has to select one") — the gateway refuses
	// this before ever trying to wake anything, because waking would only buy a GPU box
	// to run nothing. Retrying on a timer turns a clear refusal into an infinite wait.
	EngineUnavailable EngineErrorKind = "engine_unavailable"
	// EngineOff is NOT retryable: an administrator switched the engine off.
	EngineOff EngineErrorKind = "engine_off"
	// EngineOtherError is anything else — a model llama-server itself refused (e.g. an
	// exceed_context_size_error), an unknown engine key, a bad token, or a body this
	// package could not read as an error object at all. Not retryable by this package's
	// own judgement; a caller that wants to retry a 502/504-shaped ingress failure makes
	// that call itself (this package never invents one from a plain net/http error).
	EngineOtherError EngineErrorKind = ""
)

// EngineError is a refusal this package read out of either an ordinary HTTP error body
// (a non-200 answered before the stream ever opened) or an SSE `data: {"error": …}`
// event (answered AFTER — ADR 0093 decision 4: the gateway writes its 200 and headers
// before it knows whether the box will come up, so a wake failure has to travel inside
// the stream that already started).
type EngineError struct {
	Kind    EngineErrorKind
	Message string
}

func (e *EngineError) Error() string { return e.Message }

// Retryable reports whether asking again, later, can plausibly do better.
func (e *EngineError) Retryable() bool { return e.Kind == EngineWaking }

// Client is the LLM client this package exposes upward. It is the one seam segments E and
// F/G code against.
type Client interface {
	// Send streams one turn for the given, already-assembled messages (segment G's job to
	// build) and the given, already-resolved tool definitions (segment F's job to
	// resolve; nil when no tools are offered). It does not read the first byte as success
	// — only the first REAL data chunk (ADR 0093 decision 4): the gateway's heartbeat
	// comments while the box wakes are not content, and a wake that fails after the 200
	// was already written surfaces here as an *EngineError, not as an empty success.
	Send(ctx context.Context, messages []Message, tools []ToolDef) (Turn, error)

	// InputTokens asks the engine for the exact pre-send token count of the given
	// messages (ADR 0093 decision 7's `POST /v1/chat/completions/input_tokens`) — the
	// number segment G's compaction judgement is built on. D implements the call; D does
	// not decide when to use it.
	InputTokens(ctx context.Context, messages []Message, tools []ToolDef) (int, error)
}

// EngineConn is where to send an lcpp request and how to authenticate it — built by
// main's engines.go from the Control Plane's catalogue and token minting. This package
// never learns a Control Plane exists (the same func-var seam shape as
// opencode.EngineEnv / imagegen.EngineLookup, workspace/agent/engines.go:49-52).
type EngineConn struct {
	// BaseURL is absolute and already ends at .../engine/{key}/v1 (the gateway's mount
	// point), e.g. https://<cp>/engine/llm/v1. NewClient appends /chat/completions etc.
	BaseURL string
	Token   string
}

// EngineToken mints (or reuses) a connection scoped to one engine key ("llm" for chat)
// and session-scope name ("" = workspace-scoped, matching engineSessionEnv's convention).
// ok is false when the deployment runs no engines, the Control Plane is unreachable, or
// that engine does not exist — never when the engine merely has no enabled model (that
// is a NewClient()-time property, surfaced as an EngineUnavailable EngineError on Send).
var EngineToken func(ctx context.Context, key, session string) (EngineConn, bool)

// EngineWindow returns the context window this engine actually started with —
// GET /engine/{key}/props's real n_ctx when the box has answered it recently (ADR 0093
// decision 7, phase 0), falling back to the catalogue's declared context_tokens when the
// box has not (asleep, or a Control Plane too old to carry the route — read exactly like
// engines.go's own enginePropsWindow does: any non-200 is "nothing to correct", never an
// error and never retried). 0 when neither is known (no engines at all).
var EngineWindow func(ctx context.Context, key string) int

// EngineAvailable reports whether this engine exists AND currently has at least one
// enabled model — the property chatx's lcpp provider needs to decide whether it can
// offer the kind at all (ADR 0093 phase 1 §2: this is not a CLI login check, there is no
// CLI). false covers "no engines in this deployment", "this key is not the chat role",
// and "an administrator has not enabled a model yet" alike — Send's EngineUnavailable
// error is the caller-facing detail for the last one.
var EngineAvailable func(ctx context.Context, key string) bool
