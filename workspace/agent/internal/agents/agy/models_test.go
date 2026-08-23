package agy

import (
	"strings"
	"testing"
)

func TestParseModels(t *testing.T) {
	out := []byte("Gemini 3.5 Flash (Medium)\nGemini 3.1 Pro (High)\n\nClaude Sonnet 4.6 (Thinking)\n")
	list := parseModels(out)
	if len(list) != 3 {
		t.Fatalf("got %d models: %+v", len(list), list)
	}
	if list[0].ID != "Gemini 3.5 Flash (Medium)" || list[0].Label != list[0].ID {
		t.Fatalf("id/label mismatch: %+v", list[0])
	}
}

func TestParseModelsSkipsSignInNoise(t *testing.T) {
	if got := parseModels([]byte("Error: Please sign in to use Antigravity\n")); got != nil {
		t.Fatalf("sign-in error leaked into catalog: %+v", got)
	}
}

// 1.1.19 の 2 カラム形式。実機で `agy models | cat -A` を読んで確かめた区切りは TAB。
// ⚠️ 行ごと --model に渡していたので、CLI は「そんなモデルは知らない」と言って既定へ
// フォールバックしていた——**セッションは起動し、動き、黙って別のモデルだった**。
func TestParseModelsTwoColumn(t *testing.T) {
	out := []byte("gemini-3.7-flash-high\tGemini 3.7 Flash (High)\n" +
		"gemini-3.5-flash-low\tGemini 3.5 Flash (Low)\n" +
		"claude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n")
	list := parseModels(out)
	if len(list) != 3 {
		t.Fatalf("got %d models: %+v", len(list), list)
	}
	if list[1].ID != "gemini-3.5-flash-low" {
		t.Fatalf("the id must be the first column, not the whole line: %q", list[1].ID)
	}
	if list[1].Label != "Gemini 3.5 Flash (Low)" {
		t.Fatalf("the label must be the second column: %q", list[1].Label)
	}
	for _, m := range list {
		if strings.ContainsAny(m.ID, " \t") {
			t.Fatalf("an id with whitespace is the bug this test exists for: %q", m.ID)
		}
	}
}

// 旧形式（表示名だけ・それがそのまま --model に通る）は死んだコードではない。
// ~/.local は永続 home なので、古いイメージで boot-install した Workspace には
// REPIN が前進させるまで 1.1.17 が残る。ピンを 1.1.19 に上げても両方が現場にいる。
func TestParseModelsKeepsTheOldSingleColumnForm(t *testing.T) {
	list := parseModels([]byte("Gemini 3.5 Flash (Medium)\nClaude Sonnet 4.6 (Thinking)\n"))
	if len(list) != 2 || list[0].ID != "Gemini 3.5 Flash (Medium)" || list[0].Label != list[0].ID {
		t.Fatalf("the pinned version's form stopped working: %+v", list)
	}
}
