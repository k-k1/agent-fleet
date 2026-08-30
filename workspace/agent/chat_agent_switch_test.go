package main

// 会話の途中でバックエンド（CLI）を切り替える経路のテスト（docs/log/19）。切替そのものは
// ピン留めの差し替えだが、①新しい CLI 基準でモデルを解決し直す ②未知の履歴は次の送信で
// 再生される、の2点が守られていないと「別 CLI に他社モデル id を渡す」「新エージェントが
// 会話を知らないまま答える」という静かな壊れ方をする。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func patchConv(t *testing.T, id string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/chat/conversations/"+id, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	handleChatPatch(rec, req)
	return rec
}

// TestChatModelForResolvesPerActualBackend: 会話の Model は作成時エージェント基準の
// 1本しかないので、別バックエンドが回すターンではその CLI の設定から解決し直す。
func TestChatModelForResolvesPerActualBackend(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom","claude":"opus"}}`)
	c := &chatConversation{Agent: session.KindClaude, Model: "sonnet"}

	if got := chatModelFor(c, session.KindClaude); got != "sonnet" {
		t.Fatalf("pinned backend model = %q, want the conversation's own pin", got)
	}
	// 認証フォールバック／途中切替で codex が回すターン: claude の "sonnet" を -m に
	// 渡してはならない。
	if got := chatModelFor(c, session.KindCodex); got != "gpt-custom" {
		t.Fatalf("foreign backend model = %q, want the codex row from 設定", got)
	}
	// 自動ターン専用モデル（claude 限定）は従来どおり最優先。
	c.modelOverride = "haiku"
	if got := chatModelFor(c, session.KindClaude); got != "haiku" {
		t.Fatalf("override = %q", got)
	}
	c.modelOverride = ""
	// Agent 未設定の旧レコードは従来の素通し（kind 判定の材料が無い）。
	legacy := &chatConversation{Model: "gpt-5.6"}
	if got := chatModelFor(legacy, session.KindCodex); got != "gpt-5.6" {
		t.Fatalf("legacy conversation model = %q", got)
	}
}

// TestSwitchChatAgentRepinsAndKeepsResumeHandles: 切替は「ピン留め＋モデル」を差し替え、
// バックエンド毎の resume ハンドル／カーソルには触らない（元へ戻したとき、その CLI の
// native セッションを続きから使えるように）。
func TestSwitchChatAgentRepinsAndKeepsResumeHandles(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom"}}`)
	c := &chatConversation{
		Agent: session.KindClaude, ActiveAgent: session.KindClaude, Model: "sonnet",
		ClaudeSessionID: "claude-sess", ClaudeMessageCursor: 2,
		Messages: []chatMessage{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: "a1", Agent: session.KindClaude},
		},
	}
	switchChatAgent(c, session.KindCodex)

	if c.Agent != session.KindCodex || c.ActiveAgent != session.KindCodex {
		t.Fatalf("agent = %q / active = %q, want codex", c.Agent, c.ActiveAgent)
	}
	if c.Model != "gpt-custom" {
		t.Fatalf("model = %q, want the codex row (a carried-over claude model would break -m)", c.Model)
	}
	if c.ClaudeSessionID != "claude-sess" || c.ClaudeMessageCursor != 2 {
		t.Fatalf("claude resume handle lost: %q / %d", c.ClaudeSessionID, c.ClaudeMessageCursor)
	}
	last := c.Messages[len(c.Messages)-1]
	if last.Role != "notice" || last.NoticeKey != noticeKeyAgentSwitched || last.NoticeArgs["agent"] != session.KindCodex {
		t.Fatalf("switch notice = %+v", last)
	}
	if strings.TrimSpace(last.Content) == "" {
		t.Fatal("notice has no source-language fallback content")
	}

	// 同じエージェントの再選択は何も起こさない（notice も増えない）。
	n := len(c.Messages)
	switchChatAgent(c, session.KindCodex)
	if len(c.Messages) != n {
		t.Fatalf("re-selecting the current agent appended %d message(s)", len(c.Messages)-n)
	}
}

// TestSwitchedAgentGetsHistoryOnNextTurn: 切替後の初回送信で、新バックエンドがまだ
// 知らない履歴が再生される（認証フォールバックと同じ経路）。
func TestSwitchedAgentGetsHistoryOnNextTurn(t *testing.T) {
	c := &chatConversation{
		Agent: session.KindClaude, ClaudeSessionID: "claude-sess",
		Messages: []chatMessage{
			{Role: "user", Content: "前の依頼"},
			{Role: "assistant", Content: "前の回答", Agent: session.KindClaude},
		},
	}
	switchChatAgent(c, session.KindCodex)
	c.Messages = append(c.Messages, chatMessage{Role: "user", Content: "切替後の依頼"})

	got := syncProviderPrompt(c, session.KindCodex, "切替後の依頼", len(c.Messages)-1)
	for _, want := range []string{"前の依頼", "前の回答", "切替後の依頼"} {
		if !strings.Contains(got, want) {
			t.Fatalf("synced prompt = %q, missing %q", got, want)
		}
	}
	if strings.Count(got, "切替後の依頼") != 1 {
		t.Fatalf("current request duplicated: %q", got)
	}
}

func TestHandleChatPatchAgent(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom"}}`)
	conv := &chatConversation{
		ID: randUUID(), Slug: newConvSlug(), Title: "元のタイトル",
		Agent: session.KindClaude, Model: "sonnet",
		Messages: []chatMessage{{Role: "user", Content: "u1"}},
	}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}

	rec := patchConv(t, conv.ID, `{"agent":"codex"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	c, err := loadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent != session.KindCodex || c.Model != "gpt-custom" {
		t.Fatalf("persisted agent/model = %q / %q", c.Agent, c.Model)
	}
	if c.Title != "元のタイトル" {
		t.Fatalf("title clobbered by an agent-only patch: %q", c.Title)
	}

	// 改名は従来どおり（既存クライアントは {title} だけを送る）。
	if rec := patchConv(t, conv.ID, `{"title":"新しいタイトル"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if c, _ = loadConv(conv.ID); c.Title != "新しいタイトル" || c.Agent != session.KindCodex {
		t.Fatalf("rename result = %q / agent %q (agent must survive a title-only patch)", c.Title, c.Agent)
	}

	// headless チャットに載らない kind は拒否（tmux 専用 kind を含む）。
	for _, body := range []string{`{"agent":"kiro"}`, `{"agent":"shell"}`, `{"agent":""}`} {
		if rec := patchConv(t, conv.ID, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("patch %s status = %d, want 400", body, rec.Code)
		}
	}
	if rec := patchConv(t, conv.ID, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d, want 400", rec.Code)
	}
}

// TestHandleChatPatchAgentRejectsWhileTurnInFlight: 走っているターンの裏でピン留めを
// 差し替えると、実行中プロバイダと保存内容が食い違う。ロック待ちで数分ぶら下がるのも
// 困るので、削除と同じく 409 で先に止めてもらう。
func TestHandleChatPatchAgentRejectsWhileTurnInFlight(t *testing.T) {
	withTempHome(t)
	conv := &chatConversation{ID: randUUID(), Slug: newConvSlug(), Agent: session.KindClaude}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	deregister := registerLiveTurn(conv.ID, cancel)
	defer deregister()

	rec := patchConv(t, conv.ID, `{"agent":"codex"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != "conversation_busy" {
		t.Fatalf("error code = %q, want conversation_busy", body.Error.Code)
	}
	if c, err := loadConv(conv.ID); err != nil || c.Agent != session.KindClaude {
		t.Fatalf("agent switched under a live turn: %v / %+v", err, c)
	}
}
