package chatx

// Pins that the chat system prompt follows the display language.
//
// The question is what outputLanguage=auto (the default) does. The persona and the output rules
// used to be written in Japanese, so auto was not neutral: users on an English Console got
// Japanese answers, corrected by an asymmetric fallback that forced the display language for en
// only. docs/log/28 P6 made the prompt side (persona, output rules, handover preamble)
// bilingual, so the display language alone decides the answer language and auto means the same
// thing in both locales (follow the input language). A user who wants it forced has
// Settings > answer language (ja/en), which always wins.

import (
	"strings"
	"testing"
)

// writeUIPrefs (writes ui-prefs into a temporary HOME) is shared with ui_prefs_test.go.

func TestChatOutputRuleFollowsUILocale(t *testing.T) {
	cases := []struct {
		name       string
		prefs      string
		wantEN     bool
		wantMarker string
	}{
		{"english locale gets the english output rules", `{"locale":"en"}`, true, "[Output rules (strict)]"},
		{"japanese locale is unchanged", `{"locale":"ja"}`, false, "【出力ルール（厳守）】"},
		{"unset falls back to japanese", `{}`, false, "【出力ルール（厳守）】"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeUIPrefs(t, c.prefs)
			got := (&ChatConversation{}).personaOf()
			if !strings.Contains(got, c.wantMarker) {
				t.Fatalf("the output rules do not contain %q:\n%s", c.wantMarker, got)
			}
			// Do not split the language signal: the English prompt must not also carry the Japanese
			// output rules.
			if c.wantEN && strings.Contains(got, "【出力ルール（厳守）】") {
				t.Fatalf("the japanese output rules leaked into the english prompt:\n%s", got)
			}
		})
	}
}

func TestLanguageRuleAutoFollowsInput(t *testing.T) {
	cases := []struct {
		name  string
		prefs string
		conv  ChatConversation
		want  string
	}{
		// P6: the persona and output rules are now written in the display language, so auto means
		// "follow the input language" in both locales; pin it with an explicit ja/en instead.
		{"auto on an english Console follows the input language", `{"locale":"en"}`, ChatConversation{}, ""},
		{"auto on a japanese Console follows the input language", `{"locale":"ja"}`, ChatConversation{}, ""},
		{"auto with no locale set follows the input language", `{}`, ChatConversation{}, ""},
		{"explicit ja wins even on an english Console", `{"locale":"en","outputLanguage":"ja"}`, ChatConversation{}, langRuleJA},
		{"explicit en wins even on a japanese Console", `{"locale":"ja","outputLanguage":"en"}`, ChatConversation{}, langRuleEN},
		{"translation is exempt even on an english Console", `{"locale":"en"}`, ChatConversation{SeedVerb: "translate"}, ""},
		{"the old translate builtin is exempt too", `{"locale":"en"}`, ChatConversation{AssistantID: "translate"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeUIPrefs(t, c.prefs)
			if got := c.conv.languageRule(); got != c.want {
				t.Fatalf("languageRule() = %q, want %q", got, c.want)
			}
		})
	}
}
