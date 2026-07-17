package main

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Operator-injected prompts (docs/30 ②) must tag ONLY the matching user turns, leaving the
// user's own prompts and assistant turns untouched — even when an assistant happens to echo
// the injected text.
func TestOperatorInjectionTagging(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	recordOperatorInjection("slot01", "  リファクタして  ") // trimmed on store
	recordOperatorInjection("slot01", "テストも直して")
	recordOperatorInjection("slot01", "リファクタして") // dup of the first (post-trim) — no growth

	if got := operatorInjections("slot01"); len(got) != 2 {
		t.Fatalf("store should hold 2 distinct texts, got %d: %v", len(got), got)
	}

	turns := []transcript.Turn{
		{Role: "user", Text: "リファクタして"},        // operator (matches, trimmed)
		{Role: "user", Text: "自分で打った質問"},       // user's own — untagged
		{Role: "assistant", Text: "テストも直して"},    // assistant echo — never tagged (role guard)
		{Role: "user", Text: "  テストも直して  "},    // operator (matches after trim)
	}
	tagOperatorTurns("slot01", turns)

	want := []string{turnSourceOperator, "", "", turnSourceOperator}
	for i, w := range want {
		if turns[i].Source != w {
			t.Errorf("turn %d (%q %q): Source = %q, want %q", i, turns[i].Role, turns[i].Text, turns[i].Source, w)
		}
	}
}

// A session with no operator injections is a cheap no-op — nothing gets tagged.
func TestOperatorInjectionTaggingEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	turns := []transcript.Turn{{Role: "user", Text: "ふつうの入力"}}
	tagOperatorTurns("slot02", turns)
	if turns[0].Source != "" {
		t.Errorf("no injections recorded, want empty Source, got %q", turns[0].Source)
	}
}

// The store stays bounded: past the cap, only the newest maxOperatorInjections survive.
func TestOperatorInjectionCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < maxOperatorInjections+20; i++ {
		recordOperatorInjection("slot03", string(rune('A'+i%26))+"-prompt-"+itoa(i))
	}
	if got := len(operatorInjections("slot03")); got != maxOperatorInjections {
		t.Fatalf("store not capped: len = %d, want %d", got, maxOperatorInjections)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
