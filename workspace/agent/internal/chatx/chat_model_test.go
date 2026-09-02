package chatx

import (
	"context"
	"testing"

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
		if got := ResolveChatModel(tt.agent, tt.model); got != tt.want {
			t.Errorf("resolveChatModel(%q, %q) = %q, want %q", tt.agent, tt.model, got, tt.want)
		}
	}
}

func TestResolveChatModelUsesAssistantPreference(t *testing.T) {
	writeUIPrefs(t, `{"assistantModels":{"codex":"gpt-custom","opencode":""}}`)
	if got := ResolveChatModel(session.KindCodex, ""); got != "gpt-custom" {
		t.Fatalf("codex preference = %q", got)
	}
	if got := ResolveChatModel(session.KindOpencode, ""); got != "" {
		t.Fatalf("explicit CLI default = %q, want empty", got)
	}
	if got := ResolveChatModel(session.KindCodex, "explicit"); got != "explicit" {
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
		if got := ResolveChatModel(kind, ""); got != want {
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

func (p modelChatProv) Send(_ context.Context, c *ChatConversation, _ string) (string, error) {
	c.StartTurn()
	if p.model != "" {
		c.NoteTurnModel(p.model)
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
	c := &ChatConversation{Model: "sonnet"}
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

// TestTurnModelBlankWhenUnknown: a backend that neither names its model nor received
// one (codex/cursor on their own default) records nothing — the conversation's current
// setting must not stand in for it, since that is not what answered.
// TestClaudeCtxApplyRecordsTurnModel: claude's usage tracker is the single point where
// the observed model reaches both the context gauge and the turn record.
func TestClaudeCtxApplyRecordsTurnModel(t *testing.T) {
	c := &ChatConversation{Model: "sonnet"}
	tr := claudeCtx{model: "claude-sonnet-5-20260501"}
	tr.snap = ClaudeUsage{InputTokens: 100}
	tr.apply(c)
	if c.TurnModel != "claude-sonnet-5-20260501" {
		t.Fatalf("turnModel = %q", c.TurnModel)
	}
	if c.Context == nil || c.Context.Model != "claude-sonnet-5-20260501" {
		t.Fatalf("context = %+v", c.Context)
	}
}
