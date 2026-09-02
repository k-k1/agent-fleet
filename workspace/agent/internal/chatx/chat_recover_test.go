package chatx

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIsContextOverflowErr(t *testing.T) {
	overflow := []string{
		"claude returned an error: Prompt is too long · the request is ~253544 tokens (limit 200000)",
		"codex execution failed: Input exceeds the maximum length of 1048576 characters. (code -32602)",
		"error: input_too_large",
		"This model's maximum context length is 272000 tokens",
		"context_length_exceeded",
	}
	for _, m := range overflow {
		if !isContextOverflowErr(errors.New(m)) {
			t.Fatalf("should detect overflow: %q", m)
		}
	}
	notOverflow := []string{
		"claude execution failed: exit status 1",
		"failed to parse claude response: unexpected end of JSON",
		"no response from codex",
		"context deadline exceeded", // Go の ctx タイムアウト — 超過エラーではない
	}
	for _, m := range notOverflow {
		if isContextOverflowErr(errors.New(m)) {
			t.Fatalf("should NOT detect overflow: %q", m)
		}
	}
	if isContextOverflowErr(nil) {
		t.Fatal("nil must be false")
	}
}

func TestRecoverForRetryNonOverflowIsNoop(t *testing.T) {
	c := &chatConversation{ID: randUUID(), Agent: "claude", ClaudeSessionID: "old"}
	prov := &stubProvider{reply: "要約"} // 呼ばれないはず
	if recoverForRetry(context.Background(), c, prov, errors.New("some network blip")) {
		t.Fatal("non-overflow error triggered recovery")
	}
	if len(prov.prompts) != 0 {
		t.Fatal("provider called for a non-overflow error")
	}
	if c.ClaudeSessionID != "old" {
		t.Fatal("session handle mutated on a non-overflow error")
	}
}

func TestRecoverForRetryCompactsOnOverflow(t *testing.T) {
	withTempHome(t)
	c := &chatConversation{ID: randUUID(), Agent: "claude", ClaudeSessionID: "old", CtxWarned: true}
	prov := &stubProvider{reply: "引き継ぎ要約"}
	overflow := errors.New("claude returned an error: Prompt is too long")
	if !recoverForRetry(context.Background(), c, prov, overflow) {
		t.Fatal("overflow did not trigger recovery")
	}
	// compactConversation が走った証跡: ハンドルクリア＋PendingHandoff。
	if c.ClaudeSessionID != "" || c.PendingHandoff != "引き継ぎ要約" {
		t.Fatalf("compaction not applied: session=%q handoff=%q", c.ClaudeSessionID, c.PendingHandoff)
	}
}

func TestRecoverForRetryFailsWhenSummaryAlsoOverflows(t *testing.T) {
	c := &chatConversation{ID: randUUID(), Agent: "claude", ClaudeSessionID: "old"}
	// 既にウィンドウ超過 → 要約ターン自体も失敗するケース。
	prov := &stubProvider{err: errors.New("Prompt is too long")}
	if recoverForRetry(context.Background(), c, prov, errors.New("Prompt is too long")) {
		t.Fatal("recovery reported success though the summary turn failed")
	}
	if c.PendingHandoff != "" || c.ClaudeSessionID != "old" {
		t.Fatal("failed compaction must not mutate state")
	}
}

func TestNoteContextOverflow(t *testing.T) {
	withTempHome(t)
	c := &chatConversation{ID: randUUID(), Messages: []chatMessage{}}
	noteContextOverflow(c)
	if len(c.Messages) != 1 || c.Messages[0].Role != "notice" {
		t.Fatalf("no notice appended: %+v", c.Messages)
	}
	if !strings.Contains(c.Messages[0].Content, "上限を超えた") {
		t.Fatalf("notice content unexpected: %q", c.Messages[0].Content)
	}
}
