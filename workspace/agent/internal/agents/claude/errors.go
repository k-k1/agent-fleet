package claude

// API エラーで畳まれたターンの表示形。
//
// claude は失敗を**合成 assistant レコード**として転写に書く（type=assistant・
// model=`<synthetic>`・`isApiErrorMessage:true`）。その中身はただの text ブロックなので、
// ミラーではこれまで**普通の回答と同じ吹き出し**として描かれていた — 認証切れで落ちた
// ターンが「エージェントがそう答えた」ようにしか見えず、しかも本文は CLI 向けの
// 「Please run /login」で、Console の利用者にはどこを触ればいいのか分からなかった。
//
// codex / opencode は同じ失敗を `kind="error"` の part として出しており（各 errors.go）、
// Console の ErrorBlock（.mirror-error）がそれを常時展開の赤いブロックで描く。claude も
// 同じ語彙へ寄せる。ここは docs/47 の分類（再送で直るか）とは別軸で、**画面に何をどう
// 出すか**だけを決める。
//
// 実測レコード（2026-08-06 22:12 UTC / 認証切れ・転写コーパス）:
//
//	{"type":"assistant","isApiErrorMessage":true,"apiErrorStatus":401,
//	 "error":"authentication_failed",
//	 "message":{"model":"<synthetic>","content":[{"type":"text",
//	   "text":"Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate to continue."}]}}

import (
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// causeAuth は「認証をやり直せば直る」失敗の印。Console はこれを見たときだけ再認証への
// 導線（設定 > エージェント）を出す。値は**機械可読な合図**であって表示文ではない
// （文面と言語は Console の i18n が持つ）。
//
// 現状 auth だけを立てる: 他の原因（上限・プロンプト超過・サーバ側）は Console 側に
// 対応する操作が無く、印を増やしても使い道が無いため。増やすときはここに足す。
const causeAuth = "auth"

// apiError is one synthetic API-error record, normalized to the same label+detail shape
// codex/opencode use so all three render identically downstream.
type apiError struct {
	msg    string // 本文（claude の英文言。版ごとに書き換わる前提で扱う）
	kind   string // `error` フィールド: authentication_failed / rate_limit / server_error / invalid_request
	status int    // apiErrorStatus。合成レコードでは欠けることがある（docs/47 §2）
}

// authKinds are claude's own machine-readable causes that mean「ログインし直せ」。文言と
// 違ってこのフィールドは版ごとに書き換わらないので、判定の主にする（docs/47 と同じ方針）。
var authKinds = map[string]bool{
	"authentication_failed": true,
	"authentication_error":  true, // API 側の type 名がそのまま載る形への備え
}

// authMarkers are the texts that say the same thing when `error` は空 / 未知の値だった
// とき用。実測文言（"Please run /login · API Error: 401 OAuth access token has expired.
// Re-authenticate to continue."）を含め、語幹で持つ。
var authMarkers = []string{
	"run /login",
	"re-authenticate",
	"authentication_failed",
	"invalid api key",
	"unauthorized",
}

// isAuth reports whether this failure clears by signing in again. status 401 も入口に
// するが、403（権限）や 400（残高・プロンプト超過）は含めない — 再認証しても直らない
// 失敗で「再認証しろ」と出すのは、来ないリセットを待たせるのと同じ実害になる。
func (e apiError) isAuth() bool {
	if authKinds[strings.ToLower(strings.TrimSpace(e.kind))] {
		return true
	}
	if e.status == 401 {
		return true
	}
	low := strings.ToLower(e.msg)
	for _, m := range authMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// cause is the machine-readable hint the Console keys its guidance off. "" = 導線なし。
func (e apiError) cause() string {
	if e.isAuth() {
		return causeAuth
	}
	return ""
}

// label renders the short error identity ("authentication_failed (HTTP 401)").
func (e apiError) label() string {
	name := strings.TrimSpace(e.kind)
	if name == "" {
		name = "error"
	}
	if e.status > 0 {
		return name + " (HTTP " + strconv.Itoa(e.status) + ")"
	}
	return name
}

// detail is the human-facing message. claude の文面をそのまま渡す（Console は verbatim
// で描く）。本文が空のレコードでも見出しだけは残るように label へ落とす。
func (e apiError) detail() string {
	if m := strings.TrimSpace(e.msg); m != "" {
		return m
	}
	return e.label()
}

// summary is the one-line form used where a turn is flattened to text: コピー・
// get_session_output・チャットブリッジ。opencode/codex と同じ `[error] ` タグを付けて、
// 読み手（人でもオペレーターでも）がエージェントの地の文と区別できるようにする。
func (e apiError) summary() string {
	d := e.detail()
	if d == e.label() {
		return "[error] " + d
	}
	return "[error] " + e.label() + ": " + d
}

// part renders the failure as the ordered part the Console draws as an error block.
func (e apiError) part() transcript.Part {
	return transcript.Part{Kind: "error", Info: e.label(), Text: e.detail(), Cause: e.cause()}
}
