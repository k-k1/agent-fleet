package agy

import (
	"strings"
	"testing"
)

// trustFrame122 is the real 1.1.22 workspace-trust screen (2026-08-28 実測,
// runner 上の tmux capture-pane)。マーカーは ASCII の "> "、未選択行は空白 2。
const trustFrame122 = `Accessing workspace:
/tmp/tmp.aGbfGwpLSk
Do you trust the contents of this project?
Antigravity CLI requires permission to read, edit, and execute files here.
> Yes, I trust this folder
  No, exit
  ↑/↓ Navigate · enter Confirm
`

// 上流が並びを入れ替えた場合（claude 2.1.248 で実際に起きた形）。既定は No。
const trustFrameFlipped = `Accessing workspace:
/tmp/x
Do you trust the contents of this project?
Antigravity CLI requires permission to read, edit, and execute files here.
> No, exit
  Yes, I trust this folder
  ↑/↓ Navigate · enter Confirm
`

// TestTrustSelectedReadsTheHighlightedRow: 選択行は位置ではなくマーカーで読む。
func TestTrustSelectedReadsTheHighlightedRow(t *testing.T) {
	for name, tc := range map[string]struct{ frame, want string }{
		"1.1.22 実測（Yes が先頭・既定）": {trustFrame122, "Yes, I trust this folder"},
		"並びが反転（既定が No）":         {trustFrameFlipped, "No, exit"},
		"❯ マーカーに変わっても読める": {
			strings.Replace(trustFrame122, "> Yes", "❯ Yes", 1), "Yes, I trust this folder"},
	} {
		if got := trustSelected(tc.frame); got != tc.want {
			t.Errorf("%s: trustSelected = %q, want %q", name, got, tc.want)
		}
	}
}

// TestTrustSelectedUsesTheLastRender: agents.Flow.Clean() は再描画を全部連結した
// **累積**バッファ。↓ で選択が動くと Ink は選択肢の行だけを描き直して末尾に追記する
// ので、古い描画の "> Yes" を掴んではいけない（掴むと「乗っている」と誤認して No の
// 上で Enter を押す＝終了する）。
func TestTrustSelectedUsesTheLastRender(t *testing.T) {
	// 質問行を含む完全な再描画が後から来る場合。
	full := trustFrame122 + trustFrameFlipped
	if got := trustSelected(full); got != "No, exit" {
		t.Errorf("full redraw: trustSelected = %q, want %q", got, "No, exit")
	}
	// 質問行を伴わない部分再描画（選択肢の 2 行だけ）が後から来る場合。
	partial := trustFrame122 + "  Yes, I trust this folder\n> No, exit\n"
	if got := trustSelected(partial); got != "No, exit" {
		t.Errorf("partial redraw: trustSelected = %q, want %q", got, "No, exit")
	}
}

// TestTrustSelectedUnreadable: 読めないものを「読めた」ことにしない。読めないまま
// Enter を送るのが盲打ちそのものなので、ここは "" を返して呼び出し側を落とさせる。
func TestTrustSelectedUnreadable(t *testing.T) {
	for name, frame := range map[string]string{
		"プロンプトが無い": "? for shortcuts\n> \n",
		"マーカー行が無い": strings.Replace(trustFrame122, "> Yes", "  Yes", 1),
		"空":        "",
		// メイン画面の入力欄は "> " だけ。ラベルが無いので選択肢ではない。
		"入力欄だけの > 行": trustFrame122[:strings.Index(trustFrame122, "> Yes")] + "> \n",
	} {
		if got := trustSelected(frame); got != "" {
			t.Errorf("%s: trustSelected = %q, want \"\"", name, got)
		}
	}
}

// TestTrustStep: 盲打ちをやめた本体。どのフレームで Enter を押し、どのフレームで
// 押さないか。実 CLI 無しで回る（フレーム文字列を与えるだけ）。
func TestTrustStep(t *testing.T) {
	for name, tc := range map[string]struct {
		frame string
		want  trustAction
	}{
		"1.1.22 実測（Yes に乗っている）": {trustFrame122, trustConfirm},
		// ★ ここが claude 2.1.248 で実害になった形。既定が No なので Enter は禁止、
		//    まず ↓ で Yes へ動かす。
		"並びが反転（No に乗っている）": {trustFrameFlipped, trustMove},
		"↓ で Yes へ動かした後":   {trustFrameFlipped + "> Yes, I trust this folder\n  No, exit\n", trustConfirm},
		"選択肢の行がまだ描けていない":   {trustFrame122[:strings.Index(trustFrame122, "> Yes")], trustUnreadable},
		"マーカーの形が変わって読めない":  {strings.Replace(trustFrame122, "> Yes", "* Yes", 1), trustUnreadable},
		"文言ごと変わった（未知の選択肢だけ）": {
			strings.Replace(trustFrame122, "> Yes, I trust this folder", "> Sure, go ahead", 1), trustMove},
	} {
		if got := trustStep(tc.frame); got != tc.want {
			t.Errorf("%s: trustStep = %v, want %v", name, got, tc.want)
		}
	}
}

// TestTrustYesLabelMatchesTheMeasuredFrame: 文言で選ぶ以上、その文言が実測フレームの
// Yes 行に当たっていることが契約。ここがズレると「選べない」で毎回落ちる。
func TestTrustYesLabelMatchesTheMeasuredFrame(t *testing.T) {
	if sel := trustSelected(trustFrame122); !strings.HasPrefix(sel, trustYesLabel) {
		t.Fatalf("実測フレームの選択行 %q が trustYesLabel %q に前方一致しません", sel, trustYesLabel)
	}
	if !trustRe.MatchString(trustFrame122) {
		t.Fatalf("trustRe が実測フレームに当たりません")
	}
	// 反転フレームでは Yes に乗っていない＝そのまま Enter を送ってはいけない。
	if strings.HasPrefix(trustSelected(trustFrameFlipped), trustYesLabel) {
		t.Fatal("反転フレームを Yes 選択と誤認しています")
	}
}
