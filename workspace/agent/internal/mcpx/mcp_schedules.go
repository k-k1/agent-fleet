package mcpx

import (
	"net/http"
	"net/url"
)

// scheduleTools are the operator's scheduled runs, which live in the CP store (docs/log/38) and
// relay to its /internal/schedules bridge, authenticated by AF_SCHEDULE_TOKEN. Reading them is
// af_read's; registering and driving them is --write's.
func scheduleTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_schedules",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_schedules",
					"description": "定時実行スケジュールの一覧を返す（docs/log/38）。各スケジュールは id / spec_kind(cron|interval|once) / spec / spec_label(登録時の自然言語) / tz / enabled / next_run(次回発火 UTC) / next_run_local(tz でのわかりやすい表記) / last_run / last_status / prompt などを持つ。利用者に「今どんな定時タスクがある?」と聞かれた時や、update_schedule / delete_schedule / pause_schedule / run_schedule_now で対象 id を選ぶ前に呼ぶ。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				out, err := CPScheduleDo(http.MethodGet, "/internal/schedules", nil)
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
		{
			name: "get_schedule_runs",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "get_schedule_runs",
					"description": "指定スケジュールの実行履歴（最新50件）を返す。各行は fired_at（発火時刻 UTC）と status（fired / skipped_* / error:...）。定時タスクがちゃんと動いているか／失敗していないかを確認する時に呼ぶ。id は list_schedules で取得する。",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id（list_schedules で取得）"}},
						"required":   []string{"id"},
					},
				}
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				if a.ID == "" {
					return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
				}
				out, err := CPScheduleDo(http.MethodGet, "/internal/schedules/"+url.PathEscape(a.ID)+"/runs", nil)
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
		{
			name: "create_schedule",
			operator: func() map[string]any {
				return map[string]any{
					"name": "create_schedule",
					"description": "定時実行スケジュールを登録する（docs/log/38）。指定時刻に、必要なら停止中のワークスペースを起こして新規セッションを起動し、prompt を最初のタスクとして投入する。report=true を指定した時だけ完了報告がこの会話に届く（既定 false=報告しない。実行履歴と失敗通知は report と無関係に残る）。利用者が報告を求めたら report=true にする。" +
						"stop_after_run=true にすると、そのセッションは指示をやり切った時点で自動停止する（報告を先に届けてから停止・再開可能）。無人の発火が残したセッションがその後ずっと生きたままメモリを抱えるのを防げるので、定時実行では基本的に付けてよい（既定 false=起動したまま）。ワークスペース自体が止まる時刻は変わらない＝課金が減るとは説明しないこと。" +
						"利用者の自然言語（「毎朝9時」「平日夕方6時」「6時間おき」等）は、あなたが構造化 spec に翻訳して渡すこと: spec_kind=cron なら spec は5フィールドの cron 式（分 時 日 月 曜日・曜日は0=日曜）、interval なら spec は秒数（最小60）、once なら spec は RFC3339 の絶対時刻。tz は IANA タイムゾーン（例 Asia/Tokyo）で cron/once の評価基準（DST 込み）。" +
						"登録すると解釈した spec と next_run_local（次回発火の具体日時）が返るので、必ず利用者に読み上げて確認する（例『毎日 09:00 JST に実行、次回は 7/23 09:00 でよいですか?』）。元の自然言語表現は spec_label に入れておくと一覧で人に見せられる。" +
						"prompt には固定メタ変数 {{date}} {{time}} {{datetime}} {{tz}} {{schedule_id}} {{schedule_label}} {{last_run}} を埋め込め、発火時に置換される（未定義の変数はそのまま残る）。" +
						"session_mode=reuse を指定すると、毎回新規ではなく同一の長寿命セッションへ prompt を送り会話文脈を継続できる（既定は new）。reuse_target に既存セッション名を渡せばそこへ送り、省略すればスケジュール専用セッションを自動作成し rotation（ローテーション設定）で作り直す。reuse×自動作成では kind/model/repo は最初の作成時のみ使われ、以後は既存セッション側が正。" +
						"session_mode=assistant を指定すると、セッションではなく【アシスタント会話】に1ターン投入する（アシスタント発火）。reuse_target に会話の slug（a始まり7字・list_schedules の履歴や Console で確認可）を渡せばその会話へ、省略すればこのオペレーター会話（owner_conv）に投入される＝「毎朝オペレーターに◯◯させる」が追加設定なしで成立。repo/agent_kind/model は無視（会話側の設定が正）。実行中の会話への発火は skipped_overlap になる。" +
						"注意: 停止中WSを無人で起こして agent を回す強力な操作なので、登録前に必ず利用者へ内容（何時・何を・どのリポジトリ、reuse なら継続 or 新規かも）を確認すること。reuse は過去の会話が文脈に残り続ける点も踏まえて確認する。" +
						"応答に warning フィールドがあれば（このデプロイでスケジューラが無効等）、その内容を必ず利用者に伝えること。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"spec_kind":             map[string]any{"type": "string", "description": "cron | interval | once"},
							"spec":                  map[string]any{"type": "string", "description": "cron 式（分 時 日 月 曜日）/ 秒数（interval・最小60）/ RFC3339 絶対時刻（once）"},
							"tz":                    map[string]any{"type": "string", "description": "IANA タイムゾーン（cron/once の評価基準。例 Asia/Tokyo。省略時 UTC）"},
							"spec_label":            map[string]any{"type": "string", "description": "元の自然言語表現（表示用・任意。例『毎朝9時』）"},
							"prompt":                map[string]any{"type": "string", "description": "発火時にセッションへ投入するタスク文（必須）"},
							"agent_kind":            map[string]any{"type": "string", "description": "エージェント種別（任意。claude 既定 | codex | opencode | copilot）"},
							"model":                 map[string]any{"type": "string", "description": "モデル上書き（任意）"},
							"repo":                  map[string]any{"type": "string", "description": "作業ディレクトリ（任意。list_repos の path）"},
							"wake_policy":           map[string]any{"type": "string", "description": "停止中WSの扱い（任意。wake 既定=起こす | skip=見送り | catch_up）"},
							"session_mode":          map[string]any{"type": "string", "description": "new（既定・毎回新規セッション）| reuse（同一の長寿命セッションへ毎回送信し文脈を継続）| assistant（アシスタント会話へ1ターン投入）"},
							"reuse_target":          map[string]any{"type": "string", "description": "reuse 時: 送信先の既存セッション名（list_my_sessions の name）。省略でスケジュール専用セッションを自動作成し rotation 対象にする。assistant 時: 対象会話の slug（a始まり7字）。省略でこのオペレーター会話に投入"},
							"rotation":              map[string]any{"type": "string", "description": "reuse×自動作成時のローテーション設定（JSON文字列。例 {\"every_runs\":20,\"after\":\"7d\",\"calendar\":\"weekly\"}）。every_runs=N発火ごと / after=経過(7d,12h,30m 等) / calendar=daily|weekly|monthly のどれか成立で新品に作り直す。weekly は週境界=「月曜は新セッション」。省略で作り直さない"},
							"missing_target_policy": map[string]any{"type": "string", "description": "reuse×reuse_target 時のみ。対象セッションが消えていた場合（recreate 既定=作り直す | fail=失敗通知で止める）"},
							"overlap_policy":        map[string]any{"type": "string", "description": "reuse 時のみ。前回実行が走行中に次が来た場合（skip 既定=見送り | queue=キュー投入 | restart=中断して送る）"},
							"report":                map[string]any{"type": "boolean", "description": "完了報告をこの会話に届けるか（任意。既定 false=報告しない。assistant モードでは無関係=投入自体が会話に届く）"},
							"stop_after_run":        map[string]any{"type": "boolean", "description": "実行が終わったらそのセッションを停止するか（任意。既定 false=起動したまま。報告は停止前に届く。再開可能。assistant モードでは無関係=セッションを使わない）"},
							"deliver_to":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "report=true 時の届け先（任意・複数可）: operator（既定=この会話）| notifications | discord | slack。bridge は利用者が接続済みの場合だけ"},
							"silent":                map[string]any{"type": "boolean", "description": "最終回答が [SILENT] だけなら何も届けない（監視向け・任意。失敗は必ず通知）"},
						},
						"required": []string{"spec_kind", "spec", "prompt"},
					},
				}
			},
			operatorWrite: true,
			ownArgs:       true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはスケジュールの登録を許可されていません"
				}
				return ""
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				// Route completion reports back to THIS operator conversation (docs/log/30): stamp
				// owner_conv = the operator's own conv id, overriding any client-supplied value.
				out, err := CPScheduleDo(http.MethodPost, "/internal/schedules", WithOwnerConv(p.Args, convID()))
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
		{
			name: "update_schedule",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "update_schedule",
					"description": "既存スケジュール（id 指定）を編集する。渡したフィールドだけ変わり、省略した項目はそのまま。spec/spec_kind/tz を変えると次回発火が再計算される。id は list_schedules で取得。spec を変える時は create_schedule と同じ翻訳ルールに従うこと。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":                    map[string]any{"type": "string", "description": "スケジュール id（list_schedules で取得）"},
							"spec_kind":             map[string]any{"type": "string", "description": "cron | interval | once（任意）"},
							"spec":                  map[string]any{"type": "string", "description": "新しい spec（任意）"},
							"tz":                    map[string]any{"type": "string", "description": "新しいタイムゾーン（任意）"},
							"spec_label":            map[string]any{"type": "string", "description": "新しい自然言語ラベル（任意）"},
							"prompt":                map[string]any{"type": "string", "description": "新しいプロンプト（任意）"},
							"agent_kind":            map[string]any{"type": "string", "description": "新しいエージェント種別（任意）"},
							"model":                 map[string]any{"type": "string", "description": "新しいモデル（任意）"},
							"repo":                  map[string]any{"type": "string", "description": "新しい作業ディレクトリ（任意）"},
							"wake_policy":           map[string]any{"type": "string", "description": "新しい wake_policy（任意）"},
							"session_mode":          map[string]any{"type": "string", "description": "new | reuse | assistant（任意）"},
							"reuse_target":          map[string]any{"type": "string", "description": "reuse の送信先セッション名 / assistant の会話 slug（任意・空で自動作成／オペレーター会話に戻す）"},
							"rotation":              map[string]any{"type": "string", "description": "ローテーション設定 JSON（任意・空で無効化）。create_schedule と同じ形式"},
							"missing_target_policy": map[string]any{"type": "string", "description": "recreate | fail（任意）"},
							"overlap_policy":        map[string]any{"type": "string", "description": "skip | queue | restart（任意・reuse 時）"},
							"report":                map[string]any{"type": "boolean", "description": "完了報告をオペレーター会話に届けるか（任意。false=報告しない）"},
							"stop_after_run":        map[string]any{"type": "boolean", "description": "実行が終わったらセッションを停止するか（任意。false=起動したまま）"},
							"deliver_to":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "届け先（任意）: operator | notifications | discord | slack"},
							"silent":                map[string]any{"type": "boolean", "description": "[SILENT] だけの回答を届けない（任意）"},
						},
						"required": []string{"id"},
					},
				}
			},
			operatorWrite: true,
			ownArgs:       true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはスケジュールの編集を許可されていません"
				}
				return ""
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				if a.ID == "" {
					return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
				}
				out, err := CPScheduleDo(http.MethodPatch, "/internal/schedules/"+url.PathEscape(a.ID), []byte(p.Args))
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
		{
			name: "delete_schedule",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "delete_schedule",
					"description": "スケジュールを id で削除する。id は list_schedules で取得する。",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id（list_schedules で取得）"}},
						"required":   []string{"id"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはスケジュールの削除を許可されていません"
				}
				return ""
			},
			handle: func(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
				if a.ID == "" {
					return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
				}
				out, err := CPScheduleDo(http.MethodDelete, "/internal/schedules/"+url.PathEscape(a.ID), nil)
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpTextResult(req.ID, out)
			},
		},
		{name: "pause_schedule", operator: func() map[string]any {
			return map[string]any{
				"name":        "pause_schedule",
				"description": "スケジュールを一時停止する（発火しなくなる。定義は残る）。id は list_schedules で取得。再開は resume_schedule。",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id"}},
					"required":   []string{"id"},
				},
			}
		}, operatorWrite: true, refuse: refuseScheduleDrive, handle: mcpScheduleAction},
		{name: "resume_schedule", operator: func() map[string]any {
			return map[string]any{
				"name":        "resume_schedule",
				"description": "一時停止したスケジュールを再開する（次回発火を今から再計算）。id は list_schedules で取得。",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id"}},
					"required":   []string{"id"},
				},
			}
		}, operatorWrite: true, refuse: refuseScheduleDrive, handle: mcpScheduleAction},
		{name: "run_schedule_now", operator: func() map[string]any {
			return map[string]any{
				"name":        "run_schedule_now",
				"description": "スケジュールを今すぐ発火させる（動作確認用）。定時発火と同じ経路（wake ポリシー・冪等・keep-alive）を通る。次のスケジューラ tick（最大約1分後）で実行される。停止中（pause 済み）のスケジュールは先に resume すること。id は list_schedules で取得。",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id"}},
					"required":   []string{"id"},
				},
			}
		}, operatorWrite: true, refuse: refuseScheduleDrive, handle: mcpScheduleAction},
	}
}

func refuseScheduleDrive() string {
	if !writeEnabled() {
		return "このアシスタントはスケジュールの操作を許可されていません"
	}
	return ""
}

// mcpScheduleAction is pause_schedule, resume_schedule and run_schedule_now.
func mcpScheduleAction(req mcpReq, p toolCallParams, a *mcpCallArgs) []byte {
	if a.ID == "" {
		return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
	}
	action := map[string]string{"pause_schedule": "pause", "resume_schedule": "resume", "run_schedule_now": "run-now"}[p.Name]
	out, err := CPScheduleDo(http.MethodPost, "/internal/schedules/"+url.PathEscape(a.ID)+"/"+action, nil)
	if err != nil {
		return mcpToolErr(req.ID, err.Error())
	}
	return mcpTextResult(req.ID, out)
}
