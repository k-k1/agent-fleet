package chatx

import (
	"context"
	"errors"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/usagex"
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

func (s *stubProvider) Send(_ context.Context, _ *ChatConversation, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	return s.reply, s.err
}

func TestCompactConversation(t *testing.T) {
	withTempHome(t)
	c := &ChatConversation{
		ID: RandUUID(), Agent: "claude",
		ClaudeSessionID: "old-claude", CodexSessionID: "old-codex", OpencodeSessionID: "old-oc",
		AgyConversationID: "old-agy",
		Context:           &usagex.ContextUsage{Tokens: 170000, Window: 200000, Pct: 85},
		CtxWarned:         true,
		Messages:          []ChatMessage{{Role: "user", Content: "hi", TS: 1}},
	}
	prov := &stubProvider{reply: "要約テキスト"}
	if err := compactConversation(context.Background(), c, prov, CompactReasonManual); err != nil {
		t.Fatal(err)
	}
	if len(prov.prompts) != 1 || !strings.Contains(prov.prompts[0], providerSyncPreamble) ||
		!strings.HasSuffix(prov.prompts[0], CompactSummaryPromptFor("ja")) {
		t.Fatalf("summary prompt not sent: %+v", prov.prompts)
	}
	if c.ClaudeSessionID != "" || c.CodexSessionID != "" || c.OpencodeSessionID != "" || c.AgyConversationID != "" {
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
		c := &ChatConversation{ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old", CtxWarned: true}
		if err := compactConversation(context.Background(), c, prov, CompactReasonManual); err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if c.ClaudeSessionID != "old" || c.PendingHandoff != "" || !c.CtxWarned {
			t.Fatalf("%s: state mutated on failure: %+v", name, c)
		}
	}
}

func TestInjectHandoff(t *testing.T) {
	c := &ChatConversation{}
	if p, carried := InjectHandoff(c, "こんにちは"); carried || p != "こんにちは" {
		t.Fatalf("no-pending: (%q, %v)", p, carried)
	}
	c.PendingHandoff = "前回の要約"
	p, carried := InjectHandoff(c, "こんにちは")
	if !carried {
		t.Fatal("pending handoff not carried")
	}
	if !strings.HasPrefix(p, HandoffPreambleFor("ja")) || !strings.Contains(p, "前回の要約") || !strings.HasSuffix(p, "こんにちは") {
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
	mux.HandleFunc("POST /chat/conversations/{id}/compact", HandleChatCompact)

	// 存在しない会話 → 404。
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/chat/conversations/"+RandUUID()+"/compact", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown conv: code = %d", rr.Code)
	}

	// プロバイダセッションが無い会話 → 400 chat_nothing_to_compact（プロバイダ解決前に返る）。
	c := &ChatConversation{ID: RandUUID(), Agent: "claude", Messages: []ChatMessage{}}
	if err := SaveConv(c); err != nil {
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
	base := func(pct float64) *ChatConversation {
		return &ChatConversation{
			ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old",
			Context:  &usagex.ContextUsage{Tokens: int(pct * 2000), Window: 200000, Pct: pct},
			Messages: []ChatMessage{},
		}
	}

	// 閾値未満 → no-op（プロバイダ未呼び出し）。
	c := base(50)
	prov := &stubProvider{reply: "要約"}
	if MaybeAutoCompact(context.Background(), c, prov) || len(prov.prompts) != 0 {
		t.Fatal("compacted below the threshold")
	}

	// 閾値以上 → 圧縮（reason=auto の notice・ハンドルクリア・PendingHandoff）。
	c = base(95)
	prov = &stubProvider{reply: "自動要約"}
	if !MaybeAutoCompact(context.Background(), c, prov) {
		t.Fatal("did not compact at 95%")
	}
	if c.ClaudeSessionID != "" || c.PendingHandoff != "自動要約" {
		t.Fatalf("compaction not applied: %+v", c)
	}
	last := c.Messages[len(c.Messages)-1]
	if last.Role != "notice" || !strings.Contains(last.Content, CompactReasonAuto) {
		t.Fatalf("auto reason missing from notice: %+v", last)
	}

	// PendingHandoff が未配信のまま → 二重圧縮しない。
	c = base(95)
	c.PendingHandoff = "前回の要約"
	prov = &stubProvider{reply: "要約"}
	if MaybeAutoCompact(context.Background(), c, prov) || len(prov.prompts) != 0 {
		t.Fatal("compacted despite an undelivered handoff")
	}

	// プロバイダセッションが無い → no-op。
	c = base(95)
	c.ClaudeSessionID = ""
	if MaybeAutoCompact(context.Background(), c, &stubProvider{reply: "要約"}) {
		t.Fatal("compacted with no provider session")
	}

	// Context 未捕捉 → no-op。
	c = base(95)
	c.Context = nil
	if MaybeAutoCompact(context.Background(), c, &stubProvider{reply: "要約"}) {
		t.Fatal("compacted with no context snapshot")
	}

	// 圧縮失敗 → false・状態不変（ターン自体は続行される想定）。
	c = base(95)
	if MaybeAutoCompact(context.Background(), c, &stubProvider{err: errors.New("boom")}) {
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
	c := &ChatConversation{
		ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old",
		Context: &usagex.ContextUsage{Tokens: 190000, Window: 200000, Pct: 95},
	}
	prov := &stubProvider{reply: "要約"}
	if MaybeAutoCompact(context.Background(), c, prov) || len(prov.prompts) != 0 {
		t.Fatal("compacted though the setting is OFF")
	}
}

func TestMaybeAutoCompactAbsoluteTokenThreshold(t *testing.T) {
	withTempHome(t)
	// 1M ウィンドウで使用率 20% — 相対閾値(90%)には遠いが、絶対量が 150k を超えて
	// いるので圧縮する（費用防衛の閾値。実測でこの帯のターンは cache 書き直しだけで
	// $1 級になる）。
	c := &ChatConversation{
		ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old",
		Context:  &usagex.ContextUsage{Tokens: 200000, Window: 1000000, Pct: 20},
		Messages: []ChatMessage{},
	}
	prov := &stubProvider{reply: "自動要約"}
	if !MaybeAutoCompact(context.Background(), c, prov) {
		t.Fatal("did not compact at 200k tokens on a 1M window")
	}
	if c.PendingHandoff != "自動要約" {
		t.Fatalf("compaction not applied: %+v", c)
	}

	// 絶対閾値未満・相対閾値未満 → no-op。
	c = &ChatConversation{
		ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old",
		Context: &usagex.ContextUsage{Tokens: 120000, Window: 1000000, Pct: 12},
	}
	prov = &stubProvider{reply: "要約"}
	if MaybeAutoCompact(context.Background(), c, prov) || len(prov.prompts) != 0 {
		t.Fatal("compacted below both thresholds")
	}

	// 環境変数で絶対閾値を引き上げれば同じ 200k でも発火しない。
	t.Setenv("AF_CHAT_AUTOCOMPACT_TOKENS", "500000")
	c = &ChatConversation{
		ID: RandUUID(), Agent: "claude", ClaudeSessionID: "old",
		Context: &usagex.ContextUsage{Tokens: 200000, Window: 1000000, Pct: 20},
	}
	if MaybeAutoCompact(context.Background(), c, &stubProvider{reply: "要約"}) {
		t.Fatal("compacted despite the raised token threshold")
	}
}

func TestChatAutoCompactTokenThresholdEnvOverride(t *testing.T) {
	writeUIPrefs(t, `{}`) // HOME を隔離（設定が env より優先のため）
	if got := ChatAutoCompactTokenThreshold(); got != ChatCtxAutoCompactTokens {
		t.Fatalf("default token threshold = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOCOMPACT_TOKENS", "80000")
	if got := ChatAutoCompactTokenThreshold(); got != 80000 {
		t.Fatalf("env token threshold = %v", got)
	}
	t.Setenv("AF_CHAT_AUTOCOMPACT_TOKENS", "junk")
	if got := ChatAutoCompactTokenThreshold(); got != ChatCtxAutoCompactTokens {
		t.Fatalf("invalid env must fall back: %v", got)
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
