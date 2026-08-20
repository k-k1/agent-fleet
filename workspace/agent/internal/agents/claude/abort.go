package claude

// 中断ターンの検知（docs/47）。
//
// claude は API エラーでターンが落ちたとき Stop hook を鳴らさない。よって
// working→idle の遷移が誰にも記録されず、ペインだけが待機プロンプトに戻る。その後
// 自己修復（driveState / WireLive）が「非 idle なのにプロンプトへ戻っている」を見て
// 状態ファイルを黙って消していたため、応答あり通知も docs/30 の完了報告も生まれず、
// 報告の arm が未消費のまま残っていた（実測: セッション ssiw5kb / 2026-07-26）。
//
// 落ちたことは transcript に残る: type=assistant かつ isApiErrorMessage=true の
// 合成レコードが 1 行書かれ、以降そのターンには実レコードが続かない。ここではその
// 末尾形だけを見て「中断で終わったか」を判定し、原因を「再送で直る中断」と「原因が
// 解消するまで再送しても同じ失敗」に分ける。前者だけが自動再開の対象になる。

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// abortRecord is the subset of a transcript line the abort detector needs.
type abortRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsAPIError  bool   `json:"isApiErrorMessage"`
	Status      int    `json:"apiErrorStatus"`
	Timestamp   string `json:"timestamp"`
	// Error is claude's own MACHINE-READABLE cause ("server_error" / "rate_limit" /
	// "invalid_request" — 実測 2026-08-05). 英文言と違ってこれは版ごとに書き換わらない
	// ので、文言に無い形が来たときの最後の手掛かりになる（docs/47 §5「次の一手」）。
	Error string `json:"error"`
}

// Abort is a detected turn cut-off, with everything a caller needs to report it.
type Abort struct {
	Msg       string    // the error text (rides the report / chat bridge as the reason)
	Retryable bool      // 再送で直る（自動再開の対象）か、原因を直すまで無意味か
	At        time.Time // 中断が記録された時刻。ゼロ値 = レコードに時刻が無い
}

// retryableOverrides are texts where claude ITSELF says the error is not the user's
// limit. They are checked before blockedMarkers because the sentence contains the words
// a naive substring match reads as a usage limit — "Server is temporarily limiting
// requests (NOT YOUR USAGE LIMIT) · Rate limited". Getting this backwards would make the
// most common retryable error (7 of 16 in the corpus) never auto-resume.
var retryableOverrides = []string{
	"temporarily limiting requests",
	"not your usage limit",
}

// blockedMarkers are error texts whose cause does NOT clear on its own: re-sending the
// same turn reproduces the same error, so the operator must fix the cause first (残高
// /上限・プロンプト長超過・認証)。実測コーパス（docs/47 §2）由来。
var blockedMarkers = []string{
	"reached your",       // "You've reached your <model> limit. Run /usage-credits …"
	"usage limit",        // 別表現の上限（overrides で「上限ではない」旨を先に除外済み）
	"session limit",      // "You've hit your session limit · resets 7:50pm (Asia/Tokyo)"
	"weekly limit",       // "You've hit your weekly limit · resets 9am (Asia/Tokyo)"（実測コーパス）
	"spend limit",        // "You've hit your org's monthly spend limit · run /usage-credits …"
	"prompt is too long", // 会話が長すぎる — /compact なしの再送は無意味
	"credit balance",
	"invalid api key",
	"authentication",
	"unauthorized",
	// 実測の認証切れ（2026-08-06 / apiErrorStatus 401 / error:"authentication_failed"）:
	// "Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate
	// to continue." — 上の "authentication" には**当たらない**（本文にあるのは
	// "Re-authenticate"）。401 なので既定の blocked に落ちて結果は正しかったが、
	// 偶然そうなっていただけなので語幹で明示する。
	"re-authenticate",
	"run /login",
}

// limitMarkers are the blockedMarkers that specifically mean A USAGE LIMIT — a quota that
// lifts on its own schedule — as opposed to the other blocked causes (長すぎるプロンプト・
// 残高・認証) which never lift by waiting. 上限エピソード（rate_limit_resume.go）は
// blockedMarkers 全体ではなくこの部分集合だけを入口にする: プロンプト超過や認証エラーで
// 「利用上限に達しました」と通知したら、利用者は来ないリセットを待つことになる。
var limitMarkers = []string{
	// モデル別の上限。メニューを出さず 1 行のエラーでターンを畳む形（実測 2026-08-05
	// s6no6jv / claude 2.1.x）— "You've reached your Fable 5 limit. Run /usage-credits …"
	"reached your",
	"usage limit",
	// アカウントの窓。/rate-limit-options メニューを伴う形（実測 2026-07-31 s5jjqv4）—
	// "You've hit your session limit · resets 7:50pm (Asia/Tokyo)"
	"session limit",
	// 週次の窓（実測コーパス 2026-08-20）— "You've hit your weekly limit · resets 9am
	// (Asia/Tokyo)"。上の 3 語のどれにも当たらないので、**週次だけがエピソードを開けず**
	// 通知も再開予約もチップも出ていなかった（既定の blocked に落ちて分類だけ正しかった）。
	"weekly limit",
}

// LimitKind splits「上限で終わったターン」into the two kinds whose next move is opposite.
// docs/47 §4-10。どちらも 429 / `error:"rate_limit"` で届くので、コードでは分けられない。
type LimitKind string

const (
	// LimitWindow は時間の窓（5時間 / 週次 / モデル別）。待てば解ける＝自動再開の対象。
	LimitWindow LimitKind = "window"
	// LimitSpend は支出・残高の上限。**待っても解けない** — 増枠かクレジットの追加という
	// 課金側の判断が要るので、自動再開は仕込まないし「制限解除待ち」とも名乗らない。
	LimitSpend LimitKind = "spend"
)

// spendMarkers are the usage-limit texts that mean 金額側の上限。実測（2026-08-20・
// 利用者報告のスクリーンショット）:
//
//	You've hit your org's monthly spend limit · run /usage-credits to raise it,
//	or visit claude.ai/admin-settings/usage
//
// **"/usage-credits" を材料にしてはいけない**: モデル別の窓の上限（"You've reached your
// Fable 5 limit. Run /usage-credits to continue or switch models…"）も同じコマンドを案内
// するので、両方に当たって窓の上限まで「増枠が必要」に化ける。金額そのものを名指す語
// （spend limit / credit balance）だけを持つ。
var spendMarkers = []string{
	"spend limit",
	"credit balance", // "Your credit balance is too low …"（blockedMarkers の実測由来）
}

// retryableMarkers are error texts that clear by themselves: the turn was cut off by a
// transport / capacity hiccup and simply re-running it continues the work.
var retryableMarkers = []string{
	"connection closed",
	"connection error",
	"temporarily limiting requests", // 429 だが利用上限ではない（本文が明示している）
	"overloaded",
	"timed out",
	"timeout",
	// "server error" は "internal server error" を含む広い形。実測 sp2qemx (2026-07-30)
	// の "API Error: Server error mid-response. The response above may be incomplete."
	// はこの語しか手掛かりが無い — apiErrorStatus フィールドごと欠けているので下の
	// 5xx フォールバックにも掛からず、blocked（＝自動再開しない）に倒れていた。
	"server error",
	"service unavailable",
	"bad gateway",
	// ストリームの番犬（claude 2.1.x の内部リトライを使い切った形）。実測 2026-08-05:
	// "API Error: Stream idle timeout - no chunks received"。上の "timed out"/"timeout"
	// でも当たるが、既知の形として明示しておく — 文言が「no chunks received」側へ
	// 寄っても拾えるように、語幹（stream idle）で持つ。
	"stream idle",
}

// retryableErrorKinds maps claude's own `error` field onto 再送で直る側。文言に無い形が
// 来たときの受け皿で、**文言判定のあと**に見る（文言は「上限ではない」といった否定を
// 表現できるが、このフィールドは分類までしか言わない）。
//
// ここに "rate_limit" は入れない: 429 は利用上限（blocked）と一時的なレート制限
// （retryable）が同居する軸で、どちらかは文言でしか分からないから（docs/47 §2）。
// 実測で見えた値だけを載せる — 未知の値は既定どおり blocked に倒れる（判定不能は
// 自動再開しない方が安全側）ので、憶測の項目を足して穴を広げない。
var retryableErrorKinds = map[string]bool{
	"server_error": true, // 実測: 529 Overloaded / Connection closed / Server error mid-response
}

// blockedErrorKinds are the `error` values whose cause never clears by re-sending
// (プロンプト超過・不正なリクエスト・認証). 既定が blocked なので分類結果は同じだが、
// 意図した判定として明示しておく（"偶然だけ正解している" 状態を残さない）。
var blockedErrorKinds = map[string]bool{
	"invalid_request":       true, // 実測: Prompt is too long
	"authentication_failed": true, // 実測: 401 OAuth access token has expired（再ログインするまで同じ）
}

// classifyAbort splits an API error message into 再送で直る (true) か 原因を直すまで
// 無意味 (false) か。判定不能は false に倒す — 自動再開はしない方が安全側。
// blocked を先に見るのは、利用上限も 429 で届くため（"You've reached your … limit"）
// ステータスコードだけでは一時的なレート制限と区別できないから。
//
// 順序は 文言 → `error` → ステータス。文言が主なのは、それだけが「上限ではない」と
// いった否定を表現できるから（retryableOverrides）。`error` を status より先に見るのは、
// 合成レコードでは apiErrorStatus ごと欠けることがある一方、`error` は残っているため。
func classifyAbort(msg string, status int, errKind string) bool {
	low := strings.ToLower(msg)
	for _, m := range retryableOverrides {
		if strings.Contains(low, m) {
			return true
		}
	}
	for _, m := range blockedMarkers {
		if strings.Contains(low, m) {
			return false
		}
	}
	for _, m := range retryableMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	switch k := strings.ToLower(strings.TrimSpace(errKind)); {
	case retryableErrorKinds[k]:
		return true
	case blockedErrorKinds[k]:
		return false
	}
	return status >= 500 && status <= 599
}

// AbortedTurn reports whether the session's transcript ENDS on an API error — i.e. the
// last turn was cut off and no Stop hook ever fired. msg is the error text (it rides the
// report / chat bridge as the reason), retryable says whether a plain re-run should work.
//
// 実レコード（type=user / assistant）だけを終端の判定材料にする。custom-title・mode・
// last-prompt・file-history-* ・system(turn_duration) といった記帳レコードは中断後にも
// 書かれ、種類も版ごとに増減するので、除外リストではなく「user/assistant 以外は無視」
// の許可リストで受ける（版差に強い）。サブエージェント（isSidechain）のエラーは本体の
// ターンの終端ではないので同じく無視する。
//
// ユーザーが手で再開した後は末尾が user/assistant に変わるため、この関数は自然に
// false へ戻る（＝一度中断を報告したセッションが再開後にもう一度報告されることはない）。
func AbortedTurn(sid string) (msg string, retryable, ok bool) {
	a, ok := AbortInfo(sid)
	return a.Msg, a.Retryable, ok
}

// AbortInfo is AbortedTurn plus WHEN the cut-off was recorded — what a level-driven
// reader needs (docs/51): the report reconciler compares that time against the
// instruction cursor to decide which instructions this terminal event covers.
//
// It reads only the tail of the live transcript (lastLineWhere), because unlike the
// heal path — which asks once, after the pane is seen at its prompt — the reconciler
// asks every tick for every armed session.
func AbortInfo(sid string) (Abort, bool) {
	for _, p := range jsonlByMtime(sid) {
		line, found := lastLineWhere(p, func(l []byte) bool { _, ok := terminalRecord(l); return ok })
		if !found {
			continue // この転写には終端の判定材料が無い（stub 等）— 次の候補へ
		}
		r, _ := terminalRecord(line)
		return abortFrom(line, r)
	}
	return Abort{}, false
}

// abortedTurnFrom is the pure form used by the corpus / table tests: same rule, applied
// to a whole set of lines instead of a file's tail.
func abortedTurnFrom(lines [][]byte) (msg string, retryable, ok bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		r, isTerminal := terminalRecord(lines[i])
		if !isTerminal {
			continue // 記帳レコード / サブエージェント — 終端の判定材料にしない
		}
		a, ok := abortFrom(lines[i], r)
		return a.Msg, a.Retryable, ok
	}
	return "", false, false
}

// terminalRecord parses a line and reports whether it can END a turn: a real record
// (user/assistant) that is not a subagent's. 除外リストではなく許可リストで受けるのは
// 記帳レコードの種類が版ごとに増減するから（docs/47）。
func terminalRecord(line []byte) (abortRecord, bool) {
	var r abortRecord
	if json.Unmarshal(line, &r) != nil {
		return r, false
	}
	if r.IsSidechain || (r.Type != "user" && r.Type != "assistant") {
		return r, false
	}
	return r, true
}

// abortFrom is the verdict for one terminal record.
func abortFrom(line []byte, r abortRecord) (Abort, bool) {
	if r.Type != "assistant" || !r.IsAPIError {
		return Abort{}, false // 直近の実レコードが通常のターン — 中断ではない
	}
	a := Abort{Msg: strings.TrimSpace(AssistantText(line))}
	a.Retryable = classifyAbort(a.Msg, r.Status, r.Error)
	if at, err := time.Parse(time.RFC3339, r.Timestamp); err == nil {
		a.At = at
	}
	return a, true
}

// UsageLimitAbort is AbortInfo narrowed to「利用上限で終わったターン」。ok=true のときだけ
// 上限エピソード（rate_limit_resume.go）を開いてよい。
//
// なぜ転写側にもこの判定が要るか: 上限の形はひとつではない。アカウントの窓に当たると
// claude は /rate-limit-options メニューを出してキー入力待ちで止まる（ペインから読める）が、
// モデル別の上限は**メニューを出さず**、1 行のエラーを書いてターンを完了として畳み、普通の
// 入力欄へ戻る。後者はペインに手掛かりが残らない（画面のエラー行は転写テキストなので、
// その後もずっと残る＝いつの話か言えない — isCodexUpdateMenu の罠と同じ）ので、
// 「今どうなっているか」を答えられるのは転写の末尾だけになる。
//
// retryable な中断（接続断・一時的なレート制限）は上限ではないので落とす。retryableOverrides
// が classifyAbort で先に効くため、"(not your usage limit)" と自称するレコードがここの
// "usage limit" に当たることはない。
//
// kind は**待てば解けるか**を分ける（docs/47 §4-10）。同じ 429 / `error:"rate_limit"` で
// 届くのに、窓（時間）と支出（金額）はその後の一手が正反対になる — 前者は待つ、後者は
// 待っても永久に解けず、増枠かクレジットの追加が要る。
func UsageLimitAbort(sid string) (Abort, LimitKind, bool) {
	a, ok := AbortInfo(sid)
	if !ok || a.Retryable {
		return Abort{}, "", false
	}
	return limitKindOf(a.Msg, a)
}

// limitKindOf is the pure form (コーパステスト用): 文言から上限の種別を決める。
//
// **支出側を先に見る。** 両方に読める文言（"…spend limit… run /usage-credits…"）が来たら、
// 待てば解ける方に倒すのが一番高い誤りになる: 利用者は来ないリセットを待ち、自動再開は
// 同じ 429 を踏み続ける。逆向きの誤り（窓の上限を「増枠が要る」と言う）は、待てば直る
// ものを人が見に来るだけで済む。
func limitKindOf(msg string, a Abort) (Abort, LimitKind, bool) {
	low := strings.ToLower(msg)
	for _, m := range spendMarkers {
		if strings.Contains(low, m) {
			return a, LimitSpend, true
		}
	}
	for _, m := range limitMarkers {
		if strings.Contains(low, m) {
			return a, LimitWindow, true
		}
	}
	return Abort{}, "", false
}

// HealIdle is what the pane-based self-heal does once it has decided the session is
// really back at its ready prompt. It replaces the bare status.Remove that used to sit
// at both heal sites: if the transcript says the turn was CUT OFF, the turn end is a
// real terminal event and has to go through the shared notifier (通知＋docs/30 報告)
// — dropping it on the floor is exactly the bug docs/47 fixes. Anything else (killed+
// resumed, rejected permission, abandoned question) stays a silent heal as before.
//
// MarkTurnEndErr persists idle rather than removing the marker, so the heal condition
// (state != "idle") no longer holds afterwards and a second poll cannot re-report the
// same abort. A duplicate from two concurrent polls is absorbed by handleChatReport's
// disarm.
func HealIdle(sid string) {
	if msg, retryable, ok := AbortedTurn(sid); ok {
		st := agents.TurnFailed // 原因が解消するまで再送しても同じ — 既存の失敗文言へ
		if retryable {
			st = agents.TurnAborted
		}
		agents.MarkTurnEndErr(sid, st, msg)
		return
	}
	status.Remove(sid)
}
