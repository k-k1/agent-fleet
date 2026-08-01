package main

// チャットのシステムプロンプトが表示言語に従うことを固定する。
//
// 論点は「outputLanguage=auto（既定）のとき何が起きるか」。プロンプト側（persona と
// 出力ルール）が日本語で書かれている以上 auto は中立ではなく、英語 Console の利用者にも
// 日本語で返ってしまう。ADR 0033 の軸は「誰が読む文字列か」なので、auto は表示言語へ
// フォールバックする。ただし日本語 Console では auto の従来の意味（入力言語に従う＝
// 英語で書けば英語で返る）を保つ非対称にしてある — 周りのプロンプトが既に日本語で、
// 補正が要らないため。

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

func TestLanguageRuleAutoFallsBackToUILocale(t *testing.T) {
	cases := []struct {
		name  string
		prefs string
		conv  chatConversation
		want  string
	}{
		{"auto × 英語 Console → 英語を強制", `{"locale":"en"}`, chatConversation{}, langRuleEN},
		{"auto × 日本語 Console → 入力言語に従う（従来どおり）", `{"locale":"ja"}`, chatConversation{}, ""},
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
