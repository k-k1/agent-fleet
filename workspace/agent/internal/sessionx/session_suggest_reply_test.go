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
		// The model prepends a preamble even when told not to, and the header line ate one of the
		// three slots and reached the chips as-is.
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

// A long answer keeps its tail. The cues for a reply (the question, the option identifiers) sit
// at the end of a message, so cutting from the head the way the title suggestion does drops
// exactly those. The cut is made on a line boundary.
func TestReplyTailLinesKeepsTail(t *testing.T) {
	long := strings.Repeat("あ", 300) + "\n1. Aでいく\n2. Bでいく\nどうする?"
	got := replyTailLines(long, 40)
	for _, want := range []string{"1. Aでいく", "2. Bでいく", "どうする?"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tail lost %q:\n%s", want, got)
		}
	}
	// A head line that would eat the whole budget is dropped whole: never hand over a fragment
	// that starts in the middle of a line.
	if strings.Contains(got, "あ") {
		t.Fatalf("over-budget head line should be dropped whole:\n%s", got)
	}
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("want a leading ellipsis marking the cut, got %q", got)
	}
	// When the last line alone goes over the budget, keep it anyway by cutting it at a character
	// count.
	if one := replyTailLines(strings.Repeat("あ", 100)+"どうする?", 20); !strings.HasSuffix(one, "どうする?") {
		t.Fatalf("a single over-budget line must still keep its tail, got %q", one)
	}
	if short := replyTailLines("  OK?  ", 40); short != "OK?" {
		t.Fatalf("short text should pass through trimmed, got %q", short)
	}
}

// The regression this guards: one transcript turn is one content block, so an answer that uses
// tools lines up several interim notes of the "next I will do X" kind. Windowing by turn count
// without folding fills the window with those notes alone, and not one character of the real
// answer or of the request gets through (measured: the conversation log came to 22 characters
// and the only suggestion left was a bare "go ahead").
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
	// The interim notes fold into one message, so the answer carrying the options and the short
	// reply before it both make it in.
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

// The budget window: a run of long messages cuts it off early, while short replies pass at
// almost no cost.
func TestReplyFoldWindowBudget(t *testing.T) {
	long := func(n int) string { return strings.Repeat("あ", n) }
	msgs := []ReplyMsg{
		{"user", "最初の依頼"},
		{"assistant", long(800)},
		{"user", "つぎ"},
		{"assistant", long(800)},
	}
	got := replyFoldWindow(msgs)
	if len(got) != 3 { // 800 (truncated) + the short turn + 800 (truncated) goes over budget, so it stops there
		t.Fatalf("window = %d msgs, want 3: %+v", len(got), got)
	}
	if got[0].Role != "assistant" || got[len(got)-1].Role != "assistant" {
		t.Fatalf("window should end at the newest message: %+v", got)
	}
	// The message-count cap applies too: even with nothing but short messages it does not walk
	// back too far.
	many := make([]ReplyMsg, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, ReplyMsg{"user", "あ"}, ReplyMsg{"assistant", "い"})
	}
	if n := len(replyFoldWindow(many)); n != replySuggestMaxMsgs {
		t.Fatalf("window = %d msgs, want the %d cap", n, replySuggestMaxMsgs)
	}
}
