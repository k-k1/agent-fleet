package usagex

import "testing"

func TestContextWindowGuess(t *testing.T) {
	cases := []struct {
		model string
		used  int
		want  int
	}{
		// 既定 1M。未知の将来モデルもここに落ちる（列挙漏れで 200k 誤認しない）。
		{"claude-fable-5", 0, 1_000_000},
		{"claude-opus-4-8", 0, 1_000_000},
		{"claude-opus-4-6", 0, 1_000_000},
		{"claude-sonnet-4-6", 0, 1_000_000},
		{"claude-opus-5", 0, 1_000_000},
		{"claude-sonnet-5", 0, 1_000_000},
		{"anthropic/claude-sonnet-5", 0, 1_000_000}, // opencode の provider 付き
		{"claude-opus-9", 0, 1_000_000},             // 未知の将来モデル
		// 200k 側の例外。世代番号の「4-5」を「5」と取り違えないこと。
		{"claude-opus-4-5", 0, 200_000},
		{"claude-sonnet-4-5-20250929", 0, 200_000},
		{"claude-opus-4-1", 0, 200_000},
		{"claude-opus-4-20250514", 0, 200_000}, // 日付入りID
		{"claude-3-5-sonnet-20241022", 0, 200_000},
		{"claude-3-7-sonnet", 0, 200_000},
		{"claude-haiku-4-5-20251001", 0, 200_000},
		// gpt-5.x と、素性の分からない非 Claude（実績が超えたら伸ばす）。
		{"gpt-5.1-codex", 0, 272_000},
		{"some-unknown-model", 0, 200_000},
		{"some-unknown-model", 250_000, 1_000_000},
	}
	for _, c := range cases {
		if got := WindowGuess(c.model, c.used); got != c.want {
			t.Errorf("guess(%q, %d) = %d, want %d", c.model, c.used, got, c.want)
		}
	}
}
