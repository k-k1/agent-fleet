package main

import "testing"

func TestEnglishToKana(t *testing.T) {
	// 代表語の実出力をログしつつ、辞書ロード・変換が動くことを確認する。
	words := []string{
		"hello", "world", "cat", "dog", "coffee", "google", "python",
		"apple", "computer", "banana", "music", "language", "voice",
		"the", "through", "water", "orange", "session", "agent",
	}
	for _, w := range words {
		got := englishToKana(w)
		if got == "" || got == w {
			t.Errorf("%q -> %q (変換されていない)", w, got)
		}
		t.Logf("%-12s -> %s", w, got)
	}
}

func TestEnglishToKanaMixed(t *testing.T) {
	// 日英混在: 日本語・記号・数字はそのまま、英単語のみ変換。
	got := englishToKana("これは pen です。 iPhone を3個。")
	t.Logf("mixed -> %s", got)
	if !containsJP(got, "これは") || !containsJP(got, "です") {
		t.Errorf("日本語が壊れた: %q", got)
	}
	if containsASCIILetters(got) {
		t.Errorf("英字が残っている: %q", got)
	}
}

func TestAcronymAndCamel(t *testing.T) {
	cases := map[string]string{
		"AWS":         "acronym",
		"API":         "acronym",
		"getUserById": "camel",
	}
	for w := range cases {
		got := englishToKana(w)
		t.Logf("%-12s -> %s", w, got)
		if got == w || got == "" {
			t.Errorf("%q が変換されていない: %q", w, got)
		}
		if containsASCIILetters(got) {
			t.Errorf("%q に英字が残っている: %q", w, got)
		}
	}
}

func TestTechTerms(t *testing.T) {
	// AWS/開発ジャルゴンのオーバーライド＋略語＋数字ルールの網羅確認。
	want := map[string]string{
		"EC2":        "イーシーツー",
		"S3":         "エススリー",
		"Dao":        "ダオ",
		"nginx":      "エンジンエックス",
		"json":       "ジェイソン",
		"IAM":        "アイアム",
		"lambda":     "ラムダ",
		"DynamoDB":   "ダイナモディービー",
		"kubernetes": "クーバネティス",
		"IPv6":       "アイピーブイシックス",
	}
	for in, exp := range want {
		if got := englishToKana(in); got != exp {
			t.Errorf("%q -> %q, want %q", in, got, exp)
		}
	}
	// 単独の数字は日本語読みのまま（英字と地続きの数字だけ英語読み）。
	if got := englishToKana("3個"); got != "3個" {
		t.Errorf("単独数字 3個 -> %q, want 3個", got)
	}
}

func containsASCIILetters(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

func containsJP(s, sub string) bool {
	return len(sub) > 0 && (len(s) >= len(sub)) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
