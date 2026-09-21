package harness

// client.go implements Client against llama-server's OpenAI-compatible API, reached
// through the Control Plane's engine gateway (control-plane/engine_gateway.go). Segment D's
// own scope: streaming Send, InputTokens, and the error classification both share.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// httpClient has no timeout of its own: the bound is the caller's context. The gateway's
// own wake budget (AF_ENGINE_WAKE_TIMEOUT, 900s by default) already decides how long a
// cold start may run before IT gives up and reports failure inside the stream — a
// client-side timeout shorter than that would just replace a legible error with a bare
// "context deadline exceeded".
var httpClient = &http.Client{}

// engineErrMaxBody bounds how much of a non-streamed error body is read.
const engineErrMaxBody = 1 << 16

// engineSSEMaxLine caps one buffered SSE line, matching the gateway's own scanner
// (control-plane/engine_usage.go's engineScannerMaxLine) — a normal chunk is a few
// hundred bytes, and holding more than this would let a misbehaving upstream grow this
// process's memory one unterminated write at a time.
const engineSSEMaxLine = 1 << 20

// client implements Client against one engine connection and one model.
type client struct {
	conn  EngineConn
	model string
}

// NewClient builds a Client for one engine connection and one model id. conn normally
// comes from EngineToken(ctx, "llm", session); model is the catalogue id to send
// (llama-server's router dispatches on it).
func NewClient(conn EngineConn, model string) Client {
	return &client{conn: conn, model: model}
}

func (c *client) url(path string) string {
	return strings.TrimRight(c.conn.BaseURL, "/") + path
}

func (c *client) newRequest(ctx context.Context, path string, body any) (*http.Request, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("lcpp: encoding the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("lcpp: building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.conn.Token)
	return req, nil
}

// wireMessage is Message on the wire, in llama-server's OpenAI-compatible shape.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

func toWireMessages(msgs []Message) []wireMessage {
	out := make([]wireMessage, len(msgs))
	for i, m := range msgs {
		w := wireMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wtc := wireToolCall{ID: tc.ID, Type: "function"}
			wtc.Function.Name = tc.Name
			wtc.Function.Arguments = tc.Arguments
			w.ToolCalls = append(w.ToolCalls, wtc)
		}
		out[i] = w
	}
	return out
}

func toWireTools(tools []ToolDef) []wireToolDef {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wireToolDef, len(tools))
	for i, t := range tools {
		out[i].Type = "function"
		out[i].Function.Name = t.Name
		out[i].Function.Description = t.Description
		out[i].Function.Parameters = t.Parameters
	}
	return out
}

// chatRequest is the streaming /v1/chat/completions body. stream_options.include_usage is
// always asked for, by this client itself.
//
// Through the Control Plane it changes nothing: askForStreamUsage
// (control-plane/engine_gateway.go) adds the same flag and deliberately leaves a body that
// already carries it alone. Sent STRAIGHT at a llama-server it is the difference between
// having usage and not: nobody injects anything, and llama-server then ends the stream with
// no usage chunk at all. Measured 2026-09-21 against a LAN llama-server (b11067-932a68e06,
// gemma-4-12b-it): every streamed turn came back PromptTokens=0, which is decision 8's exact
// accounting silently reading zero rather than failing (docs/log/106 §7).
type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []wireMessage  `json:"messages"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	Tools         []wireToolDef  `json:"tools,omitempty"`
}

// streamOptions is OpenAI's `stream_options`, of which only include_usage is used here.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamChunk is one `data: …` line of an OpenAI-compatible chat.completion.chunk, wide
// enough to also read the gateway's own embedded error object (ADR 0093 decision 4) and
// llama-server's usage-bearing final chunk in the same shape.
type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []deltaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *errorObject `json:"error"`
}

// deltaToolCall is one streamed fragment of a tool call. llama-server (like every OpenAI-
// compatible streaming server) sends the call's id/name once and then streams `arguments`
// as a string split across chunks, all keyed by Index so several parallel calls can
// interleave.
type deltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// errorObject is the `error` object shape shared by both places one can appear: a
// non-streamed HTTP error body's {"error":{"code":"...","message":"..."}} (the Control
// Plane's own writeAPIErr, control-plane/httpapi.go) and a mid-stream
// {"error":{"type":"...","message":"..."}} event (writeEngineStreamErrorObject,
// control-plane/engine_gateway.go:1016) or a relayed far-gateway/upstream object of either
// shape. Code is read as json.RawMessage because llama-server's OWN error objects (a
// context-overflow 400, say) give it as a NUMBER, not the string the gateway's codes are —
// unmarshaling that into a string would simply fail and correctly leave codeStr empty.
type errorObject struct {
	Code    json.RawMessage `json:"code"`
	Type    string          `json:"type"`
	Message string          `json:"message"`
}

func (e *errorObject) engineError(status int) *EngineError {
	if e == nil {
		return nil
	}
	var codeStr string
	_ = json.Unmarshal(e.Code, &codeStr)
	kind := EngineOtherError
	switch {
	case codeStr == string(EngineWaking) || e.Type == string(EngineWaking):
		kind = EngineWaking
	case codeStr == string(EngineUnavailable) || e.Type == string(EngineUnavailable):
		kind = EngineUnavailable
	case codeStr == string(EngineOff) || e.Type == string(EngineOff):
		kind = EngineOff
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = fmt.Sprintf("the engine answered %d with no message", status)
	}
	return &EngineError{Kind: kind, Message: msg}
}

// readHTTPError classifies a non-200 response that arrived BEFORE any stream opened —
// the ordinary writeAPIErr JSON shape.
func readHTTPError(status int, body []byte) error {
	var doc struct {
		Error errorObject `json:"error"`
	}
	if json.Unmarshal(body, &doc) == nil && (doc.Error.Message != "" || len(doc.Error.Code) > 0 || doc.Error.Type != "") {
		return doc.Error.engineError(status)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	return &EngineError{Kind: EngineOtherError, Message: msg}
}

// Send implements Client.
//
// 🔴 The 200 status and headers arrive before the gateway knows whether the box will
// come up (ADR 0093 decision 4, docs/log/99 §4.11 / ADR 0079's collapsed engine_waking):
// while it waits, the gateway sends SSE comment lines (`: ...`) as a heartbeat, which
// this reader skips without treating them as content. A wake failure after that point
// arrives as a `data: {"error": …}` event, which is read as failure — Send returns nil
// only once at least one real content/tool-call/usage chunk has actually been read.
func (c *client) Send(ctx context.Context, messages []Message, tools []ToolDef) (Turn, error) {
	req, err := c.newRequest(ctx, "/chat/completions", chatRequest{
		Model: c.model, Messages: toWireMessages(messages), Stream: true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		Tools:         toWireTools(tools),
	})
	if err != nil {
		return Turn{}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Turn{}, fmt.Errorf("lcpp: reaching the engine failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, engineErrMaxBody))
		return Turn{}, readHTTPError(resp.StatusCode, body)
	}

	var (
		content, reasoning strings.Builder
		toolCalls          []deltaToolCall // index-keyed accumulation, compacted at the end
		turn               Turn
		gotAnything        bool
	)
	scanner := bufio.NewReaderSize(resp.Body, 4096)
	for {
		line, rerr := scanner.ReadBytes('\n')
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > engineSSEMaxLine {
			line = line[:engineSSEMaxLine]
		}
		if len(line) > 0 {
			if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				data = bytes.TrimSpace(data)
				if len(data) > 0 && !bytes.Equal(data, []byte("[DONE]")) {
					var chunk streamChunk
					if json.Unmarshal(data, &chunk) == nil {
						if ee := chunk.Error.engineError(http.StatusOK); ee != nil {
							return Turn{}, ee
						}
						gotAnything = true
						if chunk.Model != "" {
							turn.Model = chunk.Model
						}
						if chunk.Usage != nil {
							turn.Usage = Usage{
								PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens,
							}
						}
						for _, ch := range chunk.Choices {
							content.WriteString(ch.Delta.Content)
							reasoning.WriteString(ch.Delta.ReasoningContent)
							toolCalls = append(toolCalls, ch.Delta.ToolCalls...)
							if ch.FinishReason != "" {
								turn.Finish = FinishReason(ch.FinishReason)
							}
						}
					}
				}
			}
			// A line with neither "data:" nor ":" (a comment/heartbeat) is not SSE this
			// reader understands — including a blank event-separator line — and is
			// silently skipped, matching the gateway's own scanner (engine_usage.go).
		}
		if rerr != nil {
			break
		}
	}
	if !gotAnything {
		// The stream ended (EOF, or the connection was cut) without ever carrying a real
		// chunk or an error object — the box never came up and nothing said so. Reading
		// this as an empty success is exactly the bug ADR 0093 decision 4 calls out
		// (0079's collapsed engine_waking): the caller must be told the wake failed, not
		// handed a blank reply.
		return Turn{}, &EngineError{Kind: EngineOtherError,
			Message: "the engine's stream ended before any content arrived"}
	}
	turn.Content = content.String()
	turn.Reasoning = reasoning.String()
	turn.ToolCalls = compactToolCalls(toolCalls)
	return turn, nil
}

// compactToolCalls assembles the index-keyed streamed fragments into whole calls, in
// first-seen index order (parallel tool calls interleave by index; each index's id/name
// rides its first fragment and its arguments accumulate across every fragment sharing
// that index, the same convention every OpenAI-compatible streaming server uses).
func compactToolCalls(deltas []deltaToolCall) []ToolCall {
	if len(deltas) == 0 {
		return nil
	}
	order := []int{}
	byIndex := map[int]*ToolCall{}
	for _, d := range deltas {
		tc, ok := byIndex[d.Index]
		if !ok {
			tc = &ToolCall{}
			byIndex[d.Index] = tc
			order = append(order, d.Index)
		}
		if d.ID != "" {
			tc.ID = d.ID
		}
		if d.Function.Name != "" {
			tc.Name = d.Function.Name
		}
		tc.Arguments += d.Function.Arguments
	}
	out := make([]ToolCall, 0, len(order))
	for _, idx := range order {
		out = append(out, *byIndex[idx])
	}
	return out
}

// inputTokensRequest is decision 7's exact pre-send count call. llama-server takes the
// same body shape a chat-completions request would (messages/tools), minus `stream`.
type inputTokensRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Tools    []wireToolDef `json:"tools,omitempty"`
}

// InputTokens implements Client.
func (c *client) InputTokens(ctx context.Context, messages []Message, tools []ToolDef) (int, error) {
	req, err := c.newRequest(ctx, "/chat/completions/input_tokens", inputTokensRequest{
		Model: c.model, Messages: toWireMessages(messages), Tools: toWireTools(tools),
	})
	if err != nil {
		return 0, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("lcpp: reaching the engine failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, engineErrMaxBody))
	if err != nil {
		return 0, fmt.Errorf("lcpp: reading the input_tokens answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, readHTTPError(resp.StatusCode, body)
	}
	// Field name confirmed live against the dev deployment's engine image (ADR 0093 open
	// question 2, phase 1 report): {"input_tokens":61,"object":"response.input_tokens"} —
	// matching that same request's chat-completions prompt_tokens exactly. The other two
	// spellings are kept as a harmless fallback for a differently-built engine image
	// rather than removed, since nothing here asserts they are wrong, only unconfirmed.
	var doc struct {
		InputTokens *int `json:"input_tokens"`
		Tokens      *int `json:"tokens"`
		NTokens     *int `json:"n_tokens"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return 0, fmt.Errorf("lcpp: input_tokens answered a body this package could not read: %s", trimForError(body))
	}
	if doc.InputTokens != nil {
		return *doc.InputTokens, nil
	}
	if doc.Tokens != nil {
		return *doc.Tokens, nil
	}
	if doc.NTokens != nil {
		return *doc.NTokens, nil
	}
	return 0, errors.New("lcpp: input_tokens answered with neither \"tokens\" nor \"n_tokens\"")
}

func trimForError(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
