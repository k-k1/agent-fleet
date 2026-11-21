package mcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// memoQueueTools are the operator's view of memo auto-run categories (ADR 0137, addendum
// 2026-11-21). They call the CP's /internal/memo-queues face with AF_MEMO_TOKEN.
//
// What is deliberately not here, and not on that face either: writing a category's settings
// (the standing instruction, run by itself, slots, window, follow-up limit, spend limit),
// turning auto-run off, releasing memos, and returning a run's memos to the queue. AF_MEMO_TOKEN
// is in the environment of every session of the workspace, so each of these would be reachable
// by a queue run that read an injected memo; the standing instruction is the user's own words
// to unattended sessions. Settings are the person's act in the Console, like the release.
// TestMemoQueueToolsOfferNoSettingsWriteOrRelease holds this list.
func memoQueueTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_memo_queues",
			operator: func() map[string]any {
				return map[string]any{
					"name": "list_memo_queues",
					"description": "メモの自動実行カテゴリ（キュー）と、その設定の一覧を返す（ADR 0137）。各キューは categoryId / 常設の指示（instruction）/ 自動で動かすか（enabled）/ 同時に動かす数 / 動く時間帯 / 続きの上限 / 費用の上限 / 一時停止の状態 / waiting（待機中の件数）などを持つ。" +
						"利用者に「自動実行は今どうなっている?」と聞かれた時や、run_memo_queue_now / resume_memo_queue / get_memo_queue_runs で対象の categoryId を選ぶ前に呼ぶ。" +
						"設定の変更（指示の書き換え、自動実行の on/off、同時数・時間帯・続きの上限・費用の上限、自動実行の解除）とメモの承認（解放）はこのツールでも他のツールでもできない。" +
						"利用者が変更を望んだら、変更後の設定の全体（指示の全文、変える項目の現在値と新しい値）をチャットに示し、Console の「メモ」パネル → そのカテゴリの自動実行の設定で自分で保存・承認してもらうよう案内すること。指示は無人のセッションに渡る文なので、全文を省略せずに見せる。",
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
					"description": "指定したキューの実行履歴（新しい順）を返す。各行は run の id・状態・結果・開始したセッション・対象のメモなど。自動実行がちゃんと動いているか／失敗や一時停止していないかを確認する時や、stop_memo_queue_run の対象 id を選ぶ前に呼ぶ。categoryId は list_memo_queues で取得する。",
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
		{
			name: "run_memo_queue_now",
			operator: func() map[string]any {
				return map[string]any{
					"name": "run_memo_queue_now",
					"description": "指定したキューを今すぐ1回動かす（Console の「今すぐ実行」と同じ）。待機中（承認済み）のメモを取り、セッションを起動して、キューの常設の指示どおりに仕事をさせる。次のスケジューラ tick（最大約1分後）で送られる。「自動で動かす」が off でも動く。承認待ちのメモは動かない。" +
						"無人でセッションを起動する操作なので、実行前に必ず利用者へ「どのキューを・何件のメモで・どの指示で動かすか」を示して了解を得ること（list_memo_queues の指示と waiting を読み上げる）。" +
						"キューに指示が無い、空きが無い、待機中のメモが無い、テナントがキューを有効にしていない場合は理由つきで断られる。",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"categoryId": map[string]any{"type": "string", "description": "キューのカテゴリ id（list_memo_queues で取得）"}},
						"required":   []string{"categoryId"},
					},
				}
			},
			operatorWrite: true,
			refuse:        refuseMemoQueueDrive,
			handle: handler(func(req mcpReq, p toolCallParams, a *memoQueueArg) []byte {
				if a.CategoryID == "" {
					return mcpToolErr(req.ID, "categoryId（キューのカテゴリ id）が必要です")
				}
				return memoQueueCall(req, http.MethodPost, "/internal/memo-queues/"+url.PathEscape(a.CategoryID)+"/fire")
			}),
		},
		{
			name: "resume_memo_queue",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "resume_memo_queue",
					"description": "失敗が続いて自動で一時停止したキューを再開する（失敗の数え直しも今から）。止まった原因（get_memo_queue_runs の失敗した run）を利用者に伝え、直ったことを確かめてから、了解を得て呼ぶこと。止まっていないキューに呼んでも何も変わらない。categoryId は list_memo_queues で取得。",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"categoryId": map[string]any{"type": "string", "description": "キューのカテゴリ id"}},
						"required":   []string{"categoryId"},
					},
				}
			},
			operatorWrite: true,
			refuse:        refuseMemoQueueDrive,
			handle: handler(func(req mcpReq, p toolCallParams, a *memoQueueArg) []byte {
				if a.CategoryID == "" {
					return mcpToolErr(req.ID, "categoryId（キューのカテゴリ id）が必要です")
				}
				return memoQueueCall(req, http.MethodPost, "/internal/memo-queues/"+url.PathEscape(a.CategoryID)+"/resume")
			}),
		},
		{
			name: "stop_memo_queue_run",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "stop_memo_queue_run",
					"description": "動いている run を終える（run は「終了」で閉じ、そのセッションは停止される。アーカイブはされない）。run の枠が空く。まだセッションを起動していない run は断られる（tick が自分で片付ける）。止めると途中の仕事が中断されるので、対象の run とセッションを利用者に示して了解を得てから呼ぶこと。categoryId は list_memo_queues、id は get_memo_queue_runs で取得。メモをキューに戻す操作はこのツールにもなく、Console で利用者が行う。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"categoryId": map[string]any{"type": "string", "description": "キューのカテゴリ id"},
							"id":         map[string]any{"type": "string", "description": "run の id（get_memo_queue_runs で取得）"},
						},
						"required": []string{"categoryId", "id"},
					},
				}
			},
			operatorWrite: true,
			refuse:        refuseMemoQueueDrive,
			handle: handler(func(req mcpReq, p toolCallParams, a *memoQueueRunArg) []byte {
				if a.CategoryID == "" || a.ID == "" {
					return mcpToolErr(req.ID, "categoryId（キューのカテゴリ id）と id（run の id）が必要です")
				}
				return memoQueueCall(req, http.MethodPost, "/internal/memo-queues/"+url.PathEscape(a.CategoryID)+"/deliveries/"+url.PathEscape(a.ID)+"/end")
			}),
		},
	}
}

type memoQueueArg struct {
	CategoryID string `json:"categoryId"`
}

type memoQueueRunArg struct {
	CategoryID string `json:"categoryId"`
	ID         string `json:"id"`
}

func refuseMemoQueueDrive() string {
	if !writeEnabled() {
		return "このアシスタントはメモの自動実行の操作を許可されていません"
	}
	return ""
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
	"not_found":                  "そのキュー／run は見つかりません（id を list_memo_queues / get_memo_queue_runs で確かめ直してください）",
	"delivery_not_open":          "その run はまだセッションを起動していないので終えられません（少し待つと tick が片付けます）",
	"memo_queues_disabled":       "このテナントではメモのキューが有効になっていません（管理者が有効にします）",
	"memo_queue_no_instruction":  "このキューには指示がありません。Console の設定で指示を書いてもらってください",
	"memo_queue_bad_repo":        "キューのリポジトリが repos 配下のフォルダではありません。Console の設定を直してもらってください",
	"memo_queue_no_slot":         "同時に動かす数の上限に達しています。動いている run が終わるのを待つか、stop_memo_queue_run で終えます",
	"memo_queue_member_limit":    "利用者全体の同時実行の上限に達しています",
	"memo_queue_nothing_pending": "待機中（承認済み）のメモがありません。承認待ちのメモは、利用者が承認カードで承認するまで動きません",
	"memo_queue_item_invalid":    "待機中のメモの作業項目が使えない状態です。内容は CP のメッセージを参照してください",
	"quota_sessions":             "セッション数の上限に達しています",
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
