package chatx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
)

// withLcppEngine wires harness's func-var seams at srv, restoring the originals (normally
// nil, this package's own zero value — no Agent build has wired them in a test binary) on
// cleanup, and returns the server for the test to configure.
func withLcppEngine(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	oldToken, oldWindow, oldAvail := harness.EngineToken, harness.EngineWindow, harness.EngineAvailable
	harness.EngineToken = func(ctx context.Context, key, session string) (harness.EngineConn, bool) {
		return harness.EngineConn{BaseURL: srv.URL, Token: "test-token"}, true
	}
	harness.EngineWindow = func(ctx context.Context, key string) int { return 32768 }
	harness.EngineAvailable = func(ctx context.Context, key string) bool { return true }
	t.Cleanup(func() {
		harness.EngineToken, harness.EngineWindow, harness.EngineAvailable = oldToken, oldWindow, oldAvail
	})
	return srv
}

func TestLcppChatSendReturnsReplyAndUsage(t *testing.T) {
	var gotBody string
	withLcppEngine(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for _, l := range []string{
			`data: {"model":"qwen-test","choices":[{"delta":{"content":"PONG"},"finish_reason":"stop"}]}` + "\n\n",
			`data: {"usage":{"prompt_tokens":10,"completion_tokens":2}}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = w.Write([]byte(l))
			fl.Flush()
		}
	})

	c := &ChatConversation{ID: "conv-1", Model: "qwen-test"}
	c.Messages = append(c.Messages, ChatMessage{Role: "user", Content: "ping"})
	reply, err := lcppChat{}.Send(context.Background(), c, "ping")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if reply != "PONG" {
		t.Errorf("reply = %q, want PONG", reply)
	}
	// setChatContext's "fresh" is the PROMPT side only (same convention as codex/cursor,
	// chat_usage.go) — the completion tokens do not ride the context-fill chip.
	if c.Context == nil || c.Context.Tokens != 10 || c.Context.Window != 32768 {
		t.Errorf("Context = %+v, want tokens 10 window 32768", c.Context)
	}
	if c.TurnModel != "qwen-test" {
		t.Errorf("TurnModel = %q, want qwen-test", c.TurnModel)
	}
	if !strings.Contains(gotBody, `"model":"qwen-test"`) {
		t.Errorf("request body = %s, want the pinned model", gotBody)
	}
}

func TestLcppChatSendRequiresAModel(t *testing.T) {
	called := false
	withLcppEngine(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	c := &ChatConversation{ID: "conv-1"} // no Model, no Agent pin to resolve one from
	_, err := lcppChat{}.Send(context.Background(), c, "hi")
	if err == nil {
		t.Fatal("Send() succeeded with no model configured, want an error")
	}
	if called {
		t.Error("Send() reached the engine despite having no model — the check must happen first")
	}
}

func TestLcppChatSendSurfacesEngineUnavailable(t *testing.T) {
	withLcppEngine(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"engine_unavailable","message":"an administrator has to select one"}}`))
	})
	c := &ChatConversation{ID: "conv-1", Model: "qwen-test"}
	c.Messages = append(c.Messages, ChatMessage{Role: "user", Content: "hi"})
	_, err := lcppChat{}.Send(context.Background(), c, "hi")
	if err == nil {
		t.Fatal("Send() succeeded, want the engine_unavailable refusal surfaced")
	}
	if !strings.Contains(err.Error(), "administrator has to select one") {
		t.Errorf("error = %v, want the engine's own message preserved", err)
	}
}

// TestLcppMessagesSkipsLastRawEntryAndDropsCards pins the history-assembly contract:
// c.Messages' own last entry (the raw, pre-injection user text chat_handlers.go appended)
// is replaced by `prompt` (which may carry InjectPendingReports/InjectCarryover's folded-in
// text), and report/notice cards are not replayed as chat history — exactly like every other
// provider, which never replay them either (they ride the next prompt).
func TestLcppMessagesSkipsLastRawEntryAndDropsCards(t *testing.T) {
	c := &ChatConversation{}
	c.Messages = []ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "first reply"},
		{Role: "report", Content: "a card", Session: "s1"},
		{Role: "user", Content: "RAW second"}, // appended by chat_handlers.go before Send
	}
	got := lcppMessages(c, "INJECTED second")
	if len(got) != 4 { // system + first + first-reply + injected-second
		t.Fatalf("messages = %+v, want 4 entries", got)
	}
	if got[0].Role != harness.RoleSystem {
		t.Errorf("messages[0].Role = %q, want system", got[0].Role)
	}
	if got[1].Role != harness.RoleUser || got[1].Content != "first" {
		t.Errorf("messages[1] = %+v, want user/first", got[1])
	}
	if got[2].Role != harness.RoleAssistant || got[2].Content != "first reply" {
		t.Errorf("messages[2] = %+v, want assistant/first reply", got[2])
	}
	last := got[len(got)-1]
	if last.Role != harness.RoleUser || last.Content != "INJECTED second" {
		t.Errorf("last message = %+v, want user/INJECTED second (not the raw entry)", last)
	}
}

func TestLcppKindWiresIntoProviderTables(t *testing.T) {
	if _, ok := ChatProviders[lcppKind]; !ok {
		t.Fatal("ChatProviders has no lcpp entry")
	}
	if got := ChatProviderKind(&ChatConversation{}, lcppChat{}); got != lcppKind {
		t.Errorf("ChatProviderKind(lcppChat{}) = %q, want %q", got, lcppKind)
	}
	for _, k := range DefaultHeadlessOrder {
		if k == lcppKind {
			t.Error("DefaultHeadlessOrder includes lcpp — it must stay opt-in only (see its own comment)")
		}
	}
}
