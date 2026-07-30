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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
)

// abortRecord is the subset of a transcript line the abort detector needs.
type abortRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsAPIError  bool   `json:"isApiErrorMessage"`
	Status      int    `json:"apiErrorStatus"`
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
	"prompt is too long", // 会話が長すぎる — /compact なしの再送は無意味
	"credit balance",
	"invalid api key",
	"authentication",
	"unauthorized",
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
}

// classifyAbort splits an API error message into 再送で直る (true) か 原因を直すまで
// 無意味 (false) か。判定不能は false に倒す — 自動再開はしない方が安全側。
// blocked を先に見るのは、利用上限も 429 で届くため（"You've reached your … limit"）
// ステータスコードだけでは一時的なレート制限と区別できないから。
func classifyAbort(msg string, status int) bool {
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
	return abortedTurnFrom(TranscriptLines(sid))
}

// abortedTurnFrom is AbortedTurn's pure half (lines in, verdict out).
func abortedTurnFrom(lines [][]byte) (msg string, retryable, ok bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		var r abortRecord
		if json.Unmarshal(lines[i], &r) != nil {
			continue
		}
		if r.IsSidechain || (r.Type != "user" && r.Type != "assistant") {
			continue // 記帳レコード / サブエージェント — 終端の判定材料にしない
		}
		if r.Type != "assistant" || !r.IsAPIError {
			return "", false, false // 直近の実レコードが通常のターン — 中断ではない
		}
		msg = strings.TrimSpace(AssistantText(lines[i]))
		return msg, classifyAbort(msg, r.Status), true
	}
	return "", false, false
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
