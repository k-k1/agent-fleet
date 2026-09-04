package main

// The success oracle for the agent output language (docs/log/28 P6).
//
// Every prompt that makes the model produce something a user reads branches on the display
// locale. A single Japanese character on the English side splits the language signal and the
// answer tips back into Japanese - which is where P6 started. So the rule pinned here, one
// prompt function at a time, is that no English prompt contains a single Japanese character.
//
// The other direction (the Japanese side still says what it used to) is covered by each
// feature's own tests matching the body, so this file only checks lightly that the Japanese
// side IS Japanese - enough to catch the English version being handed to both.

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"strings"
	"testing"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// japaneseRanges covers hiragana and katakana (plus the prolonged-sound and middle-dot marks),
// CJK unified ideographs, CJK symbols and punctuation, and fullwidth alphanumerics and symbols.
// Any of these in an English prompt is contamination.
var japaneseRanges = []*unicode.RangeTable{
	{R16: []unicode.Range16{
		{Lo: 0x3000, Hi: 0x303F, Stride: 1}, // CJK symbols and punctuation
		{Lo: 0x3040, Hi: 0x30FF, Stride: 1}, // hiragana and katakana
		{Lo: 0x4E00, Hi: 0x9FFF, Stride: 1}, // CJK unified ideographs
		{Lo: 0xFF01, Hi: 0xFF60, Stride: 1}, // fullwidth alphanumerics and symbols
	}},
}

// firstJapaneseRune returns the first Japanese rune in s (0 when there is none).
func firstJapaneseRune(s string) rune {
	for _, r := range s {
		if unicode.IsOneOf(japaneseRanges, r) {
			return r
		}
	}
	return 0
}

func hasJapanese(s string) bool { return firstJapaneseRune(s) != 0 }

// langPrompt is one "takes a lang, returns a prompt" function.
type langPrompt struct {
	name string
	fn   func(lang string) string
}

// enPrompts collects every prompt P6 branched. Forget to add a new one here and the Japanese
// survives on the English Console alone - every newly branched prompt owes a line.
func enPrompts() []langPrompt {
	turns := []transcript.Turn{
		{Role: "user", Text: "使用量のグラフを作りたい"},
		{Role: "assistant", Text: "台帳を設計します。どちらで進める?"},
	}
	msgs := []chatx.ChatMessage{{Role: "user", Content: "使用量のグラフを作りたい"}}
	return []langPrompt{
		{"replySuggestPersona", sessionx.ReplySuggestPersona},
		{"replySuggestInstructions/session", func(l string) string {
			return sessionx.ReplySuggestInstructions(l, sessionx.ReplyCounterpartSession)
		}},
		{"replySuggestInstructions/chat", func(l string) string {
			return sessionx.ReplySuggestInstructions(l, sessionx.ReplyCounterpartChat)
		}},
		{"replySuggestLogHeader", sessionx.ReplySuggestLogHeader},
		// The whole prompt embeds the conversation log body (usually Japanese, as written), so
		// only the frame is checked.
		{"replySuggestPrompt/frame", func(l string) string {
			return promptFrame(sessionx.ReplySuggestPrompt(turns, l), sessionx.ReplySuggestLogHeader(l))
		}},
		{"chatReplySuggestPrompt/frame", func(l string) string {
			return promptFrame(chatx.ChatReplySuggestPrompt(msgs, l), sessionx.ReplySuggestLogHeader(l))
		}},
		{"compactSummaryPromptFor", chatx.CompactSummaryPromptFor},
		{"handoffPreambleFor", chatx.HandoffPreambleFor},
		{"planShapeFor", chatx.PlanShapeFor},
		{"planUpdateInstructionFor", chatx.PlanUpdateInstructionFor},
		{"planPreambleFor", chatx.PlanPreambleFor},
		{"planTruncatedNote", chatx.PlanTruncatedNote},
		{"planRefreshPersonaFor", chatx.PlanRefreshPersonaFor},
		{"planRefreshInstructions/new", func(l string) string { return chatx.PlanRefreshInstructions("", l) }},
		{"planRefreshInstructions/update", func(l string) string { return chatx.PlanRefreshInstructions("plan body", l) }},
		{"planContextHeader", chatx.PlanContextHeader},
		{"chatPersonaFor", chatx.ChatPersonaFor},
		{"verbPersona/translate", func(l string) string { return chatx.VerbPersona("translate", l) }},
		{"verbPersona/summarize", func(l string) string { return chatx.VerbPersona("summarize", l) }},
		{"seedFor/translate", func(l string) string { return chatx.SeedFor("translate", "/w/a.md", false, l) }},
		{"seedFor/summarize", func(l string) string { return chatx.SeedFor("summarize", "/w/a.md", false, l) }},
		{"seedFor/ask", func(l string) string { return chatx.SeedFor("", "/w/a.md", false, l) }},
		{"seedFor/dir", func(l string) string { return chatx.SeedFor("", "/w", true, l) }},
		{"chatDefaultTitle", chatx.ChatDefaultTitle},
	}
}

// reportBodyForTest builds the prompt body of one session report (heading + facts +
// instructions + notes) from the same materials the real delivery path (recordSessionReport)
// assembles. Used by the existing tests that match the Japanese wording.
func reportBodyForTest(display, name, kind, reason string) string {
	args := chatx.ReportArgs(display, name, kind, reason, 0)
	return chatx.ReportPromptFor(chatx.ChatMessage{
		Role: "report", ReportKind: kind, ReportReason: reason, NoticeArgs: args,
	}, "ja")
}

// promptFrame drops everything after the conversation-log header: the log is the user's
// own text, passed through verbatim (translating it is the model's job, not ours).
func promptFrame(prompt, header string) string {
	if i := strings.Index(prompt, header); i >= 0 {
		return prompt[:i+len(header)]
	}
	return prompt
}

// The branched prompt is actually chosen by the ui-prefs locale: translating a function is
// pointless if its caller stays pinned to Japanese - the miss P6 can realistically leave behind.
func TestCarryoverPromptsFollowUILocale(t *testing.T) {
	writeUIPrefs(t, `{"locale":"en"}`)
	c := &chatx.ChatConversation{Plan: "## What comes next\n- lane A", PendingHandoff: "previous summary"}

	if got := chatx.CompactPrompt(c); hasJapanese(promptFrame(got, "## What comes next")) {
		t.Fatalf("Japanese mixed into the compaction prompt under the English locale:\n%s", got)
	}
	p, _ := chatx.InjectPlan(c, "claude", "go on")
	if hasJapanese(p) {
		t.Fatalf("Japanese mixed into the plan preamble under the English locale:\n%s", p)
	}
	h, _ := chatx.InjectHandoff(c, "go on")
	if hasJapanese(h) {
		t.Fatalf("Japanese mixed into the handoff preamble under the English locale:\n%s", h)
	}

	writeUIPrefs(t, `{"locale":"ja"}`)
	if got := chatx.CompactPrompt(c); !strings.Contains(got, "引き継ぎの作成") {
		t.Fatalf("the usual compaction prompt is missing under the Japanese locale:\n%s", got)
	}
}

func TestEnglishPromptsHaveNoJapanese(t *testing.T) {
	for _, p := range enPrompts() {
		t.Run(p.name, func(t *testing.T) {
			en := p.fn("en")
			if en == "" {
				t.Fatalf("%s(\"en\") is empty", p.name)
			}
			if r := firstJapaneseRune(en); r != 0 {
				t.Fatalf("Japanese %q contaminates the English prompt:\n%s", string(r), en)
			}
			if ja := p.fn("ja"); !hasJapanese(ja) {
				t.Fatalf("the Japanese prompt is not Japanese (is the English version handed to both?):\n%s", ja)
			}
			if en == p.fn("ja") {
				t.Fatalf("%s: ja and en are the same string", p.name)
			}
		})
	}
}
