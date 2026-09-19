package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer builds a test gateway that writes 200 + headers immediately (matching the real
// gateway's own behaviour — control-plane/engine_gateway.go's streamed()), then plays back
// `lines` verbatim, one Write+Flush per entry.
func sseServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		for _, l := range lines {
			_, _ = w.Write([]byte(l))
			flusher.Flush()
		}
	}))
}

func newTestClient(srv *httptest.Server) *client {
	return &client{conn: EngineConn{BaseURL: srv.URL + "/engine/llm/v1", Token: "t"}, model: "qwen-test"}
}

// TestSendDoesNotSucceedBeforeFirstToken pins ADR 0093 decision 4: the gateway writes its
// 200 and headers before it knows whether the box will come up, and sends heartbeat comment
// lines while it waits. A wake that never produces a real chunk — the stream just ends —
// must NOT read as an empty success.
//
// This is the property the task asked to be verified as a positive control: with the
// `gotAnything` guard in Send removed, this test goes red (confirmed by hand during
// development — see the phase 1 report).
func TestSendDoesNotSucceedBeforeFirstToken(t *testing.T) {
	srv := sseServer(t, []string{
		": af-engine waking\n\n",
		": af-engine waking\n\n",
		": af-engine waking\n\n",
		// connection just ends here — no data: line ever arrives, exactly like a wake that
		// blew the gateway's own AF_ENGINE_WAKE_TIMEOUT budget and got torn down upstream
		// without even a final error event making it out.
	})
	defer srv.Close()
	c := newTestClient(srv)
	turn, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatalf("Send() succeeded with turn %+v, want an error — a 200 with only heartbeats and no content must not read as success", turn)
	}
	var ee *EngineError
	if !isEngineError(err, &ee) {
		t.Fatalf("Send() error = %v (%T), want *EngineError", err, err)
	}
	if ee.Retryable() {
		t.Errorf("an unexplained empty stream should not be reported as the retryable engine_waking kind (kind=%q)", ee.Kind)
	}
}

func isEngineError(err error, out **EngineError) bool {
	ee, ok := err.(*EngineError)
	if ok {
		*out = ee
	}
	return ok
}

func TestSendReadsHeartbeatsThenContent(t *testing.T) {
	srv := sseServer(t, []string{
		": af-engine waking\n\n",
		": af-engine waking\n\n",
		`data: {"model":"qwen-test","choices":[{"delta":{"content":"PO"}}]}` + "\n\n",
		`data: {"model":"qwen-test","choices":[{"delta":{"content":"NG"},"finish_reason":"stop"}]}` + "\n\n",
		`data: {"usage":{"prompt_tokens":12,"completion_tokens":2}}` + "\n\n",
		"data: [DONE]\n\n",
	})
	defer srv.Close()
	c := newTestClient(srv)
	turn, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if turn.Content != "PONG" {
		t.Errorf("Content = %q, want %q", turn.Content, "PONG")
	}
	if turn.Model != "qwen-test" {
		t.Errorf("Model = %q, want %q", turn.Model, "qwen-test")
	}
	if turn.Finish != FinishStop {
		t.Errorf("Finish = %q, want %q", turn.Finish, FinishStop)
	}
	if turn.Usage.PromptTokens != 12 || turn.Usage.CompletionTokens != 2 {
		t.Errorf("Usage = %+v, want {12 2}", turn.Usage)
	}
}

func TestSendSeparatesReasoningFromContent(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"reasoning_content":"let me think… "}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"reasoning_content":"ok."}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"42"},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	})
	defer srv.Close()
	c := newTestClient(srv)
	turn, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "what is 6*7"}}, nil)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if turn.Reasoning != "let me think… ok." {
		t.Errorf("Reasoning = %q, want %q", turn.Reasoning, "let me think… ok.")
	}
	if turn.Content != "42" {
		t.Errorf("Content = %q, want %q", turn.Content, "42")
	}
}

// TestSendAssemblesStreamedToolCalls pins the index-keyed accumulation every OpenAI-
// compatible streaming server uses: id/name ride the first fragment of an index, and
// arguments split across chunks concatenate in order. Two parallel calls (index 0 and 1)
// interleave to make sure they don't cross-contaminate.
func TestSendAssemblesStreamedToolCalls(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"grep","arguments":""}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"q\":\"x\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n\n",
	})
	defer srv.Close()
	c := newTestClient(srv)
	turn, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if turn.Finish != FinishToolCalls {
		t.Errorf("Finish = %q, want %q", turn.Finish, FinishToolCalls)
	}
	if len(turn.ToolCalls) != 2 {
		t.Fatalf("ToolCalls = %+v, want 2 entries", turn.ToolCalls)
	}
	if got := turn.ToolCalls[0]; got.ID != "call_1" || got.Name != "read_file" || got.Arguments != `{"path":"a.go"}` {
		t.Errorf("ToolCalls[0] = %+v, want {call_1 read_file {\"path\":\"a.go\"}}", got)
	}
	if got := turn.ToolCalls[1]; got.ID != "call_2" || got.Name != "grep" || got.Arguments != `{"q":"x"}` {
		t.Errorf("ToolCalls[1] = %+v, want {call_2 grep {\"q\":\"x\"}}", got)
	}
}

// TestSendMidStreamErrorIsNotSuccess pins ADR 0079's collapsed engine_waking: the 200 is
// already committed, so a wake failure has to travel inside the stream as a data: {"error"}
// event (writeEngineStreamError, control-plane/engine_gateway.go).
func TestSendMidStreamErrorIsNotSuccess(t *testing.T) {
	srv := sseServer(t, []string{
		": af-engine waking\n\n",
		`data: {"error":{"type":"engine_unavailable","message":"the fleet's own inference engine did not come up in time: dial tcp: timeout"}}` + "\n\n",
		"data: [DONE]\n\n",
	})
	defer srv.Close()
	c := newTestClient(srv)
	_, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	var ee *EngineError
	if !isEngineError(err, &ee) {
		t.Fatalf("Send() error = %v (%T), want *EngineError", err, err)
	}
	if ee.Kind != EngineUnavailable {
		t.Errorf("Kind = %q, want %q", ee.Kind, EngineUnavailable)
	}
	if ee.Retryable() {
		t.Error("engine_unavailable must not be reported retryable")
	}
}

// TestSendMidStreamErrorRelaysBorrowedEngineWaking is ADR 0079's borrowed-engine shape: the
// far deployment's OWN gateway refusal rides inside engineUpstreamErrorObject's relay, which
// uses "code" (a string) rather than this gateway's own "type" (writeEngineUpstreamError,
// control-plane/engine_gateway.go:1003-1012 relays the upstream object as-is).
func TestSendMidStreamErrorRelaysBorrowedEngineWaking(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"error":{"code":"engine_waking","message":"the box is still coming up"}}` + "\n\n",
		"data: [DONE]\n\n",
	})
	defer srv.Close()
	c := newTestClient(srv)
	_, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	var ee *EngineError
	if !isEngineError(err, &ee) {
		t.Fatalf("Send() error = %v (%T), want *EngineError", err, err)
	}
	if ee.Kind != EngineWaking {
		t.Errorf("Kind = %q, want %q", ee.Kind, EngineWaking)
	}
	if !ee.Retryable() {
		t.Error("engine_waking must be reported retryable")
	}
}

// TestSendMidStreamLlamaServerOwnErrorIsNotMisreadAsWaking pins the case
// writeEngineUpstreamError's own doc comment warns about: llama-server's own refusal (a
// context-overflow 400, whose "code" is the NUMBER 400, not a gateway string) must read as
// EngineOtherError, never silently as one of the three named kinds.
func TestSendMidStreamLlamaServerOwnErrorIsNotMisreadAsWaking(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"error":{"code":400,"message":"request (33565 tokens) exceeds the available context size (32768 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":33565,"n_ctx":32768}}` + "\n\n",
		"data: [DONE]\n\n",
	})
	defer srv.Close()
	c := newTestClient(srv)
	_, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	var ee *EngineError
	if !isEngineError(err, &ee) {
		t.Fatalf("Send() error = %v (%T), want *EngineError", err, err)
	}
	if ee.Kind != EngineOtherError {
		t.Errorf("Kind = %q, want %q (a numeric llama-server code must not collide with a gateway string code)", ee.Kind, EngineOtherError)
	}
	if ee.Retryable() {
		t.Error("a context-overflow refusal must not be reported retryable")
	}
	if !strings.Contains(ee.Message, "exceeds the available context size") {
		t.Errorf("Message = %q, want the upstream's own text preserved", ee.Message)
	}
}

// TestSendPreStreamErrorsAreClassified pins the three named kinds on the OTHER path a
// refusal can take: an ordinary (non-streamed) writeAPIErr JSON body, answered BEFORE any
// 200 — e.g. pendingGuard's engine_waking, an engine switched off, or an empty catalogue's
// engine_unavailable (control-plane/engine_gateway.go:511-519, :646-649, :831-834).
func TestSendPreStreamErrorsAreClassified(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		wantKind  EngineErrorKind
		wantRetry bool
	}{
		{http.StatusServiceUnavailable, `{"error":{"code":"engine_waking","message":"still syncing"}}`, EngineWaking, true},
		{http.StatusServiceUnavailable, `{"error":{"code":"engine_unavailable","message":"an administrator has to select one"}}`, EngineUnavailable, false},
		{http.StatusServiceUnavailable, `{"error":{"code":"engine_off","message":"this engine is switched off"}}`, EngineOff, false},
		{http.StatusNotFound, `{"error":{"code":"model_unknown","message":"no model x"}}`, EngineOtherError, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.wantKind)+"/"+fmt.Sprint(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := newTestClient(srv)
			_, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
			var ee *EngineError
			if !isEngineError(err, &ee) {
				t.Fatalf("Send() error = %v (%T), want *EngineError", err, err)
			}
			if ee.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", ee.Kind, tc.wantKind)
			}
			if ee.Retryable() != tc.wantRetry {
				t.Errorf("Retryable() = %v, want %v", ee.Retryable(), tc.wantRetry)
			}
		})
	}
}

// TestSendPreStream404WithPlainBodyDoesNotPanic pins what was actually seen live against a
// Control Plane older than ADR 0093 phase 0 (docs/log/99, phase 1 report): a bare Go
// "404 page not found" text body, not the JSON writeAPIErr shape. Must classify as
// EngineOtherError with the raw text preserved, never panic on the failed JSON decode.
func TestSendPreStream404WithPlainBodyDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := newTestClient(srv)
	_, err := c.Send(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	var ee *EngineError
	if !isEngineError(err, &ee) {
		t.Fatalf("Send() error = %v (%T), want *EngineError", err, err)
	}
	if ee.Kind != EngineOtherError || ee.Retryable() {
		t.Errorf("got %+v, want a non-retryable EngineOtherError", ee)
	}
	if !strings.Contains(ee.Message, "page not found") {
		t.Errorf("Message = %q, want the raw body preserved", ee.Message)
	}
}

// TestInputTokensReadsInputTokensField pins the shape confirmed live against the dev
// deployment's engine image (ADR 0093 open question 2, phase 1 report):
// {"input_tokens":61,"object":"response.input_tokens"}, matching that same request's
// chat-completions prompt_tokens exactly.
func TestInputTokensReadsInputTokensField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; !strings.HasSuffix(got, "/chat/completions/input_tokens") {
			t.Errorf("path = %q, want a /chat/completions/input_tokens suffix", got)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if body.Model != "qwen-test" || len(body.Messages) != 1 || body.Messages[0].Content != "hi" {
			t.Errorf("request body = %+v, want model qwen-test and one message", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":61,"object":"response.input_tokens"}`))
	}))
	defer srv.Close()
	c := newTestClient(srv)
	n, err := c.InputTokens(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("InputTokens() error = %v", err)
	}
	if n != 61 {
		t.Errorf("InputTokens() = %d, want 61", n)
	}
}

// TestInputTokensFallsBackToUnconfirmedFieldSpellings covers the two alternate field
// names that were guessed before the live probe (README-described, never confirmed
// against an actual engine) and are kept as a fallback rather than deleted.
func TestInputTokensFallsBackToUnconfirmedFieldSpellings(t *testing.T) {
	for _, body := range []string{`{"tokens":17}`, `{"n_tokens":17}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		n, err := newTestClient(srv).InputTokens(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
		srv.Close()
		if err != nil {
			t.Fatalf("InputTokens() error = %v (body %s)", err, body)
		}
		if n != 17 {
			t.Errorf("InputTokens() = %d, want 17 (body %s)", n, body)
		}
	}
}

func TestInputTokensSurfacesEngineError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"engine_unavailable","message":"an administrator has to select one"}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv)
	_, err := c.InputTokens(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	var ee *EngineError
	if !isEngineError(err, &ee) {
		t.Fatalf("InputTokens() error = %v (%T), want *EngineError", err, err)
	}
	if ee.Kind != EngineUnavailable {
		t.Errorf("Kind = %q, want %q", ee.Kind, EngineUnavailable)
	}
}

func TestInputTokensUnreadableBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	c := newTestClient(srv)
	if _, err := c.InputTokens(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err == nil {
		t.Fatal("InputTokens() succeeded on an unparseable body, want an error")
	}
}
