package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

func TestCleanSuggestedReplies(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"plain lines", "進めて\nそれでOK\nいったん待って", []string{"進めて", "それでOK", "いったん待って"}},
		{"strips numbering and bullets", "1. 進めて\n- それでOK\n・待って", []string{"進めて", "それでOK", "待って"}},
		{"keeps bare numeric/letter selectors", "1\n2\nA", []string{"1", "2", "A"}},
		{"keeps P1-style selectors", "P1\nP2", []string{"P1", "P2"}},
		{"strips list marker but keeps identifier answer", "1) 修正して\n2", []string{"修正して", "2"}},
		{"strips quotes", "「進めて」\n\"OK\"", []string{"進めて", "OK"}},
		{"dedupes case-insensitively", "OK\nok\n進めて", []string{"OK", "進めて"}},
		{"drops blanks", "\n進めて\n\n\nOK\n", []string{"進めて", "OK"}},
		{"caps at three", "a\nb\nc\nd\ne", []string{"a", "b", "c"}},
		{"drops overlong lines", strings.Repeat("x", 60) + "\nOK", []string{"OK"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cleanSuggestedReplies(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("cleanSuggestedReplies(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// 長い回答は「末尾」を残す。返信の手がかり（問いかけ・選択肢の識別子）は発言の終わりにあり、
// 件名提案と同じ先頭切りをすると、まさにそこが落ちる。
func TestReplyTailTextKeepsTail(t *testing.T) {
	long := strings.Repeat("あ", replySuggestTailRunes+50) + "どうする? A か B。"
	got := replyTailText(long)
	if !strings.HasSuffix(got, "どうする? A か B。") {
		t.Fatalf("tail lost: %q", got[max(0, len([]rune(got))-40):])
	}
	if r := []rune(got); len(r) != replySuggestTailRunes+1 { // 先頭の省略記号込み
		t.Fatalf("len = %d runes, want %d", len(r), replySuggestTailRunes+1)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("want a leading ellipsis marking the cut, got %q", string([]rune(got)[:1]))
	}
	if short := replyTailText("  OK?  "); short != "OK?" {
		t.Fatalf("short text should pass through trimmed, got %q", short)
	}
}

// 窓は直近2ターン（直前の回答＋その前のユーザー発話）。それより前は入れない。
func TestReplySuggestPromptWindow(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "古い依頼"},
		{Role: "assistant", Text: "古い回答"},
		{Role: "assistant", Text: "サイドチェーン", Sidechain: true},
		{Role: "user", Text: "直前の依頼"},
		{Role: "assistant", Text: "直前の回答。どうする?"},
	}
	got := replySuggestPrompt(turns)
	for _, want := range []string{"user: 直前の依頼", "assistant: 直前の回答。どうする?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"古い依頼", "古い回答", "サイドチェーン"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("prompt should not carry %q:\n%s", unwanted, got)
		}
	}
}
