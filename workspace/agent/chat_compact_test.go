package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if err := compactConversation(context.Background(), c, prov, compactReasonManual); err != nil {
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
		if err := compactConversation(context.Background(), c, prov, compactReasonManual); err == nil {
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

func TestMaybeAutoCompact(t *testing.T) {
	withTempHome(t)
	base := func(pct float64) *chatConversation {
		return &chatConversation{
			ID: randUUID(), Agent: "claude", ClaudeSessionID: "old",
			Context:  &contextUsage{Tokens: int(pct * 2000), Window: 200000, Pct: pct},
			Messages: []chatMessage{},
		}
	}

	// 閾値未満 → no-op（プロバイダ未呼び出し）。
	c := base(50)
	prov := &stubProvider{reply: "要約"}
	if maybeAutoCompact(context.Background(), c, prov) || len(prov.prompts) != 0 {
		t.Fatal("compacted below the threshold")
	}

	// 閾値以上 → 圧縮（reason=auto の notice・ハンドルクリア・PendingHandoff）。
	c = base(95)
	prov = &stubProvider{reply: "自動要約"}
	if !maybeAutoCompact(context.Background(), c, prov) {
		t.Fatal("did not compact at 95%")
	}
	if c.ClaudeSessionID != "" || c.PendingHandoff != "自動要約" {
		t.Fatalf("compaction not applied: %+v", c)
	}
	last := c.Messages[len(c.Messages)-1]
	if last.Role != "notice" || !strings.Contains(last.Content, compactReasonAuto) {
		t.Fatalf("auto reason missing from notice: %+v", last)
	}

	// PendingHandoff が未配信のまま → 二重圧縮しない。
	c = base(95)
	c.PendingHandoff = "前回の要約"
	prov = &stubProvider{reply: "要約"}
	if maybeAutoCompact(context.Background(), c, prov) || len(prov.prompts) != 0 {
		t.Fatal("compacted despite an undelivered handoff")
	}

	// プロバイダセッションが無い → no-op。
	c = base(95)
	c.ClaudeSessionID = ""
	if maybeAutoCompact(context.Background(), c, &stubProvider{reply: "要約"}) {
		t.Fatal("compacted with no provider session")
	}

	// Context 未捕捉 → no-op。
	c = base(95)
	c.Context = nil
	if maybeAutoCompact(context.Background(), c, &stubProvider{reply: "要約"}) {
		t.Fatal("compacted with no context snapshot")
	}

	// 圧縮失敗 → false・状態不変（ターン自体は続行される想定）。
	c = base(95)
	if maybeAutoCompact(context.Background(), c, &stubProvider{err: errors.New("boom")}) {
		t.Fatal("reported success though the summary turn failed")
	}
	if c.ClaudeSessionID != "old" || c.PendingHandoff != "" {
		t.Fatal("failed auto compact mutated state")
	}
}

func TestMaybeAutoCompactDisabledBySetting(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "agent-fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-fleet", "ui-prefs.json"),
		[]byte(`{"assistantAutoCompact": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &chatConversation{
		ID: randUUID(), Agent: "claude", ClaudeSessionID: "old",
		Context: &contextUsage{Tokens: 190000, Window: 200000, Pct: 95},
	}
	prov := &stubProvider{reply: "要約"}
	if maybeAutoCompact(context.Background(), c, prov) || len(prov.prompts) != 0 {
		t.Fatal("compacted though the setting is OFF")
	}
}

func TestChatAutoCompactThresholdEnvOverride(t *testing.T) {
	if got := chatAutoCompactThreshold(); got != chatCtxAutoCompactPct {
		t.Fatalf("default threshold = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOCOMPACT_PCT", "42.5")
	if got := chatAutoCompactThreshold(); got != 42.5 {
		t.Fatalf("env threshold = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOCOMPACT_PCT", "junk")
	if got := chatAutoCompactThreshold(); got != chatCtxAutoCompactPct {
		t.Fatalf("invalid env must fall back: %v", got)
	}
}
