package main

// チャットのシステムプロンプトが表示言語に従うことを固定する。
//
// 論点は「outputLanguage=auto（既定）のとき何が起きるか」。以前は persona と出力ルールが
// 日本語で書かれていたので auto は中立でなく、英語 Console の利用者にも日本語で返って
// しまい、「en のときだけ表示言語を強制する」非対称なフォールバックで補正していた。
// docs/log/28 P6 で prompt 側（persona・出力ルール・引き継ぎ前置き）を両言語化したので、
// 表示言語だけで回答言語が決まるようになり、auto は両ロケールで同じ意味（入力言語に従う）に
// 戻した。強制したい利用者には 設定 > 回答言語（ja/en）があり、そちらが常に優先される。

import (
	"strings"
	"testing"
)

// writeUIPrefs（一時 HOME に ui-prefs を書く）は ui_prefs_test.go のものを共用。

func TestChatOutputRuleFollowsUILocale(t *testing.T) {
	cases := []struct {
		name       string
		prefs      string
		wantEN     bool
		wantMarker string
	}{
		{"英語ロケールは英語の出力ルール", `{"locale":"en"}`, true, "[Output rules (strict)]"},
		{"日本語ロケールは従来どおり", `{"locale":"ja"}`, false, "【出力ルール（厳守）】"},
		{"未設定は日本語", `{}`, false, "【出力ルール（厳守）】"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeUIPrefs(t, c.prefs)
			got := (&chatConversation{}).personaOf()
			if !strings.Contains(got, c.wantMarker) {
				t.Fatalf("出力ルールが %q を含まない:\n%s", c.wantMarker, got)
			}
			// 言語シグナルを割らない: 英語側に日本語の出力ルールが同居しないこと。
			if c.wantEN && strings.Contains(got, "【出力ルール（厳守）】") {
				t.Fatalf("英語プロンプトに日本語の出力ルールが混入している:\n%s", got)
			}
		})
	}
}

func TestLanguageRuleAutoFollowsInput(t *testing.T) {
	cases := []struct {
		name  string
		prefs string
		conv  chatConversation
		want  string
	}{
		// P6: persona/出力ルールが表示言語で書かれるようになったので、auto は両ロケールとも
		// 「入力言語に従う」— 言語を固定したい利用者は明示 ja/en を選ぶ。
		{"auto × 英語 Console → 入力言語に従う", `{"locale":"en"}`, chatConversation{}, ""},
		{"auto × 日本語 Console → 入力言語に従う", `{"locale":"ja"}`, chatConversation{}, ""},
		{"auto × 未設定 → 入力言語に従う", `{}`, chatConversation{}, ""},
		{"明示 ja は英語 Console でも優先", `{"locale":"en","outputLanguage":"ja"}`, chatConversation{}, langRuleJA},
		{"明示 en は日本語 Console でも優先", `{"locale":"ja","outputLanguage":"en"}`, chatConversation{}, langRuleEN},
		{"翻訳は英語 Console でも除外", `{"locale":"en"}`, chatConversation{SeedVerb: "translate"}, ""},
		{"旧 翻訳 builtin も除外", `{"locale":"en"}`, chatConversation{AssistantID: "translate"}, ""},
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
