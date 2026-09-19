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

// TestLcppMessagesOrdinaryTurnReplacesRawWithInjectedPrompt is case (a): the ordinary
// user-turn path (chat_handlers.go:452,536), which appends this turn's raw user text to
// c.Messages right before calling Send. Because `prompt` here CONTAINS that raw text
// (InjectPendingReports/InjectCarryover fold their preamble in front of it, verbatim —
// exactly what a real prompt looks like), the raw entry is dropped and replaced by the
// (possibly-injected) prompt, so the turn is not sent twice. report/notice cards are not
// replayed as chat history either — no other provider replays them (they ride the next
// prompt via InjectPendingReports).
func TestLcppMessagesOrdinaryTurnReplacesRawWithInjectedPrompt(t *testing.T) {
	c := &ChatConversation{}
	c.Messages = []ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "first reply"},
		{Role: "report", Content: "a card", Session: "s1"},
		{Role: "user", Content: "second"}, // appended by chat_handlers.go before Send
	}
	got := lcppMessages(c, "【利用者からのメッセージ】\nsecond") // InjectPendingReports' own shape: preamble + raw text
	if len(got) != 4 {                              // system + first + first-reply + injected-second
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
	if last.Role != harness.RoleUser || last.Content != "【利用者からのメッセージ】\nsecond" {
		t.Errorf("last message = %+v, want the injected prompt (not the bare raw entry)", last)
	}
}

// TestLcppMessagesCompactionDropsNothing is case (b), the blocker srx5cky flagged: a
// compaction call (chat_compact.go:127) or a report auto-turn (chat_report.go:609) build
// `prompt` from scratch (CompactPrompt/reportsPrompt) — it has nothing to do with
// c.Messages' last entry, which is an ordinary past turn. The old unconditional
// `hist[:n-1]` silently erased exactly the turn being summarized/reported on; nothing may
// be dropped here.
func TestLcppMessagesCompactionDropsNothing(t *testing.T) {
	c := &ChatConversation{}
	c.Messages = []ChatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "first reply"},
		{Role: "user", Content: "second, about to be summarized"},
	}
	got := lcppMessages(c, "Summarize the conversation so far.") // CompactPrompt's own shape — unrelated to c.Messages
	if len(got) != 5 {                                           // system + first + first-reply + second + the compact instruction
		t.Fatalf("messages = %+v, want 5 entries (nothing dropped)", got)
	}
	if got[3].Role != harness.RoleUser || got[3].Content != "second, about to be summarized" {
		t.Errorf("messages[3] = %+v, want the last real turn preserved", got[3])
	}
	last := got[len(got)-1]
	if last.Content != "Summarize the conversation so far." {
		t.Errorf("last message = %+v, want the compact instruction appended on top", last)
	}
}

// TestLcppMessagesEmptyConversation is case (c): a throwaway conversation with no stored
// history at all (HandleChatAsk's ephemeral c), which must produce just system + prompt.
func TestLcppMessagesEmptyConversation(t *testing.T) {
	c := &ChatConversation{}
	got := lcppMessages(c, "one-shot question")
	if len(got) != 2 {
		t.Fatalf("messages = %+v, want 2 entries (system + prompt)", got)
	}
	if got[0].Role != harness.RoleSystem {
		t.Errorf("messages[0].Role = %q, want system", got[0].Role)
	}
	if got[1].Role != harness.RoleUser || got[1].Content != "one-shot question" {
		t.Errorf("messages[1] = %+v, want user/one-shot question", got[1])
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
