package sessionx

// Regression tests for CleanSuggestedTitle.
// Real damage: session sicoxqh ended up titled "会話の内容から、件名をお作りします：" because the
// old implementation took the reply's first line verbatim, so the preamble became the title.
// Two things are pinned here: drop preamble and label lines and pick the real title, and return
// empty when there is none, so a meaningless title never latches.

import "testing"

func TestCleanSuggestedTitleDropsPreamble(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"a bare single line", "ミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"preamble line plus body", "会話の内容から、件名をお作りします：\n\nミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"label on the same line", "セッション件名: ミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"label line plus the next line", "セッション件名:\nミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"ascii label", "Title: Session title fix", "Session title fix"},
		{"bullet plus emphasis", "件名は以下の通りです。\n- **ミラーのスキルピッカー**", "ミラーのスキルピッカー"},
		{"numbered", "1. ミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"quoted", "「ミラーのスキルピッカー」", "ミラーのスキルピッカー"},
		{"a legitimate title that contains the word title", "セッションタイトルの自動提案", "セッションタイトルの自動提案"},
		{"a legitimate title starting with a digit", "2段クォータの実装", "2段クォータの実装"},
		{"a legitimate title containing a colon", "ログイン画面: リダイレクト不具合", "ログイン画面: リダイレクト不具合"},
		{"preamble only, fullwidth colon", "会話の内容から、件名をお作りします：", ""},
		{"preamble only, ending in a period", "以下が件名です。", ""},
		{"label only", "セッション件名:", ""},
		{"empty", "", ""},
		{"whitespace only", "  \n\n ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanSuggestedTitle(c.reply); got != c.want {
				t.Fatalf("CleanSuggestedTitle(%q) = %q, want %q", c.reply, got, c.want)
			}
		})
	}
}

// An English reply needs the same guarantee. The title persona is fixed to Japanese, but on an
// all-English conversation the model sometimes adds an English preamble, which the Japanese
// sentence-ending list does not catch. The language-independent rule "a line ending in a colon is
// a preamble", plus English preamble words, closes that.
func TestCleanSuggestedTitleEnglishPreamble(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"english label on the same line", "Title: Session title auto-suggestion", "Session title auto-suggestion"},
		{"english label line plus the next line", "Title:\nSession title auto-suggestion", "Session title auto-suggestion"},
		{"english preamble plus body", "Here is a concise title:\n\nLogin redirect bugfix", "Login redirect bugfix"},
		{"ends in a colon with no label word", "Here is a suitable session name:\nLogin redirect bugfix", "Login redirect bugfix"},
		{"english preamble only, no colon", "Sure, I can name this session", ""},
		{"japanese, ends in a colon with no label word", "以下の通りです：\nミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"a legitimate english title", "Login redirect bugfix", "Login redirect bugfix"},
		{"a legitimate english title containing title", "Session title auto-suggestion", "Session title auto-suggestion"},
		{"preamble only, no body", "Here is a suitable session name:", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanSuggestedTitle(c.reply); got != c.want {
				t.Fatalf("CleanSuggestedTitle(%q) = %q, want %q", c.reply, got, c.want)
			}
		})
	}
}

// The length cap counts display columns (a fullwidth character is two). Japanese still gets 24
// characters, and English is not cut off at 24 but fits up to 48 columns.
func TestCleanSuggestedTitleCaps(t *testing.T) {
	ja := CleanSuggestedTitle("あいうえおかきくけこさしすせそたちつてとなにぬねのはひふへほ")
	if n := len([]rune(ja)); n != 24 {
		t.Fatalf("japanese: len=%d want 24 (%q)", n, ja)
	}
	en := CleanSuggestedTitle("Session title auto-suggestion for the left pane list")
	if want := "Session title auto-suggestion for the left pane"; en != want {
		t.Fatalf("english: got %q want %q", en, want)
	}
}
