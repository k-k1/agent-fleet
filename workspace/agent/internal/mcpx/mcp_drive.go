package mcpx

import (
	"encoding/json"
	"net/http"
	"net/url"
)

type sendToSessionArgs struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

type answerSessionQuestionArgs struct {
	Name string `json:"name"`
	// Choices are 1-based choice numbers, in question order.
	Choices []int `json:"choices"`
}

type respondSessionPlanArgs struct {
	Name     string `json:"name"`
	Decision string `json:"decision"`
	Feedback string `json:"feedback"`
}

type stopSessionAfterTurnArgs struct {
	Name string `json:"name"`
	// On arms or releases. A POINTER because the zero value of the arming flag has to mean
	// "arm": decoded into a plain bool, a call that omitted it would silently release the arm
	// it was meant to set.
	On *bool `json:"on"`
}

// deleteBranchArgs: Repo names the branch's repo.
type deleteBranchArgs struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
}

// driveTools steer, stop and clean up sessions: the operator's under --write. stop_session,
// stop_session_after_turn and resume_session are also a session's under --fleet-spawn, for its
// own children only (sessionDriveAllowed).
func driveTools() []toolSpec {
	return []toolSpec{
		{
			name: "send_to_session",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "send_to_session",
					"description": "指定セッションにプロンプト（テキスト）を送信して実行させる（末尾に Enter）。停止中なら会話を保持したまま自動で再開してから送る。成功時だけ sent=true を返すため、sent=true を確認するまでは利用者に送信済みと伝えないこと。送信後にそのセッションが入力待ちになる／異常終了すると、この会話に自動で報告が届くのでポーリングは不要（すぐ結果が要る時だけ get_session_status / get_session_output で確認）。利用者が「s7 に○○を伝えて/やらせて」等の作業依頼をした時に呼ぶ。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":   map[string]any{"type": "string", "description": "送信先セッション名（例: s7）"},
							"prompt": map[string]any{"type": "string", "description": "送信するプロンプト本文"},
						},
						"required": []string{"name", "prompt"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントは書き込みツールを許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *sendToSessionArgs) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				if a.Prompt == "" {
					return mcpToolErr(req.ID, "prompt（送信本文）が必要です")
				}
				// P3: sending to a raw shell session runs an arbitrary command — gate it like a shell
				// create when this is an unattended operator turn (agent sessions keep their own guardrails).
				if sessionIsShell(a.Name) {
					if err := bridgeApprovalGate(approvalLabel("send_to_session_shell"), shellSendTarget(a.Name, a.Prompt)); err != nil {
						return mcpToolErr(req.ID, err.Error())
					}
				}
				// confirm (docs/log/38 delivery verification): an operator send is an unattended
				// route, so wait for proof that the turn actually started rather than the 200 that
				// only means the keystrokes went out. If the input was swallowed the Agent repairs
				// itself (re-sends Enter / retypes), and if it is still unconfirmed the tool fails
				// with delivery_unconfirmed — the fix for instructions silently lost to a stopped
				// session (bc5d685e).
				reqBody, _ := json.Marshal(map[string]any{"prompt": a.Prompt, "report_to": convID(), "confirm": true})
				out, resumed, err := AgentSendToSession(a.Name, reqBody)
				if err != nil {
					return mcpToolErr(req.ID, "Agent への送信に失敗しました: "+err.Error())
				}
				result := map[string]any{"sent": true, "resumed": resumed, "session": a.Name}
				if json.Valid([]byte(out)) {
					result["agent_result"] = json.RawMessage(out)
				}
				b, _ := json.Marshal(result)
				return mcpTextResult(req.ID, string(b))
			}),
		},
		{
			name: "answer_session_question",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "answer_session_question",
					"description": "セッションが提示している質問（選択肢フォーム）に回答する。質問と選択肢は get_session_status の questions（または get_session_output）で確認し、原則として選択肢を利用者に提示して意向を確認してから回答すること（質問は本来利用者に向けられたもの。利用者が事前に判断を任せている場合のみ自分で選んでよい）。choices は質問順に 1-based の選択肢番号を1つずつ並べた配列（質問が1つなら要素1つ）。自由入力（Other）や複数選択の質問には使えない（Console から回答してもらう）。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":    map[string]any{"type": "string", "description": "回答先セッション名（例: s7）"},
							"choices": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "質問順の選択肢番号（1-based）。例: 質問1で2番を選ぶなら [2]"},
						},
						"required": []string{"name", "choices"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントは書き込みツールを許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *answerSessionQuestionArgs) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				if len(a.Choices) == 0 {
					return mcpToolErr(req.ID, "choices（質問順の 1-based 選択肢番号の配列）が必要です")
				}
				reqBody, _ := json.Marshal(map[string]any{"choices": a.Choices})
				out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/answer-question", reqBody)
				if err != nil {
					return mcpToolErr(req.ID, "質問への回答に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "respond_session_plan",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "respond_session_plan",
					"description": "セッションが承認待ちで提示しているプラン（実行計画）に応答する（claude セッションのみ）。プラン本文は get_session_status の plan で確認する。decision=approve は承認して実行を開始させる。decision=reject は承認ダイアログを閉じて中断し、feedback を修正指示として送る（改訂プランが再提示される）。原則、利用者の意向（または自動走行モードの報告に含まれる指示）に基づいて使うこと。プランに破壊的・不可逆な操作が含まれる場合は承認前に必ず利用者に確認する。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":     map[string]any{"type": "string", "description": "対象セッション名（例: s7）"},
							"decision": map[string]any{"type": "string", "description": "approve | reject"},
							"feedback": map[string]any{"type": "string", "description": "reject 時の修正指示（推奨。省略すると却下のみで指示なし）"},
						},
						"required": []string{"name", "decision"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントは書き込みツールを許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *respondSessionPlanArgs) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				if a.Decision != "approve" && a.Decision != "reject" {
					return mcpToolErr(req.ID, "decision は approve か reject を指定してください")
				}
				reqBody, _ := json.Marshal(map[string]string{"decision": a.Decision, "feedback": a.Feedback})
				out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/plan-respond", reqBody)
				if err != nil {
					return mcpToolErr(req.ID, "プランへの応答に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "stop_session",
			operator: func() map[string]any {
				return map[string]any{
					// Stopping relays to /halt, which is resumable. /stop is the old name of "delete this
					// session" (it moves it to the trash, ADR 0101) and is deliberately not this tool:
					// deleting is delete_session, which the operator confirms first.
					"name":        "stop_session",
					"description": "指定セッションを停止する（停止中＝再開可能。会話履歴と作業ディレクトリは保持され、resume_session や Console から再開できる）。暴走している・不要になった・リソースを空けたいセッションを畳む時に呼ぶ。実行中の作業は中断され、そのセッションへの未達の自動報告は取り消される。実行前に『どのセッションを止めるか』を一言添えて利用者に確認すること。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "停止するセッション名（list_my_sessions の name）"},
						},
						"required": []string{"name"},
					},
				}
			},
			operatorWrite: true,
			session: func() map[string]any {
				return map[string]any{
					"name": "stop_session",
					"description": "Agent Fleet: stop a session you started, now. Only your own children. " +
						"The stop is resumable - conversation and working copy stay, and resume_session brings it back - " +
						"but whatever it was doing is cut off mid-turn. " +
						"Use it for a child that is looping or working on something you no longer want. If it is simply " +
						"busy with work you still want, use stop_session_after_turn instead. " +
						"Stopping does not free one of your child slots straight away - the user deleting or archiving " +
						"the child does, and a child left stopped expires on its own after a while.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
						},
						"required": []string{"name"},
					},
				}
			},
			refuse: func() string {
				if !writeEnabled() && !mcpFleetSpawnEnabled {
					return "このアシスタントはセッションの停止を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *nameArg) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				if err := sessionDriveAllowed(a.Name); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				// disarm_report: an operator stop means the instruction is cancelled, so swallow the
				// armed one-shot report — otherwise a later resume's completion delivers a stale one.
				//
				// A SESSION's stop does not send it (ADR 0073 decision 10). Disarming says "the
				// instruction is withdrawn", and a parent folding up a child withdraws nothing the
				// operator asked for; if the operator had steered this child, its report is still owed.
				// (The stop still delays that report until the child is resumed — leaving the arm alone
				// is not the same as having no effect.)
				reqBody, _ := json.Marshal(map[string]bool{"disarm_report": !selfReportOnly()})
				out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/halt", reqBody)
				if err != nil {
					return mcpToolErr(req.ID, "セッションの停止に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "stop_session_after_turn",
			operator: func() map[string]any {
				return map[string]any{
					// The counterpart of stop_session for a session that is still working: it arms the
					// stop instead of performing it, so the turn finishes (and its report is delivered)
					// before the session is folded away. docs/log/85.
					"name": "stop_session_after_turn",
					"description": "指定セッションに『今の作業が終わったら停止する』を予約する（docs/log/85）。stop_session と違って即座には止めず、走っているターンが終わってから停止するので、作業が中断されず、そのセッションが返すはずの完了報告も先に届く。" +
						"ファンアウトで複数セッションを走らせ、終わったものから畳んで資源を空けたい時に使う（走行中に stop_session を押すと作業が切れる）。停止は再開可能で会話も残る。" +
						"質問・承認待ちの間や作業が続いている間は停止しない。予約後にそのセッションへ新しい指示を送ると予約は自動的に解除される。on=false で明示的に解除できる。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "対象セッション名（list_my_sessions の name）"},
							"on":   map[string]any{"type": "boolean", "description": "true=予約する（既定）、false=予約を解除する"},
						},
						"required": []string{"name"},
					},
				}
			},
			operatorWrite: true,
			session: func() map[string]any {
				return map[string]any{
					"name": "stop_session_after_turn",
					"description": "Agent Fleet: book a child to stop once it finishes what it is doing. Only your own children. " +
						"Unlike stop_session nothing is cut off: the turn ends, its report goes out, then the session folds " +
						"away. This is the one to use when you fanned work out and want each child to release its memory as " +
						"it finishes. " +
						"It does not stop a session that is waiting on a question or an approval, and sending it new work " +
						"releases the booking. on=false cancels it.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
							"on":   map[string]any{"type": "boolean", "description": "true = book the stop (default), false = cancel it"},
						},
						"required": []string{"name"},
					},
				}
			},
			// Deliberately does NOT disarm the report, which is the whole difference from
			// stop_session: this stop lets the instruction finish, so the report it owes is
			// still owed — and the Agent delivers it before folding the session away
			// (docs/log/85).
			refuse: func() string {
				if !writeEnabled() && !mcpFleetSpawnEnabled {
					return "このアシスタントはセッションの停止を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *stopSessionAfterTurnArgs) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				if err := sessionDriveAllowed(a.Name); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				on := a.On == nil || *a.On
				armBody, _ := json.Marshal(map[string]bool{"on": on})
				out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/stop-after-turn", armBody)
				if err != nil {
					return mcpToolErr(req.ID, "停止予約の更新に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "resume_session",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "resume_session",
					"description": "停止中のセッションを再開する（会話履歴を引き継いで再起動。稼働中なら何もしない）。stop_session で止めたセッションや停止中のセッションを再び動かす時に呼ぶ。再開後に作業を指示するには send_to_session を使う。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "再開するセッション名（list_my_sessions の name）"},
						},
						"required": []string{"name"},
					},
				}
			},
			operatorWrite: true,
			session: func() map[string]any {
				return map[string]any{
					"name": "resume_session",
					"description": "Agent Fleet: restart a stopped child, keeping its conversation. Only your own children. " +
						"Call it when you need more from a child you stopped. It only brings the session back - to give it " +
						"work, send it a peer message afterwards. A running session is left alone.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "minLength": 1, "description": "Child session name"},
						},
						"required": []string{"name"},
					},
				}
			},
			refuse: func() string {
				if !writeEnabled() && !mcpFleetSpawnEnabled {
					return "このアシスタントはセッションの再開を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *nameArg) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				if err := sessionDriveAllowed(a.Name); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/start", nil)
				if err != nil {
					return mcpToolErr(req.ID, "セッションの再開に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "archive_session",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "archive_session",
					"description": "終わったセッションをアーカイブして普段の一覧から隠す（会話履歴は残り、Console から復元できる＝可逆）。list_cleanup_candidates で action=archive_session の候補を片付ける時に使う。稼働中の作業は中断されるので、完了済みかどうかを含め実行前に利用者へ確認すること。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "アーカイブするセッション名（list_my_sessions / list_cleanup_candidates の name）"},
						},
						"required": []string{"name"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはセッションのアーカイブを許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *nameArg) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				out, err := AgentPOST("/sessions/"+url.PathEscape(a.Name)+"/archive", nil)
				if err != nil {
					return mcpToolErr(req.ID, "セッションのアーカイブに失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "delete_worktree",
			operator: func() map[string]any {
				return map[string]any{
					"name": "delete_worktree",
					"description": "不要になった worktree（作業コピー）を削除する。list_cleanup_candidates で action=delete_worktree の候補（マージ済みクリーン＝safe、未マージだがクリーン＝review）を片付ける時に使う。" +
						"未コミット/未pushの変更がある worktree は保護のため削除できない（keep 候補。Console で強制削除するよう案内する）。その worktree の停止中の AI セッションはアーカイブ（棚）へ移り、shell/ssm はごみ箱へ入る（どちらも復元できる。会話は消えない）。ローカルの作業コピーだけが消え、履歴・リモート・ブランチは残る。削除した worktree もごみ箱に記録され、restore_cleanup_archive で同じパスに作り直せる。破壊的操作なので、どの worktree を消すかを一言添えて実行前に必ず利用者へ確認すること。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "削除する worktree の名前（list_cleanup_candidates の worktree 候補の id）"},
						},
						"required": []string{"name"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントは worktree の削除を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *nameArg) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（worktree 名）が必要です")
				}
				if err := bridgeApprovalGate(approvalLabel("delete_worktree"), a.Name); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				// The Agent shelves the stopped AI sessions in it and trashes its shell / ssm on every
				// delete (ADR 0101 decision 4), so no flag is sent. No force: a dirty or ahead worktree
				// stays protected and is refused by the Agent, which returns the reason (push first,
				// or force it from the Console).
				out, err := agentDo(http.MethodDelete, "/repos/"+url.PathEscape(a.Name), nil)
				if err != nil {
					return mcpToolErr(req.ID, "worktree の削除に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "delete_session",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "delete_session",
					"description": "アーカイブ済み／不要なセッションを完全に削除して容量を回収する（会話ログ jsonl も消す）。archive_session が一覧から隠すだけなのに対し、これは実体を消す。消す前に jsonl とメタを gz アーカイブ（安全網）へ退避するので復元可能（list_cleanup_archives → restore_cleanup_archive）。稼働中のセッションは削除できない（先に停止）。list_cleanup_candidates で action=delete_session の候補を片付ける時に使う。不可逆に近い操作なので、どのセッションを消すかを明示して実行前に必ず利用者へ確認すること。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string", "description": "削除するセッション名（list_cleanup_candidates / list_my_sessions の name）"},
						},
						"required": []string{"name"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはセッションの削除を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *nameArg) []byte {
				if a.Name == "" {
					return mcpToolErr(req.ID, "name（セッション名）が必要です")
				}
				if err := bridgeApprovalGate(approvalLabel("delete_session"), a.Name); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				// Every delete moves the meta and jsonl to the gz trash before removing them (ADR 0101),
				// so it stays restorable. A running session is refused (no stop=1: stopping is a
				// separate, confirmed step).
				out, err := agentDo(http.MethodDelete, "/sessions/"+url.PathEscape(a.Name), nil)
				if err != nil {
					return mcpToolErr(req.ID, "セッションの削除に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "delete_branch",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "delete_branch",
					"description": "マージ済みのローカルブランチを削除する（worktree を消した後に残る temp/* 等の掃除）。マージ済み（親に取り込み済み）のみ削除でき、未マージのブランチは固有コミットを失わないよう拒否される（push/マージするか Console で対応するよう案内）。消す前にブランチ名と SHA を gz アーカイブへ退避するので復元可能。list_cleanup_candidates で action=delete_branch の候補（repo=id, branch）を片付ける時に使う。実行前に対象を明示して利用者へ確認すること。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"repo":   map[string]any{"type": "string", "description": "ブランチのあるリポジトリ名（list_cleanup_candidates の branch 候補の id）"},
							"branch": map[string]any{"type": "string", "description": "削除するブランチ名（list_cleanup_candidates の branch フィールド）"},
						},
						"required": []string{"repo", "branch"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはブランチの削除を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *deleteBranchArgs) []byte {
				if a.Repo == "" || a.Branch == "" {
					return mcpToolErr(req.ID, "repo（リポジトリ名）と branch（ブランチ名）が必要です")
				}
				if err := bridgeApprovalGate(approvalLabel("delete_branch"), a.Repo+" / "+a.Branch); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				out, err := agentDo(http.MethodDelete,
					"/repos/"+url.PathEscape(a.Repo)+"/branch?branch="+url.QueryEscape(a.Branch), nil)
				if err != nil {
					return mcpToolErr(req.ID, "ブランチの削除に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "restore_cleanup_archive",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "restore_cleanup_archive",
					"description": "掃除で退避したアーカイブ（gz 安全網）を復元する。delete_session なら会話ログ jsonl とセッション一覧の行が戻り、delete_branch ならブランチが再作成される。消しすぎた・やっぱり要る時に使う。id は list_cleanup_archives で取得する。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "string", "description": "復元するアーカイブ id（list_cleanup_archives で取得）"},
						},
						"required": []string{"id"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはアーカイブの復元を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *idArg) []byte {
				if a.ID == "" {
					return mcpToolErr(req.ID, "id（アーカイブ id）が必要です")
				}
				out, err := AgentPOST("/cleanup/archives/"+url.PathEscape(a.ID)+"/restore", nil)
				if err != nil {
					return mcpToolErr(req.ID, "アーカイブの復元に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "purge_cleanup_archive",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "purge_cleanup_archive",
					"description": "掃除で退避したアーカイブを完全に削除して容量を回収する（復元不可になる）。もう戻す必要がないと確認できたアーカイブにのみ使う。id は list_cleanup_archives で取得する。完全削除なので実行前に利用者へ確認すること。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{"type": "string", "description": "完全削除するアーカイブ id（list_cleanup_archives で取得）"},
						},
						"required": []string{"id"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはアーカイブの完全削除を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *idArg) []byte {
				if a.ID == "" {
					return mcpToolErr(req.ID, "id（アーカイブ id）が必要です")
				}
				if err := bridgeApprovalGate(approvalLabel("purge_cleanup_archive"), a.ID); err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				out, err := agentDo(http.MethodDelete, "/cleanup/archives/"+url.PathEscape(a.ID), nil)
				if err != nil {
					return mcpToolErr(req.ID, "アーカイブの完全削除に失敗しました: "+err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
	}
}
