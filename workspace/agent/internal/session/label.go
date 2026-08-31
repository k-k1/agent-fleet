package session

import (
	"regexp"
	"strings"
)

// セッションの表示ラベル（claude の `--name` に渡す文字列）の**形の唯一の定義**と、その
// 読み戻し。組み立ては package main の sessionLabelFor、剥がすのは Display / CP / Console
// と散っているので、形そのものはここ1か所に置く。
//
// 形は `[AF:<name>] <title>`。`[AF]` タグは Agent Fleet が起こしたセッションを claude.ai の
// Remote Control ピッカーで見分けるための印で、`:<name>` はそこに**セッション名**を足した
// もの（docs/log/58 §58.16）。
//
// **足した理由は誤配で、実害が出ている。** claude 自身の cross-session チャネル
// （ListAgents / SendMessage）は宛先を**このラベル文字列**で指す。AF のセッション名はその
// 名前空間に無いので `to:"s6bbilu"` は届かず、逆にラベルはタイトルだけだったので、**同じ
// タイトルのセッションが2つあると送り手には区別が付かない** — 実際に、試走セッションからの
// 申告が「同名タイトルの旧セッション」へ丸ごと流れた（2026-08-31）。名前を入れておけば
// 一覧の時点で別物として見える。
//
// 古い `[AF] <title>` も読めること。ラベルは作成時（とタイトル変更時）に確定して meta に
// 焼かれるので、既存セッションは古い形のまま残る。**新しい形しか剥がせない strip を書くと、
// アップグレードを跨いだ古い行だけ表示にタグが残る**という、レビューで見つけにくい壊れ方に
// なる。
const labelTag = "[AF]"

// labelRe は先頭のタグに一致する。旧 `[AF] ` と新 `[AF:<name>] ` の両方。名前部分は
// ValidName と同じ字種に限る（タイトルが `[AF:` で始まっていても名前と誤読しないため）。
var labelRe = regexp.MustCompile(`^\[AF(?::([A-Za-z0-9][A-Za-z0-9_-]*))?\]\s*`)

// LabelPrefix は新しいラベルの先頭に置くタグ（末尾の空白込み）を返す。名前が不正なときは
// 旧来の `[AF] ` へ落とす — ラベルは表示とピッカーのためのもので、ここで失敗させて起動を
// 止める価値は無い。
func LabelPrefix(name string) string {
	if !ValidName(name) {
		return labelTag + " "
	}
	return "[AF:" + name + "] "
}

// StripLabel は表示用にタグを取り除く。タグが無ければそのまま返す（他所で付けられた
// `--name` を持つセッションもあるため）。
func StripLabel(label string) string {
	return strings.TrimSpace(labelRe.ReplaceAllString(label, ""))
}

// LabelSessionName はラベルからセッション名を読み戻す。"" = タグが無い / 旧形式 / AF の
// ラベルではない。**推測はしない** — 名前が取れないことと、間違った名前を出すことでは
// 後者の方がはるかに悪い（ミラーの peer バッジは「誰の仕業か」を辿る唯一の手掛かりで、
// そこに別セッションの名前が出ると調査が丸ごと逸れる）。
func LabelSessionName(label string) string {
	m := labelRe.FindStringSubmatch(label)
	if m == nil {
		return ""
	}
	return m[1]
}
