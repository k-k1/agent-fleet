package agy

// agy の workspace trust プロンプト（"Do you trust the contents of this project?"）へ
// PTY 越しに答える口。trust.go が settings.json 側（＝出させない方）を持ち、こちらは
// それでも出た場合の応答を持つ。
//
// ⚠️ 「Yes が既定だから Enter を送ればよい」とは**書かない**。上流は既定の選択肢を平気で
// 入れ替える —— claude 2.1.248 で同じダイアログの並びが反転し（2.1.247: `❯ 1. Yes, I
// trust this folder / 2. No, exit` → 2.1.248: `❯ No, exit / Yes, I trust this folder`）、
// 盲打ちの Enter がその日から「承認」ではなく「終了」になった。CLI が即終了するので、
// 呼び出し側からは「空フレームのまま固まった」ようにしか見えない（PR #229）。
// agy にも同型の盲打ちが login / usage / context の 3 箇所あった。
//
// 1.1.22 の実測フレーム（runner 上・tmux capture-pane、マーカーは ASCII の "> "）:
//
//	Accessing workspace:
//	/tmp/tmp.aGbfGwpLSk
//	Do you trust the contents of this project?
//	Antigravity CLI requires permission to read, edit, and execute files here.
//	> Yes, I trust this folder
//	  No, exit
//	  ↑/↓ Navigate · enter Confirm
//
// 今日はまだ Yes が先頭だが、それに依存しない: 位置ではなく**文言**で行を選び、乗れ
// なければフレームごとエラーにして落とす。

import (
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
)

const (
	// trustPrompt is the prompt's question line (the screen's identity).
	trustPrompt = "Do you trust the contents of this project?"
	// trustYesLabel is the prefix of the accepting row. Prefix, not the whole
	// label: "this folder" / "this project" 程度の言い換えでは壊れないように。
	trustYesLabel = "Yes, I trust"
	// trustAnswerTries bounds the move-and-recheck loop. 2 択なら移動は 1 回で足りる
	// ので、残りは描画待ちの余裕（400ms × 6 ≈ 2.4 秒）。
	trustAnswerTries = 6
)

var (
	trustRe = regexp.MustCompile(regexp.QuoteMeta(trustPrompt))
	// 選択行の先頭マーカー。1.1.22 は ASCII の "> "（未選択行は空白 2 で字下げ）。
	// 上流が ❯ に変えても拾えるよう両方見る。ラベルが空の行（メイン画面の入力欄
	// "> " など）は選択肢ではないので `\S` を要求して弾く。
	trustMarkedRe = regexp.MustCompile(`^\s*[>❯]\s+(\S.*?)\s*$`)
)

// trustSelected returns the label of the currently highlighted row of the trust
// prompt as rendered in out (agents.Flow.Clean() の**累積**バッファ), or "" when
// no highlighted row can be read.
//
// 累積バッファなので、質問行の最後の出現以降だけを見る（parseUsage / parseContext と
// 同じ「最後の描画だけ信じる」型）。Ink は選択が動くと選択肢の行だけを描き直して末尾に
// 追記するので、その区間を**末尾から**走査して最初に見つかったマーカー行が現在の選択。
func trustSelected(out string) string {
	i := strings.LastIndex(out, trustPrompt)
	if i < 0 {
		return ""
	}
	lines := strings.Split(out[i:], "\n")
	for k := len(lines) - 1; k >= 0; k-- {
		if m := trustMarkedRe.FindStringSubmatch(lines[k]); m != nil {
			return m[1]
		}
	}
	return ""
}

// trustFrame returns the tail of out from the last trust prompt on, for error
// messages（何が出ていたのかを見ないと次の変化に気づけない）。
func trustFrame(out string) string {
	if i := strings.LastIndex(out, trustPrompt); i >= 0 {
		return out[i:]
	}
	// 質問文ごと変わった場合は末尾を出す。行境界で切るので UTF-8 の途中で切れない。
	if len(out) > 800 {
		out = out[len(out)-800:]
		if k := strings.IndexByte(out, '\n'); k >= 0 {
			out = out[k+1:]
		}
	}
	return out
}

// trustAction is what to do with the prompt as currently rendered.
type trustAction int

const (
	trustUnreadable trustAction = iota // 選択行が読めない → 押さずに落とす
	trustConfirm                       // Yes の行に乗っている → Enter
	trustMove                          // 別の行に乗っている → ↓ で動かす
)

// trustStep decides the next keystroke from the accumulated PTY buffer. Pure —
// これが盲打ちをやめた本体なので、実 CLI 無しで検証できる形に切り出してある。
func trustStep(out string) trustAction {
	switch sel := trustSelected(out); {
	case sel == "":
		return trustUnreadable
	case strings.HasPrefix(sel, trustYesLabel):
		return trustConfirm
	default:
		return trustMove
	}
}

// answerTrustPrompt accepts the workspace-trust prompt on f by moving the
// selection onto the "Yes, I trust …" row and only then pressing Enter. It never
// presses Enter on a row it has not read.
func answerTrustPrompt(f *agents.Flow) error {
	for i := 0; i < trustAnswerTries; i++ {
		switch trustStep(f.Clean()) {
		case trustConfirm:
			_, err := f.Ptmx.Write([]byte(keyEnter))
			return err
		case trustMove:
			if _, err := f.Ptmx.Write([]byte(keyDown)); err != nil {
				return err
			}
		}
		// trustUnreadable も含めてここで待って読み直す。WaitFor は質問行が出た瞬間に
		// 返るので、選択肢の行がまだ届いていないことがある —— それは「形が変わった」
		// ではなく「まだ描けていない」。どちらにせよ**押さない**のが肝。
		time.Sleep(400 * time.Millisecond)
	}
	return errString("agy の信頼プロンプトで「" + trustYesLabel + "」の行を選べませんでした" +
		"（選択肢の形が変わった可能性）:\n" + trustFrame(f.Clean()))
}
