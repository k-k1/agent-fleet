package chatx

import (
	"context"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/assistants"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestResolveChatModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// opencode の推奨は「カタログに Go モデルがあればそれ、無ければ無料の既定」。
	// HOME 隔離で鍵は消えるが、カタログはもう一つ外の世界＝稼働中の
	// `opencode serve` からも読む（docs/log/54）。開発機ではそれが鍵付きで動いている
	// ため、潰さないと期待値が Go モデルに変わる。届かないアドレスを指して、
	// この検査を「認証なし」の世界に固定する。
	t.Setenv("AF_OPENCODE_SERVE_ADDR", "http://127.0.0.1:1")
	tests := []struct {
		agent string
		model string
		want  string
	}{
		{session.KindCodex, "", defaultCodexChatModel},
		{session.KindCodex, "  gpt-5.6  ", "gpt-5.6"},
		{session.KindClaude, "", "sonnet"},
		{session.KindOpencode, "", defaultOpencodeChatModel},
		{session.KindOpencode, "  anthropic/claude-opus-4-6  ", "anthropic/claude-opus-4-6"},
		{session.KindAgy, "", defaultAgyChatModel},
		{session.KindAgy, "  Gemini 3.1 Pro (High)  ", "Gemini 3.1 Pro (High)"},
	}
	for _, tt := range tests {
		if got := resolveChatModel(tt.agent, tt.model); got != tt.want {
			t.Errorf("resolveChatModel(%q, %q) = %q, want %q", tt.agent, tt.model, got, tt.want)
		}
	}
}

func TestResolveChatModelUsesAssistantPreference(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom","opencode":""}}`)
	if got := resolveChatModel(session.KindCodex, ""); got != "gpt-custom" {
		t.Fatalf("codex preference = %q", got)
	}
	if got := resolveChatModel(session.KindOpencode, ""); got != "" {
		t.Fatalf("explicit CLI default = %q, want empty", got)
	}
	if got := resolveChatModel(session.KindCodex, "explicit"); got != "explicit" {
		t.Fatalf("conversation pin must win, got %q", got)
	}
}

func TestResolveChatModelRecommended(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"claude":"recommended","codex":"recommended","cursor":"recommended"}}`)
	tests := map[string]string{
		session.KindClaude: "sonnet",
		session.KindCodex:  defaultCodexChatModel,
		session.KindCursor: "",
	}
	for kind, want := range tests {
		if got := resolveChatModel(kind, ""); got != want {
			t.Errorf("%s recommendation = %q, want %q", kind, got, want)
		}
	}
}

func TestRecommendedCatalogModelRequiresExactEntitlement(t *testing.T) {
	const target = "opencode-go/glm-5.2"
	if got := recommendedCatalogModel([]string{"opencode/glm-5.2", target}, target, "fallback"); got != target {
		t.Fatalf("Go entitlement = %q", got)
	}
	if got := recommendedCatalogModel([]string{"opencode/glm-5.2"}, target, "fallback"); got != "fallback" {
		t.Fatalf("Zen twin must not prove Go entitlement: %q", got)
	}
}

// modelChatProv is a stub provider that records a turn model the way a real provider
// does (startTurn on entry, noteTurnModel only on success).
type modelChatProv struct {
	reply string
	model string // "" = the CLI named no model and none was passed
}

func (p modelChatProv) send(_ context.Context, c *chatConversation, _ string) (string, error) {
	c.startTurn()
	if p.model != "" {
		c.noteTurnModel(p.model)
	}
	return p.reply, nil
}

// TestTurnModelRecordedPerMessage: the assistant message keeps the model of the turn
// that produced it, so a later model/backend change cannot rewrite history.
// TestChatModelOverride pins the per-call override (自動ターン専用モデル): 立って
// いる間だけ会話のモデルより優先され、倒せば元に戻る。unexported なので永続化されない
// ことは型が保証する（JSON タグ無しの小文字フィールド）。
func TestChatModelOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &chatConversation{Model: "sonnet"}
	if got := chatModel(c); got != "sonnet" {
		t.Fatalf("base model = %q", got)
	}
	c.modelOverride = "haiku"
	if got := chatModel(c); got != "haiku" {
		t.Fatalf("override = %q", got)
	}
	c.modelOverride = ""
	if got := chatModel(c); got != "sonnet" {
		t.Fatalf("cleared override = %q", got)
	}
}

func TestTurnModelRecordedPerMessage(t *testing.T) {
	withTempHome(t)
	stubChatProvider(t, session.KindClaude, modelChatProv{reply: "了解", model: "claude-sonnet-5-20260501"})
	conv := &chatConversation{
		ID: randUUID(), Agent: session.KindClaude, Model: "sonnet",
		Tools: assistants.ToolsAFWrite, AssistantID: "operator",
	}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	if _, err := runOperatorTurn(conv.ID, "状況は?"); err != nil {
		t.Fatal(err)
	}
	c, err := loadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Messages[len(c.Messages)-1].Model; got != "claude-sonnet-5-20260501" {
		// The CLI's own id, not the "sonnet" alias we passed on the command line.
		t.Fatalf("assistant model = %q, want the model the CLI reported", got)
	}
	if c.turnModel != "" { // scratch state, never persisted
		t.Fatalf("turnModel leaked into the stored conversation: %q", c.turnModel)
	}
}

// TestTurnModelBlankWhenUnknown: a backend that neither names its model nor received
// one (codex/cursor on their own default) records nothing — the conversation's current
// setting must not stand in for it, since that is not what answered.
func TestTurnModelBlankWhenUnknown(t *testing.T) {
	withTempHome(t)
	stubChatProvider(t, session.KindClaude, modelChatProv{reply: "了解"})
	conv := &chatConversation{
		ID: randUUID(), Agent: session.KindClaude, Model: "sonnet",
		Tools: assistants.ToolsAFWrite, AssistantID: "operator",
	}
	if err := saveConv(conv); err != nil {
		t.Fatal(err)
	}
	if _, err := runOperatorTurn(conv.ID, "状況は?"); err != nil {
		t.Fatal(err)
	}
	c, err := loadConv(conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Messages[len(c.Messages)-1].Model; got != "" {
		t.Fatalf("assistant model = %q, want empty (must not guess from conversation.Model)", got)
	}
}

// TestClaudeCtxApplyRecordsTurnModel: claude's usage tracker is the single point where
// the observed model reaches both the context gauge and the turn record.
func TestClaudeCtxApplyRecordsTurnModel(t *testing.T) {
	c := &chatConversation{Model: "sonnet"}
	tr := claudeCtx{model: "claude-sonnet-5-20260501"}
	tr.snap = claudeUsage{InputTokens: 100}
	tr.apply(c)
	if c.turnModel != "claude-sonnet-5-20260501" {
		t.Fatalf("turnModel = %q", c.turnModel)
	}
	if c.Context == nil || c.Context.Model != "claude-sonnet-5-20260501" {
		t.Fatalf("context = %+v", c.Context)
	}
}
