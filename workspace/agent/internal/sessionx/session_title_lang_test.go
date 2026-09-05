package sessionx

// A subject line is generated in the Console's display language (ui-prefs "locale").
// Pins the contract: generate in the language current at generation time and never
// regenerate when the display language changes later, i.e. read titleLang() on every
// generation and never touch an already stored title.

import (
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// writeUIPrefs (writes ui-prefs into a temporary HOME) is shared from ui_prefs_test.go.

func TestTitleLangFollowsUILocale(t *testing.T) {
	cases := []struct {
		name  string
		prefs string
		want  string
	}{
		{"english", `{"locale":"en"}`, "en"},
		{"japanese", `{"locale":"ja"}`, "ja"},
		{"unset falls back to japanese", `{}`, "ja"},
		{"unknown locale falls back to japanese", `{"locale":"fr"}`, "ja"},
		{"broken prefs fall back to japanese", `{`, "ja"},
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

// Under the English locale both the persona and the prompt must be English throughout:
// mixing in a Japanese instruction splits the language signal the model gets, the same
// reason chat.go keeps langRuleJA/EN apart.
func TestTitleSuggestPromptLanguage(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "使用量のグラフを作りたい"},
		{Role: "assistant", Text: "台帳を設計します"},
	}

	en := TitleSuggestPersona("en") + "\n" + titleSuggestPrompt(turns, "en")
	if !strings.Contains(en, "subject line") || !strings.Contains(en, "conversation log") {
		t.Fatalf("the English prompt carries no English instructions:\n%s", en)
	}
	for _, ja := range []string{"件名", "会話ログ", "悪い例"} {
		if strings.Contains(TitleSuggestInstructions("en"), ja) || strings.Contains(TitleSuggestPersona("en"), ja) {
			t.Fatalf("Japanese %q leaked into the English instructions", ja)
		}
	}
	// The conversation log body is passed through verbatim; translating it is the model's job.
	if !strings.Contains(en, "使用量のグラフを作りたい") {
		t.Fatalf("the conversation log body was dropped:\n%s", en)
	}

	jaPrompt := TitleSuggestPersona("ja") + "\n" + titleSuggestPrompt(turns, "ja")
	if !strings.Contains(jaPrompt, "件名") || strings.Contains(jaPrompt, "subject line") {
		t.Fatalf("the Japanese prompt is not in its established form:\n%s", jaPrompt)
	}
}

// The assistant chat's subject line shares the same instruction block; neither side may go
// English on its own.
func TestChatTitleSuggestPromptLanguage(t *testing.T) {
	msgs := []chatx.ChatMessage{{Role: "user", Content: "使用量のグラフを作りたい"}}
	if got := chatx.ChatTitleSuggestPrompt(msgs, "en"); !strings.Contains(got, "conversation log") {
		t.Fatalf("the English prompt for a chat subject line is not in English:\n%s", got)
	}
	if got := chatx.ChatTitleSuggestPrompt(msgs, "ja"); !strings.Contains(got, "会話ログ") {
		t.Fatalf("the Japanese prompt for a chat subject line is not in its established form:\n%s", got)
	}
}
