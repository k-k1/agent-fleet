package mcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorelay"
)

// memoTools are the memo queue, which lives in the CP store, not the Agent. list_memos, add_memo
// and update_memo go through the Agent's relay (memorelay, ADR 0137 §3), which stamps the
// calling session on a create; delete_memo and flush_memos, which only the operator gets, still
// call the CP's /internal/memos bridge with AF_MEMO_TOKEN. list_memos is read-only (available to
// af_read too); the mutating ones require --write, except that a session may add to its user's
// queue and correct what is in it.
func memoTools() []toolSpec {
	return []toolSpec{
		{
			name: "list_memos",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "list_memos",
					"description": "メモキュー（溜めて一括でセッションへ送るメモ）の一覧を返す。未送信＋保持期間内の送信済みを含む。memos の各メモは id/repo/category/kind(file|text)/body/refPath/state を持ち、categories のうち queue=true のものはキュー（メモがセッションへの仕事になる）。利用者に「今どんなメモが溜まっている?」と聞かれた時や、flush_memos / update_memo / delete_memo で対象の id を選ぶ前に呼ぶ。",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			session: func() map[string]any {
				return map[string]any{
					"name": "list_memos",
					"description": "Agent Fleet: list your user's memo queue - what they call their \"memo\": the notes " +
						"they collect and later send into a session in one batch. " +
						"Each memo has id / repo / category / kind (file|text) / body / refPath / state; a category " +
						"with queue=true is a queue, whose notes are work for a session. " +
						"Call it before add_memo to see whether the point is already queued, and to match the repo and " +
						"category labels already in use.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
				}
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *noArgs) []byte {
				out, err := agentGET("/memos")
				if err != nil {
					return mcpToolErr(req.ID, memoRelayErr(err))
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "add_memo",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "add_memo",
					"description": "メモキューに1件追加する。kind=text は body（メモ本文）、kind=file は refPath（~/repos/... パス）が必須で body は任意コメント。repo（''=共通/未分類）と category（サブプロジェクトの自由ラベル）で仕分ける。チャット中に出た TODO・依頼・後で渡したい対象を溜めておく時に呼ぶ。キュー（queue=true）のカテゴリに入れたメモはセッションへの仕事になり、利用者が解放するまで待つ。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind":     map[string]any{"type": "string", "description": "text | file"},
							"body":     map[string]any{"type": "string", "description": "メモ本文（kind=text）またはコメント（kind=file）"},
							"refPath":  map[string]any{"type": "string", "description": "~/repos/... のパス（kind=file）"},
							"repo":     map[string]any{"type": "string", "description": "レポのバケツ。''=共通/未分類（任意）"},
							"category": map[string]any{"type": "string", "description": "サブプロジェクトのラベル（任意）"},
						},
						"required": []string{"kind"},
					},
				}
			},
			operatorWrite: true,
			session: func() map[string]any {
				return map[string]any{
					"name": "add_memo",
					"description": "Agent Fleet: add one note to your user's memo queue. It is a message to the HUMAN, " +
						"read in the Console later - not a message to another session (that is send_to_peer_session) and " +
						"not a way to give yourself a to-do. " +
						"When your user asks you to note something down - \"add this to my memo\", \"memo it\" - THIS " +
						"queue is what they mean, not your own agent memory or notes file: write there only when they " +
						"say memory, or name the file. " +
						"Use it for something worth acting on that is outside what you were asked to do: a bug you had to " +
						"step around, a stale document you noticed, follow-up work your change implies. " +
						"Say what you found and where (repo, file:line); the queue is read away from this conversation, " +
						"so nothing here is context the reader already has. " +
						"Do not log progress, completion or thanks - your report and the Console already carry those. " +
						"kind=text needs body; kind=file needs refPath (a ~/repos/... path) and takes body as a comment. " +
						"In a category that is a queue (queue=true) the note is work for a session instead, and waits " +
						"until your user releases it; you cannot release, send or delete it.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"kind":     map[string]any{"type": "string", "enum": []string{"text", "file"}, "description": "text | file"},
							"body":     map[string]any{"type": "string", "description": "The note (kind=text), or a comment on the file (kind=file)"},
							"refPath":  map[string]any{"type": "string", "description": "A ~/repos/... path (kind=file)"},
							"repo":     map[string]any{"type": "string", "description": "Which repo bucket it belongs to (optional; '' = unfiled)"},
							"category": map[string]any{"type": "string", "description": "Free-form sub-project label (optional; reuse one from list_memos)"},
						},
						"required": []string{"kind"},
					},
				}
			},
			ownArgs: true,
			refuse: func() string {
				if !memoWriteAllowed() {
					return "このアシスタントはメモの追加を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *noArgs) []byte {
				// The caller is this server's own session, never an argument: the Agent stamps
				// producer and producerOrigin from it, and nothing the model passes reaches either.
				// The operator has no session and sends "".
				from := ""
				if selfReportOnly() {
					self, err := mcpOwningSession()
					if err != nil {
						return mcpToolErr(req.ID, err.Error())
					}
					from = self
				}
				body, err := memoCreateBody(from, p.Args)
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				out, err := AgentPOST("/memos", body)
				if err != nil {
					return mcpToolErr(req.ID, memoRelayErr(err))
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "update_memo",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "update_memo",
					"description": "既存メモ（id 指定）を編集する。渡したフィールドだけ変わり、省略した項目はそのまま。文言の整形・カテゴリ変更・並び替え(position)に使う。id は list_memos で得る。解放済みのキューのメッセージを編集すると解放待ちに戻る。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":       map[string]any{"type": "string", "description": "メモ id（list_memos で取得）"},
							"body":     map[string]any{"type": "string", "description": "新しい本文（任意）"},
							"repo":     map[string]any{"type": "string", "description": "新しいレポバケツ（任意）"},
							"category": map[string]any{"type": "string", "description": "新しいカテゴリ（任意）"},
							"refPath":  map[string]any{"type": "string", "description": "新しい参照パス（任意）"},
							"position": map[string]any{"type": "integer", "description": "グループ内の新しい並び順（任意）"},
						},
						"required": []string{"id"},
					},
				}
			},
			operatorWrite: true,
			session: func() map[string]any {
				return map[string]any{
					"name": "update_memo",
					"description": "Agent Fleet: edit one memo already in your user's queue, by id from list_memos. " +
						"Only the fields you pass change. " +
						"Call it when what you queued turns out to be wrong or incomplete - the cause was something " +
						"else, the file moved, the follow-up you noted is already done - rather than adding a second " +
						"memo that contradicts the first. " +
						"The queue is your user's, and it holds notes from them and from other sessions too: correct " +
						"your own, and leave someone else's alone unless you are fixing the thing it is about. " +
						"Editing a released queue message sends it back to wait for release. " +
						"You cannot delete, release or send a memo; that stays with the user in the Console.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"id":       map[string]any{"type": "string", "minLength": 1, "description": "Memo id from list_memos"},
							"body":     map[string]any{"type": "string", "description": "Replacement note text (optional)"},
							"refPath":  map[string]any{"type": "string", "description": "Replacement ~/repos/... path (optional)"},
							"repo":     map[string]any{"type": "string", "description": "Move it to another repo bucket (optional; '' = unfiled)"},
							"category": map[string]any{"type": "string", "description": "Change the sub-project label (optional)"},
						},
						"required": []string{"id"},
					},
				}
			},
			ownArgs: true,
			refuse: func() string {
				if !memoWriteAllowed() {
					return "このアシスタントはメモの編集を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *idArg) []byte {
				if a.ID == "" {
					return mcpToolErr(req.ID, "id（メモ id）が必要です")
				}
				out, err := agentDo(http.MethodPatch, "/memos/"+url.PathEscape(a.ID), memoArgsObject(p.Args))
				if err != nil {
					return mcpToolErr(req.ID, memoRelayErr(err))
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "delete_memo",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "delete_memo",
					"description": "メモを id で削除する。id は list_memos で取得する。",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"id": map[string]any{"type": "string", "description": "メモ id（list_memos で取得）"}},
						"required":   []string{"id"},
					},
				}
			},
			operatorWrite: true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはメモの削除を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *idArg) []byte {
				if a.ID == "" {
					return mcpToolErr(req.ID, "id（メモ id）が必要です")
				}
				out, err := cpMemoDo(http.MethodDelete, "/internal/memos/"+url.PathEscape(a.ID), nil)
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
		{
			name: "flush_memos",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "flush_memos",
					"description": "選択したメモを1メッセージに連結（カテゴリを見出しに）してセッションに1回だけ送信し、送信済み(sent_at)にする。sessionName（list_my_sessions の name）と ids（list_memos の id 配列）を渡す。レポ全体/カテゴリ単位/個別は ids の作り方だけの違い。溜めたメモをまとめてセッションに渡す時に呼ぶ。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"sessionName": map[string]any{"type": "string", "description": "送信先セッション名（list_my_sessions の name）"},
							"ids":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "送るメモ id の配列（list_memos で取得）"},
						},
						"required": []string{"sessionName", "ids"},
					},
				}
			},
			operatorWrite: true,
			ownArgs:       true,
			refuse: func() string {
				if !writeEnabled() {
					return "このアシスタントはメモの一括送信を許可されていません"
				}
				return ""
			},
			handle: handler(func(req mcpReq, p toolCallParams, a *noArgs) []byte {
				out, err := cpMemoDo(http.MethodPost, "/internal/memos/flush", []byte(p.Args))
				if err != nil {
					return mcpToolErr(req.ID, err.Error())
				}
				return mcpTextResult(req.ID, out)
			}),
		},
	}
}

// memoArgsObject is a call's arguments as a JSON object body: an absent argument list is {}.
func memoArgsObject(args json.RawMessage) []byte {
	if len(args) == 0 || string(args) == "null" {
		return []byte("{}")
	}
	return []byte(args)
}

// memoCreateBody is add_memo's request to the Agent: the caller and the model's arguments.
func memoCreateBody(from string, args json.RawMessage) ([]byte, error) {
	var in struct {
		From string          `json:"from"`
		Memo json.RawMessage `json:"memo"`
	}
	in.From, in.Memo = from, memoArgsObject(args)
	return json.Marshal(in)
}

// memoRelayErr is the tool error for a failed relay call. The relay's own "no bridge here"
// answer reads as it did when the tools called the CP themselves.
func memoRelayErr(err error) string {
	var he *agentHTTPError
	if !errors.As(err, &he) {
		return err.Error()
	}
	if he.hasCode("memo_unavailable") {
		return memorelay.UnavailableMessage
	}
	return fmt.Sprintf("CP メモAPI エラー (%d): %s", he.StatusCode, he.Body)
}
