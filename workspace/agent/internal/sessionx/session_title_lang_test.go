package sessionx

// 件名の生成言語は Console の表示言語（ui-prefs "locale"）に従う。
// 「生成時の言語で作り、後から表示言語を切り替えても作り直さない」＝ titleLang() を
// 生成のたびに読むだけで、保存済みタイトルには一切触れない、という契約をここで固定する。

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// writeUIPrefs（一時 HOME に ui-prefs を書く）は ui_prefs_test.go のものを共用。

func TestTitleLangFollowsUILocale(t *testing.T) {
	cases := []struct {
		name  string
		prefs string
		want  string
	}{
		{"英語", `{"locale":"en"}`, "en"},
		{"日本語", `{"locale":"ja"}`, "ja"},
		{"未設定は日本語", `{}`, "ja"},
		{"未知のロケールは日本語", `{"locale":"fr"}`, "ja"},
		{"壊れた prefs は日本語", `{`, "ja"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeUIPrefs(t, c.prefs)
			if got := titleLang(); got != c.want {
				t.Fatalf("titleLang() = %q, want %q", got, c.want)
			}
		})
	}
}

// 英語ロケールでは persona もプロンプトも英語一色になること（日本語指示が混ざると
// モデルへの言語シグナルが割れる — chat.go の langRuleJA/EN と同じ理由）。
func TestTitleSuggestPromptLanguage(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "使用量のグラフを作りたい"},
		{Role: "assistant", Text: "台帳を設計します"},
	}

	en := TitleSuggestPersona("en") + "\n" + titleSuggestPrompt(turns, "en")
	if !strings.Contains(en, "subject line") || !strings.Contains(en, "conversation log") {
		t.Fatalf("英語プロンプトに英語指示が無い:\n%s", en)
	}
	for _, ja := range []string{"件名", "会話ログ", "悪い例"} {
		if strings.Contains(TitleSuggestInstructions("en"), ja) || strings.Contains(TitleSuggestPersona("en"), ja) {
			t.Fatalf("英語指示に日本語 %q が混入している", ja)
		}
	}
	// 会話ログ本文は原文のまま渡す（翻訳するのはモデルの仕事）。
	if !strings.Contains(en, "使用量のグラフを作りたい") {
		t.Fatalf("会話ログ本文が落ちている:\n%s", en)
	}

	jaPrompt := TitleSuggestPersona("ja") + "\n" + titleSuggestPrompt(turns, "ja")
	if !strings.Contains(jaPrompt, "件名") || strings.Contains(jaPrompt, "subject line") {
		t.Fatalf("日本語プロンプトが従来形でない:\n%s", jaPrompt)
	}
}

// アシスタントチャットの件名も同じ指示ブロックを共有する（片方だけ英語化しない）。
func TestChatTitleSuggestPromptLanguage(t *testing.T) {
	msgs := []chatx.ChatMessage{{Role: "user", Content: "使用量のグラフを作りたい"}}
	if got := chatx.ChatTitleSuggestPrompt(msgs, "en"); !strings.Contains(got, "conversation log") {
		t.Fatalf("チャット件名の英語プロンプトが英語でない:\n%s", got)
	}
	if got := chatx.ChatTitleSuggestPrompt(msgs, "ja"); !strings.Contains(got, "会話ログ") {
		t.Fatalf("チャット件名の日本語プロンプトが従来形でない:\n%s", got)
	}
}
