package main

// cleanSuggestedTitle の回帰テスト。
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
			if got := cleanSuggestedTitle(c.reply); got != c.want {
				t.Fatalf("cleanSuggestedTitle(%q) = %q, want %q", c.reply, got, c.want)
			}
		})
	}
}

// 24文字キャップは既存挙動（左ペインで切れない長さ）。行走査化で落ちていないことを見る。
func TestCleanSuggestedTitleCaps(t *testing.T) {
	long := "あ𝓍いうえおかきくけこさしすせそたちつてとなにぬねのはひふへほ"
	got := []rune(cleanSuggestedTitle(long))
	if len(got) != 24 {
		t.Fatalf("len=%d want 24 (%q)", len(got), string(got))
	}
}
