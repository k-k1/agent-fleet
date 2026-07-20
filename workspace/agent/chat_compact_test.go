package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubProvider は chatProvider の記録付きスタブ。
type stubProvider struct {
	reply   string
	err     error
	prompts []string
}

func (s *stubProvider) send(_ context.Context, _ *chatConversation, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	return s.reply, s.err
}

func TestCompactConversation(t *testing.T) {
	withTempHome(t)
	c := &chatConversation{
		ID: randUUID(), Agent: "claude",
		ClaudeSessionID: "old-claude", CodexSessionID: "old-codex", OpencodeSessionID: "old-oc",
		Context:   &contextUsage{Tokens: 170000, Window: 200000, Pct: 85},
		CtxWarned: true,
		Messages:  []chatMessage{{Role: "user", Content: "hi", TS: 1}},
	}
	prov := &stubProvider{reply: "要約テキスト"}
	if err := compactConversation(context.Background(), c, prov); err != nil {
		t.Fatal(err)
	}
	if len(prov.prompts) != 1 || prov.prompts[0] != compactSummaryPrompt {
		t.Fatalf("summary prompt not sent: %+v", prov.prompts)
	}
	if c.ClaudeSessionID != "" || c.CodexSessionID != "" || c.OpencodeSessionID != "" {
		t.Fatal("resume handles not cleared")
	}
	if c.PendingHandoff != "要約テキスト" {
		t.Fatalf("PendingHandoff = %q", c.PendingHandoff)
	}
	if c.Context != nil || c.CtxWarned {
		t.Fatal("context snapshot / warn flag not reset")
	}
	last := c.Messages[len(c.Messages)-1]
	if last.Role != "notice" || !strings.Contains(last.Content, "要約テキスト") {
		t.Fatalf("compaction notice missing/incomplete: %+v", last)
	}
}

func TestCompactConversationFailuresLeaveStateIntact(t *testing.T) {
	for name, prov := range map[string]*stubProvider{
		"provider error": {err: errors.New("boom")},
		"empty summary":  {reply: "  \n"},
	} {
		c := &chatConversation{ID: randUUID(), Agent: "claude", ClaudeSessionID: "old", CtxWarned: true}
		if err := compactConversation(context.Background(), c, prov); err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if c.ClaudeSessionID != "old" || c.PendingHandoff != "" || !c.CtxWarned {
			t.Fatalf("%s: state mutated on failure: %+v", name, c)
		}
	}
}

func TestInjectHandoff(t *testing.T) {
	c := &chatConversation{}
	if p, carried := injectHandoff(c, "こんにちは"); carried || p != "こんにちは" {
		t.Fatalf("no-pending: (%q, %v)", p, carried)
	}
	c.PendingHandoff = "前回の要約"
	p, carried := injectHandoff(c, "こんにちは")
	if !carried {
		t.Fatal("pending handoff not carried")
	}
	if !strings.HasPrefix(p, handoffPreamble) || !strings.Contains(p, "前回の要約") || !strings.HasSuffix(p, "こんにちは") {
		t.Fatalf("prompt shape wrong: %q", p)
	}
	// クリアは呼び出し側の成功時責務（失敗ターンで再注入させるため）。
	if c.PendingHandoff == "" {
		t.Fatal("injectHandoff itself must not clear PendingHandoff")
	}
}

func TestHandleChatCompactGuards(t *testing.T) {
	withTempHome(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/conversations/{id}/compact", handleChatCompact)

	// 存在しない会話 → 404。
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/chat/conversations/"+randUUID()+"/compact", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown conv: code = %d", rr.Code)
	}

	// プロバイダセッションが無い会話 → 400 chat_nothing_to_compact（プロバイダ解決前に返る）。
	c := &chatConversation{ID: randUUID(), Agent: "claude", Messages: []chatMessage{}}
	if err := saveConv(c); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/chat/conversations/"+c.ID+"/compact", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), errCodeChatNothingToCompact) {
		t.Fatalf("fresh conv: code = %d body = %s", rr.Code, rr.Body.String())
	}
}
