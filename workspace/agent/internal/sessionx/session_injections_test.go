package sessionx

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// Operator-injected prompts (docs/log/30 ②) must tag ONLY the matching user turns, leaving the
// user's own prompts and assistant turns untouched — even when an assistant happens to echo
// the injected text.
func TestOperatorInjectionTagging(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	recordOperatorInjection("slot01", "  リファクタして  ") // trimmed on store
	recordOperatorInjection("slot01", "テストも直して")
	recordOperatorInjection("slot01", "リファクタして")            // dup of the first (post-trim) — no growth
	recordInjection("slot01", "スマホから返信", TurnSourceDiscord) // chat-bridge origin (docs/log/37 P2a)

	if got := operatorInjections("slot01"); len(got) != 3 {
		t.Fatalf("store should hold 3 distinct texts, got %d: %v", len(got), got)
	}

	turns := []transcript.Turn{
		{Role: "user", Text: "リファクタして"},      // operator (matches, trimmed)
		{Role: "user", Text: "自分で打った質問"},     // user's own — untagged
		{Role: "assistant", Text: "テストも直して"}, // assistant echo — never tagged (role guard)
		{Role: "user", Text: "  テストも直して  "},  // operator (matches after trim)
		{Role: "user", Text: "スマホから返信"},      // chat bridge → "discord"
	}
	tagInjectedTurns("slot01", turns)

	want := []string{TurnSourceOperator, "", "", TurnSourceOperator, TurnSourceDiscord}
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
	tagInjectedTurns("slot02", turns)
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

// Schedule-driven injections (docs/log/38) tag with their own origins — and a slash-command
// injection must tag its <command-*> tag-block turn (either tag order), since the
// transcript never contains the raw "/scout" text that was recorded.
func TestScheduleInjectionTaggingCommandForm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	recordInjection("slot04", "/scout", TurnSourceSchedule)
	recordInjection("slot04", "/review 今日の差分", TurnSourceScheduleManual)

	turns := []transcript.Turn{
		// Skill invocation (2.1.215 実測): <command-message> FIRST.
		{Role: "user", Text: "<command-message>scout</command-message>\n<command-name>/scout</command-name>"},
		// Built-in style: <command-name> first, args carried separately.
		{Role: "user", Text: "<command-name>/review</command-name><command-message>review</command-message><command-args>今日の差分</command-args>"},
		// Prose merely quoting the tag must NOT match (leading-tag guard).
		{Role: "user", Text: "この <command-name>/scout</command-name> という記録について教えて"},
		{Role: "user", Text: "/scout"}, // raw text form still matches directly
	}
	tagInjectedTurns("slot04", turns)

	want := []string{TurnSourceSchedule, TurnSourceScheduleManual, "", TurnSourceSchedule}
	for i, w := range want {
		if turns[i].Source != w {
			t.Errorf("turn %d (%q): Source = %q, want %q", i, turns[i].Text, turns[i].Source, w)
		}
	}
}

// scheduleInjectionSource is the report_to-independent half of the whitelist: ONLY a
// schedule origin passes, so it can gate "remember this even without report_to" without
// turning plain Console input into an operator badge.
func TestScheduleInjectionSource(t *testing.T) {
	for in, want := range map[string]string{
		"schedule":        TurnSourceSchedule,
		"schedule-manual": TurnSourceScheduleManual,
		"":                "",
		"operator":        "",
		"evil-badge":      "",
	} {
		if got := scheduleInjectionSource(in); got != want {
			t.Errorf("scheduleInjectionSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// badgeOriginOf は投入1件をミラーのバッジ1つへ対応させる唯一の場所。TUI と managed で
// 別々に switch を書いていたときの取りこぼし（片方だけ由来を落とす）を作らないための表。
// "" は「利用者が自分で打った入力＝バッジ無し」で、ここだけは決して埋めてはいけない
// — 埋めると素の入力が operator バッジになる。
func TestBadgeOriginOf(t *testing.T) {
	cases := []struct{ peerFrom, reportTo, source, want string }{
		{peerFrom: "sender", want: turnSourcePeer},
		{peerFrom: "sender", source: "schedule", want: turnSourcePeer}, // peer が最優先
		{reportTo: "conv1", want: TurnSourceOperator},
		{reportTo: "conv1", source: "schedule", want: TurnSourceSchedule},
		{source: "schedule", want: TurnSourceSchedule},              // 完了報告 OFF の定時実行
		{source: "schedule-manual", want: TurnSourceScheduleManual}, // 手動発火
		{want: ""},                       // 素の Console 入力
		{source: "evil-badge", want: ""}, // report_to 無しの未知は素の入力扱い
	}
	for _, c := range cases {
		if got := badgeOriginOf(c.peerFrom, c.reportTo, c.source); got != c.want {
			t.Errorf("badgeOriginOf(%q, %q, %q) = %q, want %q", c.peerFrom, c.reportTo, c.source, got, c.want)
		}
	}
}

// injectionSource whitelists what callers may record: schedule origins pass through,
// anything unknown (or empty) degrades to operator — no arbitrary badge strings.
func TestInjectionSourceWhitelist(t *testing.T) {
	for in, want := range map[string]string{
		"schedule":        TurnSourceSchedule,
		"schedule-manual": TurnSourceScheduleManual,
		"":                TurnSourceOperator,
		"evil-badge":      TurnSourceOperator,
	} {
		if got := injectionSource(in); got != want {
			t.Errorf("injectionSource(%q) = %q, want %q", in, got, want)
		}
	}
}
