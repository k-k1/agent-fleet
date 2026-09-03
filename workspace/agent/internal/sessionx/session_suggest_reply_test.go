package sessionx

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
		// モデルは禁止しても前置きを付ける。見出し行が候補枠を1つ食い、そのままチップに出ていた。
		{"drops a header line", "ユーザーが次に送る返信の候補：\n1で案内して\n2で頼む", []string{"1で案内して", "2で頼む"}},
		{"drops an ascii header line", "Suggestions:\nOK\nWait", []string{"OK", "Wait"}},
		{"strips a label but keeps its content", "候補: 進めて\n返信：OK", []string{"進めて", "OK"}},
		{"keeps identifier answers with a colon-looking neighbor", "A\nB:", []string{"A"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CleanSuggestedReplies(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("CleanSuggestedReplies(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// 長い回答は「末尾」を残す。返信の手がかり（問いかけ・選択肢の識別子）は発言の終わりにあり、
// 件名提案と同じ先頭切りをすると、まさにそこが落ちる。切るのは行境界。
func TestReplyTailLinesKeepsTail(t *testing.T) {
	long := strings.Repeat("あ", 300) + "\n1. Aでいく\n2. Bでいく\nどうする?"
	got := replyTailLines(long, 40)
	for _, want := range []string{"1. Aでいく", "2. Bでいく", "どうする?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tail lost %q:\n%s", want, got)
		}
	}
	// 予算を食い尽くす先頭行は丸ごと落ちる（行の途中から始まる断片を渡さない）。
	if strings.Contains(got, "あ") {
		t.Fatalf("over-budget head line should be dropped whole:\n%s", got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("want a leading ellipsis marking the cut, got %q", got)
	}
	// 末尾の1行だけで予算を超えるときは、その行を字数で切ってでも残す。
	if one := replyTailLines(strings.Repeat("あ", 100)+"どうする?", 20); !strings.HasSuffix(one, "どうする?") {
		t.Fatalf("a single over-budget line must still keep its tail, got %q", one)
	}
	if short := replyTailLines("  OK?  ", 40); short != "OK?" {
		t.Fatalf("short text should pass through trimmed, got %q", short)
	}
}

// ★本命の回帰: 転写の 1 ターン＝1 コンテンツブロックなので、ツールを使う回答は「次に X します。」
// 級の途中報告が何本も並ぶ。畳まずにターン数で窓を切ると、その途中報告だけで窓が埋まり、
// 実質的な回答も依頼も 1 文字も渡らない（実測: 会話ログが 22 文字になり候補が「進めて」だけ）。
func TestReplySuggestPromptFoldsFragments(t *testing.T) {
	turns := []transcript.Turn{
		{Role: "user", Text: "古い依頼"},
		{Role: "assistant", Text: "古い回答"},
		{Role: "user", Text: "L19 と L37 を直したい"},
		{Role: "assistant", Text: "調べる。"},
		{Role: "assistant", Text: "サイドチェーン", Sidechain: true},
		{Role: "assistant", Text: "1. L19 を削る\n2. L37 を削る\nこれで進めてよいですか。"},
		{Role: "user", Text: "1"},
		{Role: "assistant", Text: "2点をトリムします。"},
		{Role: "assistant", Text: "txt を再生成します。"},
	}
	got := ReplySuggestPrompt(turns, "ja")
	// 途中報告は畳まれて 1 発言になり、その手前の「選択肢つきの回答」と「短い返事」まで届く。
	for _, want := range []string{
		"assistant: 2点をトリムします。\ntxt を再生成します。",
		"user: 1",
		"1. L19 を削る",
		"これで進めてよいですか。",
		"user: L19 と L37 を直したい",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "サイドチェーン") {
		t.Fatalf("sidechain turns must stay out:\n%s", got)
	}
}

// 予算窓: 長い発言が続けば早く打ち切られ、短い返事はほぼコストゼロで通過する。
func TestReplyFoldWindowBudget(t *testing.T) {
	long := func(n int) string { return strings.Repeat("あ", n) }
	msgs := []ReplyMsg{
		{"user", "最初の依頼"},
		{"assistant", long(800)},
		{"user", "つぎ"},
		{"assistant", long(800)},
	}
	got := replyFoldWindow(msgs)
	if len(got) != 3 { // 800(切詰) + "つぎ" + 800(切詰) で予算超過 → そこで停止
		t.Fatalf("window = %d msgs, want 3: %+v", len(got), got)
	}
	if got[0].Role != "assistant" || got[len(got)-1].Role != "assistant" {
		t.Fatalf("window should end at the newest message: %+v", got)
	}
	// 発言数の上限も効く（短い発言ばかりでも遡りすぎない）。
	many := make([]ReplyMsg, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, ReplyMsg{"user", "あ"}, ReplyMsg{"assistant", "い"})
	}
	if n := len(replyFoldWindow(many)); n != replySuggestMaxMsgs {
		t.Fatalf("window = %d msgs, want the %d cap", n, replySuggestMaxMsgs)
	}
}
