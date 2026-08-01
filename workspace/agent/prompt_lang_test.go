package main

// エージェント出力言語（docs/28 P6）の成功オラクル。
//
// 「利用者が読む生成物を作らせる prompt」は表示言語でロケール分岐する。英語側にひとかけらでも
// 日本語が混ざると、モデルへの言語シグナルが割れて回答が日本語に倒れる（P6 の出発点そのもの）。
// そこで**英語側の全 prompt に日本語の文字が 1 つも無いこと**を、prompt 関数ごとにここで固定する。
//
// 逆向き（日本語側が従来どおりであること）は各機能のテストが本文一致で見ているので、ここでは
// 「日本語側は日本語である」ことだけを軽く確認する（英語版を両方に配ってしまう取り違えの検出）。

import (
	"strings"
	"testing"
	"unicode"

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
	msgs := []chatMessage{{Role: "user", Content: "使用量のグラフを作りたい"}}
	return []langPrompt{
		{"replySuggestPersona", replySuggestPersona},
		{"replySuggestInstructions/session", func(l string) string {
			return replySuggestInstructions(l, replyCounterpartSession)
		}},
		{"replySuggestInstructions/chat", func(l string) string {
			return replySuggestInstructions(l, replyCounterpartChat)
		}},
		{"replySuggestLogHeader", replySuggestLogHeader},
		// prompt 全体は会話ログ本文（原文＝日本語のことが多い）を含むので、枠だけを見る。
		{"replySuggestPrompt/frame", func(l string) string {
			return promptFrame(replySuggestPrompt(turns, l), replySuggestLogHeader(l))
		}},
		{"chatReplySuggestPrompt/frame", func(l string) string {
			return promptFrame(chatReplySuggestPrompt(msgs, l), replySuggestLogHeader(l))
		}},
	}
}

// promptFrame drops everything after the conversation-log header: the log is the user's
// own text, passed through verbatim (translating it is the model's job, not ours).
func promptFrame(prompt, header string) string {
	if i := strings.Index(prompt, header); i >= 0 {
		return prompt[:i+len(header)]
	}
	return prompt
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
