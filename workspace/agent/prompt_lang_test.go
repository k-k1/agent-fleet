package main

// エージェント出力言語（docs/log/28 P6）の成功オラクル。
//
// 「利用者が読む生成物を作らせる prompt」は表示言語でロケール分岐する。英語側にひとかけらでも
// 日本語が混ざると、モデルへの言語シグナルが割れて回答が日本語に倒れる（P6 の出発点そのもの）。
// そこで**英語側の全 prompt に日本語の文字が 1 つも無いこと**を、prompt 関数ごとにここで固定する。
//
// 逆向き（日本語側が従来どおりであること）は各機能のテストが本文一致で見ているので、ここでは
// 「日本語側は日本語である」ことだけを軽く確認する（英語版を両方に配ってしまう取り違えの検出）。

import (
	"github.com/k-k1/agent-fleet/workspace/agent/internal/sessionx"
	"strings"
	"testing"
	"unicode"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// japaneseRanges: ひらがな・カタカナ（＋長音/中点）・CJK 統合漢字・CJK 記号（。、「」）・
// 全角英数記号（（）：　）。英語 prompt にこれらが現れたら混入。
var japaneseRanges = []*unicode.RangeTable{
	{R16: []unicode.Range16{
		{Lo: 0x3000, Hi: 0x303F, Stride: 1}, // 、。「」『』（）〜 など CJK 記号・句読点
		{Lo: 0x3040, Hi: 0x30FF, Stride: 1}, // ひらがな・カタカナ・ー・・
		{Lo: 0x4E00, Hi: 0x9FFF, Stride: 1}, // CJK 統合漢字
		{Lo: 0xFF01, Hi: 0xFF60, Stride: 1}, // 全角英数・記号
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

// langPrompt は「lang を受け取って prompt を返す」関数 1 本ぶん。
type langPrompt struct {
	name string
	fn   func(lang string) string
}

// enPrompts collects every prompt P6 branched. 追加した prompt をここに足すのを忘れると、
// 英語 Console にだけ日本語が残る — 新しい prompt を分岐したら必ず 1 行足すこと。
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
		// prompt 全体は会話ログ本文（原文＝日本語のことが多い）を含むので、枠だけを見る。
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
		{"planRefreshInstructions/新規", func(l string) string { return chatx.PlanRefreshInstructions("", l) }},
		{"planRefreshInstructions/差分更新", func(l string) string { return chatx.PlanRefreshInstructions("plan body", l) }},
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

// reportBodyForTest はセッション報告 1 通の**プロンプト本文**（見出し＋事実＋指示＋付記）を、
// 実際の配送（recordSessionReport）と同じ材料の組み方で作る。日本語側の文言を見る既存の
// テストが使う。
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

// 分岐した prompt が実際に ui-prefs の locale で選ばれること（関数を英語化しても呼び出し側が
// 日本語固定のままなら意味がない — P6 で実際に起きうる取りこぼし）。
func TestCarryoverPromptsFollowUILocale(t *testing.T) {
	writeUIPrefs(t, `{"locale":"en"}`)
	c := &chatx.ChatConversation{Plan: "## What comes next\n- lane A", PendingHandoff: "previous summary"}

	if got := chatx.CompactPrompt(c); hasJapanese(promptFrame(got, "## What comes next")) {
		t.Fatalf("英語ロケールの圧縮プロンプトに日本語が混ざる:\n%s", got)
	}
	p, _ := chatx.InjectPlan(c, "claude", "go on")
	if hasJapanese(p) {
		t.Fatalf("英語ロケールの計画前置きに日本語が混ざる:\n%s", p)
	}
	h, _ := chatx.InjectHandoff(c, "go on")
	if hasJapanese(h) {
		t.Fatalf("英語ロケールの引き継ぎ前置きに日本語が混ざる:\n%s", h)
	}

	writeUIPrefs(t, `{"locale":"ja"}`)
	if got := chatx.CompactPrompt(c); !strings.Contains(got, "引き継ぎの作成") {
		t.Fatalf("日本語ロケールで従来の圧縮プロンプトが出ない:\n%s", got)
	}
}

func TestEnglishPromptsHaveNoJapanese(t *testing.T) {
	for _, p := range enPrompts() {
		t.Run(p.name, func(t *testing.T) {
			en := p.fn("en")
			if en == "" {
				t.Fatalf("%s(\"en\") が空", p.name)
			}
			if r := firstJapaneseRune(en); r != 0 {
				t.Fatalf("英語 prompt に日本語 %q が混入している:\n%s", string(r), en)
			}
			if ja := p.fn("ja"); !hasJapanese(ja) {
				t.Fatalf("日本語 prompt が日本語でない（英語版を両方に配っていないか）:\n%s", ja)
			}
			if en == p.fn("ja") {
				t.Fatalf("%s: ja と en が同一文字列", p.name)
			}
		})
	}
}
