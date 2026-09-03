package sessionx

// CleanSuggestedTitle の回帰テスト。
// 実害: セッション sicoxqh が「会話の内容から、件名をお作りします：」というタイトルで
// 確定した（旧実装が返信の1行目をそのまま採用していたため、前置き行が件名になった）。
// ここでは「前置き・ラベル行は捨てて本物の件名を拾う」「拾えないなら空を返す（無意味な
// タイトルを latch させない）」の2点を固定する。

import "testing"

func TestCleanSuggestedTitleDropsPreamble(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"素の1行", "ミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"前置き行＋本文", "会話の内容から、件名をお作りします：\n\nミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"ラベル同一行", "セッション件名: ミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"ラベル行＋次行", "セッション件名:\nミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"半角ラベル", "Title: Session title fix", "Session title fix"},
		{"箇条書き＋強調", "件名は以下の通りです。\n- **ミラーのスキルピッカー**", "ミラーのスキルピッカー"},
		{"番号付き", "1. ミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"引用符", "「ミラーのスキルピッカー」", "ミラーのスキルピッカー"},
		{"タイトル語を含む正当な件名", "セッションタイトルの自動提案", "セッションタイトルの自動提案"},
		{"数字始まりの正当な件名", "2段クォータの実装", "2段クォータの実装"},
		{"コロンを含む正当な件名", "ログイン画面: リダイレクト不具合", "ログイン画面: リダイレクト不具合"},
		{"前置きだけ（全角コロン）", "会話の内容から、件名をお作りします：", ""},
		{"前置きだけ（句点）", "以下が件名です。", ""},
		{"ラベルだけ", "セッション件名:", ""},
		{"空", "", ""},
		{"空白のみ", "  \n\n ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanSuggestedTitle(c.reply); got != c.want {
				t.Fatalf("CleanSuggestedTitle(%q) = %q, want %q", c.reply, got, c.want)
			}
		})
	}
}

// 英語返信でも同じ保証が要る。件名 persona は日本語固定だが、会話が全編英語だと
// モデルは英語の前置きを付けてくることがあり、日本語の語尾リストでは捕まらない。
// 「コロンで終わる行は前置き」という言語非依存ルールと英語前置き語で塞ぐ。
func TestCleanSuggestedTitleEnglishPreamble(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"英語ラベル同一行", "Title: Session title auto-suggestion", "Session title auto-suggestion"},
		{"英語ラベル行＋次行", "Title:\nSession title auto-suggestion", "Session title auto-suggestion"},
		{"英語前置き＋本文", "Here is a concise title:\n\nLogin redirect bugfix", "Login redirect bugfix"},
		{"ラベル語なしのコロン終わり", "Here is a suitable session name:\nLogin redirect bugfix", "Login redirect bugfix"},
		{"英語前置きのみ（コロン無し）", "Sure, I can name this session", ""},
		{"日本語ラベル語なしのコロン終わり", "以下の通りです：\nミラーのスキルピッカー", "ミラーのスキルピッカー"},
		{"正当な英語件名", "Login redirect bugfix", "Login redirect bugfix"},
		{"title を含む正当な英語件名", "Session title auto-suggestion", "Session title auto-suggestion"},
		{"前置きだけで本文なし", "Here is a suitable session name:", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanSuggestedTitle(c.reply); got != c.want {
				t.Fatalf("CleanSuggestedTitle(%q) = %q, want %q", c.reply, got, c.want)
			}
		})
	}
}

// 長さキャップは表示桁（全角2桁）基準。日本語は従来どおり24文字で、英語は24文字で
// 途中切れせず48桁まで入ることを見る。
func TestCleanSuggestedTitleCaps(t *testing.T) {
	ja := CleanSuggestedTitle("あいうえおかきくけこさしすせそたちつてとなにぬねのはひふへほ")
	if n := len([]rune(ja)); n != 24 {
		t.Fatalf("日本語: len=%d want 24 (%q)", n, ja)
	}
	en := CleanSuggestedTitle("Session title auto-suggestion for the left pane list")
	if want := "Session title auto-suggestion for the left pane"; en != want {
		t.Fatalf("英語: got %q want %q", en, want)
	}
}
