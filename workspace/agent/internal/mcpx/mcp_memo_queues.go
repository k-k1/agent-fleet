package mcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// memoQueueTools are the operator's read-only view of memo auto-run categories (ADR 0137,
// addendum 2026-11-21). They call the CP's /internal/memo-queues face with AF_MEMO_TOKEN.
//
// Read-only on purpose. AF_MEMO_TOKEN is in the environment of every session of the workspace,
// so nothing on that face may write a queue's settings, release memos, return a run's memos,
// start a run (it skips the window and the daily cap), resume a paused queue or end a run: a
// queue run that read an injected memo could do each of them over HTTP, whatever --write and a
// tool description say. Those are the person's controls in the Console. The tools tell the
// operator which one to press.
// TestMemoQueueToolsAreReadOnly holds this list.
func memoQueueTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_memo_queues",
			operator: func() map[string]any {
				return map[string]any{
					"name": "list_memo_queues",
					"description": "メモの自動実行カテゴリ（キュー）と、その設定を読む（読み取り専用。ADR 0137）。各キューは categoryId / repo / category / 常設の指示（instruction。無人のセッションに渡る文）/ enabled（自動で動かすか）/ maxActive（同時に動かす数）/ openCron・openMinutes・tz・openAfter・maxFiresPerDay（動く時間帯と日次上限）/ continuationMax / spendCapUsd / paused・pausedReason（一時停止）などを持つ。" +
						"waiting は待機メモの件数ではない。待機メモがあるのに今は何も始まらないキューにだけ付く、その理由の説明で、実行できるキューや待機メモのないキューには付かない（無いことは待機が 0 件の意味ではない）。待機中のメモの件数と中身は list_memos の category / state で数える。" +
						"利用者に「自動実行は今どうなっている?」と聞かれた時や、get_memo_queue_runs で対象の categoryId を選ぶ前に呼ぶ。" +
						"このツールと get_memo_queue_runs は読むだけで、次のことはどのツールでもできない: 設定の変更（指示の書き換え、自動実行の on/off、同時数・時間帯・続きの上限・費用の上限）、自動実行の解除、メモの承認（解放）、今すぐ実行、一時停止の再開、実行の終了、実行のメモのキューへの返却。これらは利用者が Console で行う。" +
						"利用者が設定の変更を望んだら、変更後の設定の全体（指示の全文を省略せず、変える項目の現在値と新しい値）をチャットに示し、Console の「メモ」パネル → そのカテゴリの「自動実行の設定」で自分で保存してもらうよう案内する。「今すぐ実行」「再開」、実行の行の「終了」「キューに戻す」も、どれを押すかを案内するだけにする。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *noArgs) []byte {
				return memoQueueCall(req, http.MethodGet, "/internal/memo-queues")
			}),
		},
		{
			name: "get_memo_queue_runs",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "get_memo_queue_runs",
					"description": "指定したキューの実行履歴（新しい順）を返す。各行は run の id・状態・結果・開始したセッション・対象のメモなど。自動実行がちゃんと動いているか／失敗や一時停止していないかを確認する時に呼ぶ（読み取り専用）。再開が要るなら、止まった原因（失敗した run の結果）を伝えて Console の「再開」を案内する。実行を終えたい時は、対象の run とそのセッションを示して Console の実行の行の「終了」を案内する。他の session の hand-back の理由はここには含まれない（Console だけ）。categoryId は list_memo_queues で取得する。",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"categoryId": map[string]any{"type": "string", "description": "キューのカテゴリ id（list_memo_queues で取得）"}},
						"required":   []string{"categoryId"},
					},
				}
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *memoQueueArg) []byte {
				if a.CategoryID == "" {
					return mcpToolErr(req.ID, "categoryId（キューのカテゴリ id）が必要です")
				}
				return memoQueueCall(req, http.MethodGet, "/internal/memo-queues/"+url.PathEscape(a.CategoryID)+"/deliveries")
			}),
		},
	}
}

type memoQueueArg struct {
	CategoryID string `json:"categoryId"`
}

func memoQueueCall(req mcpReq, method, path string) []byte {
	out, err := cpMemoDo(method, path, nil)
	if err != nil {
		return mcpToolErr(req.ID, memoQueueErr(err))
	}
	if out == "" {
		out = `{"ok":true}`
	}
	return mcpTextResult(req.ID, out)
}

// memoQueueReasons says in a sentence what the CP's error codes of the queue routes mean to the
// operator; the CP's own message follows, so nothing it knows is lost.
var memoQueueReasons = map[string]string{
	"not_found":            "そのキューは見つかりません（categoryId を list_memo_queues で確かめ直してください）",
	"memo_queues_disabled": "このテナントではメモのキューが有効になっていません（管理者が有効にします）",
}

// memoQueueErr turns a CP error into text the model can act on. An answer that is not the CP's
// {"error":{"code","message"}} shape, or a code not listed, passes through as it came.
func memoQueueErr(err error) string {
	var ce *cpMemoError
	if !errors.As(err, &ce) {
		return err.Error()
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(ce.Body), &body) != nil || body.Error.Code == "" {
		return err.Error()
	}
	reason, ok := memoQueueReasons[body.Error.Code]
	if !ok {
		return err.Error()
	}
	return fmt.Sprintf("%s（%d %s: %s）", reason, ce.Status, body.Error.Code, body.Error.Message)
}
