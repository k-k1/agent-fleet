package mcpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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
				out, err := agentDoTimeout(http.MethodGet, "/memos", nil, memoAgentTimeout)
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
					"description": "メモキューに1件追加する。kind=text は body（メモ本文）、kind=file は refPath（~/repos/... パス）が必須で body は任意コメント。repo（''=共通/未分類）と category（サブプロジェクトの自由ラベル）で仕分ける。チャット中に出た TODO・依頼・後で渡したい対象を溜めておく時に呼ぶ。キュー（queue=true）のカテゴリに入れたメモはセッションへの仕事になり、利用者が解放するまで待つ。作業項目（GitHub / Jira / Bitbucket）を指すだけなら reference を渡す（その場合 body は空でもよい。項目の中身は写されず、受け取るセッションが自分で読む）。自動実行カテゴリに同じ項目の待機中メモが既にあれば、追加せずそのメモを返す（existing）。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"kind":       map[string]any{"type": "string", "description": "text | file"},
							"body":       map[string]any{"type": "string", "description": "メモ本文（kind=text）またはコメント（kind=file）"},
							"refPath":    map[string]any{"type": "string", "description": "~/repos/... のパス（kind=file）"},
							"repo":       map[string]any{"type": "string", "description": "レポのバケツ。''=共通/未分類（任意）。worktree のフォルダ（repo@slug）を渡しても元のレポ（repo）に入る。スペースは spaces/<フォルダ>"},
							"category":   map[string]any{"type": "string", "description": "サブプロジェクトのラベル（任意）"},
							"reference":  memoRefSchemaJA(false),
							"after":      memoAfterSchemaJA(false),
							"startAfter": memoStartAfterSchemaJA(false),
							"agentKind":  memoRecipeSchemaJA("agentKind", false),
							"model":      memoRecipeSchemaJA("model", false),
							"effort":     memoRecipeSchemaJA("effort", false),
							"route":      memoRecipeSchemaJA("route", false),
							"runNext":    memoRunNextSchemaJA,
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
						"kind=text needs body or reference; kind=file needs refPath (a ~/repos/... path) and takes body as a comment. " +
						"To point at a work item (GitHub, Jira, Bitbucket) pass reference: the item itself is not copied " +
						"(the session that gets the note reads it with gh, jira or bkt), and the note may be only that " +
						"reference. If the category is an auto-run queue that already holds a waiting note for the same " +
						"item, nothing is added and the answer is the note already there (existing: true). " +
						"In a category that is a queue (queue=true) the note is work for a session instead, and waits " +
						"until your user releases it; you cannot release, send or delete it.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"kind":       map[string]any{"type": "string", "enum": []string{"text", "file"}, "description": "text | file"},
							"body":       map[string]any{"type": "string", "description": "The note (kind=text), or a comment on the file (kind=file)"},
							"refPath":    map[string]any{"type": "string", "description": "A ~/repos/... path (kind=file)"},
							"repo":       map[string]any{"type": "string", "description": "Which repo bucket it belongs to (optional; '' = unfiled). A worktree folder (repo@slug) is filed under its base repo. A space is spaces/<folder>; in a space, its own folder name files it under that space"},
							"category":   map[string]any{"type": "string", "description": "Free-form sub-project label (optional; reuse one from list_memos)"},
							"reference":  memoRefSchema(false),
							"after":      memoAfterSchema(false),
							"startAfter": memoStartAfterSchema(false),
							"agentKind":  memoRecipeSchema("agentKind", false),
							"model":      memoRecipeSchema("model", false),
							"effort":     memoRecipeSchema("effort", false),
							"route":      memoRecipeSchema("route", false),
							"runNext":    memoRunNextSchema,
							"continuation": map[string]any{"type": "boolean", "description": "Only for a session a queue started: true when this note is the next part of the queued message you were given, for the same work item (give the same reference, repo and category). " +
								"If your user's release of that message allows continuations, it runs without another release, up to the queue's limit. Otherwise it waits for a release like any note"},
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
				out, err := agentDoTimeout(http.MethodPost, "/memos", body, memoAgentTimeout)
				if err != nil {
					return mcpToolErr(req.ID, memoRelayErr(err))
				}
				return memoCreateResult(req.ID, p.Args, out)
			}),
		},
		{
			name: "update_memo",
			operator: func() map[string]any {
				return map[string]any{
					"name":        "update_memo",
					"description": "既存メモ（id 指定）を編集する。渡したフィールドだけ変わり、省略した項目はそのまま。文言の整形・カテゴリ変更・並び替え(position)に使う。id は list_memos で得る。解放済みのキューのメッセージを編集すると解放待ちに戻る。reference は省略で据え置き、{} で外し、3 項目そろえた object で付け替える。",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":         map[string]any{"type": "string", "description": "メモ id（list_memos で取得）"},
							"body":       map[string]any{"type": "string", "description": "新しい本文（任意）"},
							"repo":       map[string]any{"type": "string", "description": "新しいレポバケツ（任意）。worktree のフォルダ（repo@slug）は元のレポに入る。スペースは spaces/<フォルダ>"},
							"category":   map[string]any{"type": "string", "description": "新しいカテゴリ（任意）"},
							"refPath":    map[string]any{"type": "string", "description": "新しい参照パス（任意）"},
							"position":   map[string]any{"type": "integer", "description": "グループ内の新しい並び順（任意）"},
							"reference":  memoRefSchemaJA(true),
							"after":      memoAfterSchemaJA(true),
							"startAfter": memoStartAfterSchemaJA(true),
							"agentKind":  memoRecipeSchemaJA("agentKind", true),
							"model":      memoRecipeSchemaJA("model", true),
							"effort":     memoRecipeSchemaJA("effort", true),
							"route":      memoRecipeSchemaJA("route", true),
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
						"reference: leave it out to keep the note's reference, pass {} to remove it, or pass all three " +
						"fields to replace it; changing it onto an item another waiting note already holds is refused. " +
						"You cannot delete, release or send a memo; that stays with the user in the Console.",
					"inputSchema": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"id":         map[string]any{"type": "string", "minLength": 1, "description": "Memo id from list_memos"},
							"body":       map[string]any{"type": "string", "description": "Replacement note text (optional)"},
							"refPath":    map[string]any{"type": "string", "description": "Replacement ~/repos/... path (optional)"},
							"repo":       map[string]any{"type": "string", "description": "Move it to another repo bucket (optional; '' = unfiled). A worktree folder (repo@slug) is filed under its base repo. A space is spaces/<folder>; in a space, its own folder name files it under that space"},
							"category":   map[string]any{"type": "string", "description": "Change the sub-project label (optional)"},
							"reference":  memoRefSchema(true),
							"after":      memoAfterSchema(true),
							"startAfter": memoStartAfterSchema(true),
							"agentKind":  memoRecipeSchema("agentKind", true),
							"model":      memoRecipeSchema("model", true),
							"effort":     memoRecipeSchema("effort", true),
							"route":      memoRecipeSchema("route", true),
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
				// As on a create, the caller is this server's own session, never an argument. It
				// only picks the bucket a named repo maps to, so an edit still goes through without it.
				path := "/memos/" + url.PathEscape(a.ID)
				if selfReportOnly() {
					if self, err := mcpOwningSession(); err == nil {
						path += "?from=" + url.QueryEscape(self)
					}
				}
				out, err := agentDoTimeout(http.MethodPatch, path, memoArgsObject(p.Args), memoAgentTimeout)
				if err != nil {
					return mcpToolErr(req.ID, memoRelayErr(err))
				}
				return memoWriteResult(req.ID, p.Args, out)
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

// memoAgentTimeout is how long a memo tool waits for the Agent's relay: longer than the relay's
// two Control Plane calls of GET /memos together, so its partial answer (memos without
// categories) arrives instead of a timeout here.
const memoAgentTimeout = 2*memorelay.CPTimeoutDefault + 5*time.Second

// memoRefSchema is the reference argument of the session's add_memo and update_memo. The shape
// and the three-way meaning on an update are the Control Plane's (docs/build/03-control-plane.md
// 3.6); the relay forwards them as they are.
func memoRefSchema(update bool) map[string]any {
	desc := "A pointer to the work item this note is about; the item is not copied (the session that gets the note reads it itself). All three fields are needed."
	if update {
		desc += " Omit to keep the current reference; {} removes it."
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "description": desc,
		"properties": map[string]any{
			"provider": map[string]any{"type": "string", "description": "github | jira | bitbucket"},
			"key":      map[string]any{"type": "string", "description": "owner/repo#12 for GitHub and Bitbucket, PROJ-12 for Jira"},
			"url":      map[string]any{"type": "string", "description": "The item's http(s) address"},
		},
	}
}

// memoRefSchemaJA is memoRefSchema for the operator assistant's Japanese tool texts.
func memoRefSchemaJA(update bool) map[string]any {
	desc := "メモが指す作業項目（写さずに指すだけ。受け取るセッションが自分で読む）。3 項目すべて必要。"
	if update {
		desc += "省略で据え置き、{} で外す。"
	}
	return map[string]any{
		"type": "object", "description": desc,
		"properties": map[string]any{
			"provider": map[string]any{"type": "string", "description": "github | jira | bitbucket"},
			"key":      map[string]any{"type": "string", "description": "GitHub / Bitbucket は owner/repo#12、Jira は PROJ-12"},
			"url":      map[string]any{"type": "string", "description": "項目の http(s) アドレス"},
		},
	}
}

// memoAfterSchema is the after argument of the session's add_memo and update_memo: the notes of
// the same queue this one waits for. The Control Plane resolves and checks it (ADR 0137, addendum
// 2026-11-19); the relay forwards it as it is.
func memoAfterSchema(update bool) map[string]any {
	desc := "The notes of the same auto-run category (queue=true) this note waits for: each a memo id from list_memos, " +
		"or a work-item key such as owner/repo#12, which names the newest note of the category holding that item. " +
		"Any note of the category may be one, earlier or later, but a note cannot wait for itself, directly or through " +
		"the notes it waits for. The note is started only after every one of them was run and its run ended cleanly " +
		"with every pull request merged, or, for a run that opened none, with a last answer that starts with Done:; " +
		"if one went wrong, the note goes back to wait for your user."
	if update {
		desc += " Omit to keep them; [] removes them."
	}
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// memoAfterSchemaJA is memoAfterSchema for the operator assistant's Japanese tool texts.
func memoAfterSchemaJA(update bool) map[string]any {
	desc := "このメモが待つ、同じ自動実行カテゴリのメモ。各要素はメモ id（list_memos）か、作業項目のキー（owner/repo#12。その項目を持つカテゴリ内の最新のメモを指す）。" +
		"どれもこのメモより前にあるものに限る。すべてが実行されてきれいに終わってから、このメモが始まる。どれかがうまくいかなければ、このメモは利用者の確認待ちに戻る。"
	if update {
		desc += "省略で据え置き、[] で外す。"
	}
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// memoRunNextSchema is the runNext argument of the session's add_memo. update_memo has none: the
// mark on a message already waiting is the member's, set and cleared in the Console (#5021).
var memoRunNextSchema = map[string]any{"type": "boolean", "description": "Optional, for a note of an auto-run category only: true puts this note ahead of the waiting notes that are not marked: when a slot frees, the marked notes that can run are taken first, oldest of them first (its prerequisites, its start time and your user's approval, where one is needed, still apply). " +
	"Use it for a fix that unblocks other queued work, not to jump the queue with routine work. Only the note this call adds is marked; your user changes the mark in the Console."}

// memoRunNextSchemaJA is memoRunNextSchema for the operator assistant's Japanese tool texts.
var memoRunNextSchemaJA = map[string]any{"type": "boolean", "description": "任意。自動実行カテゴリのメモだけ: true にすると、このメモを印の無い待機中のメモより先にする。枠が空くと、実行できる印付きのメモを先に、その中では古い順に取る（前提のメモ・開始時刻・必要な場合の利用者の解放はそのまま）。ほかのキューの仕事を止めている修正に使う。印が付くのはこの呼び出しで追加するメモだけで、印の付け外しは利用者が Console で行う。"}

// memoStartAfterSchema is the startAfter argument of the session's add_memo and update_memo.
func memoStartAfterSchema(update bool) map[string]any {
	desc := "The earliest time this note of an auto-run category (queue=true) may be started, as an RFC 3339 time " +
		"WITH its offset, e.g. 2026-12-04T09:00:00+09:00 for 9:00 in Japan. A time without an offset is refused; " +
		"one already past means no constraint; at most 60 days ahead. Before that time the note is not started, " +
		"even when your user presses Run now, and releasing the note releases its start time too. " +
		"Use it for work that must not begin before some event, instead of writing \"release after <date>\" in the body."
	if update {
		desc += " Omit to keep it; \"\" removes it."
	}
	return map[string]any{"type": "string", "description": desc}
}

// memoStartAfterSchemaJA is memoStartAfterSchema for the operator assistant's Japanese tool texts.
func memoStartAfterSchemaJA(update bool) map[string]any {
	desc := "自動実行カテゴリのこのメモを始めてよい最も早い時刻。オフセット付きの RFC 3339（例: 日本時間 9:00 なら 2026-12-04T09:00:00+09:00）。" +
		"オフセットの無い時刻は拒否され、過ぎた時刻は制約なしになる。60 日先まで。その時刻までは「今すぐ実行」でも始まらず、解放するとこの時刻ごと解放したことになる。"
	if update {
		desc += "省略で据え置き、\"\" で外す。"
	}
	return map[string]any{"type": "string", "description": desc}
}

// memoRecipeSchema is one run-recipe argument (agentKind, model, effort, route) of the session's
// add_memo and update_memo.
func memoRecipeSchema(field string, update bool) map[string]any {
	what := map[string]string{
		"agentKind": "the agent kind that runs it",
		"model":     "the model it runs on (an id that suits the agent)",
		"effort":    "the effort level it runs at",
		"route":     "the route the run uses (leave out unless the model needs one)",
	}[field]
	desc := "Only for a note of an auto-run category (queue=true): " + what + ". Left out, the category's own setting " +
		"applies, field by field. Pick a heavier recipe only for work that needs it. A value the Control Plane refuses is an " +
		"error, and releasing the note releases its recipe too."
	if update {
		desc += " Omit to keep it; \"\" removes it, and changing it on a released note sends it back to wait for release."
	}
	return map[string]any{"type": "string", "description": desc}
}

// memoRecipeSchemaJA is memoRecipeSchema for the operator assistant's Japanese tool texts.
func memoRecipeSchemaJA(field string, update bool) map[string]any {
	what := map[string]string{
		"agentKind": "実行するエージェントの種類",
		"model":     "実行するモデル（エージェントに合う id）",
		"effort":    "実行する effort レベル",
		"route":     "実行に使うルート（モデルが要るときだけ）",
	}[field]
	desc := "自動実行カテゴリ（queue=true）のメモだけ: " + what + "。省略するとカテゴリの設定を項目ごとに使う。拒否された値はエラーになり、解放するとこの指定ごと解放したことになる。"
	if update {
		desc += "省略で据え置き、\"\" で外す。解放済みのメッセージで変えると解放待ちに戻る。"
	}
	return map[string]any{"type": "string", "description": desc}
}

// memoRecipeProblem says why a recipe field the call asked for is not on the note the answer
// returns, "" when every one is or when none was asked (a value of "" is a clear and asks for
// nothing). An older Control Plane drops the fields, and success would then be a false report.
func memoRecipeProblem(args json.RawMessage, answer string) string {
	var want map[string]any
	if json.Unmarshal(args, &want) != nil {
		return ""
	}
	got := map[string]any{}
	if json.Unmarshal([]byte(answer), &got) != nil {
		return ""
	}
	for _, k := range []string{"agentKind", "model", "effort", "route"} {
		// A non-string value is not this check's to read: the Control Plane refuses it.
		if w, _ := want[k].(string); strings.TrimSpace(w) != "" && got[k] != strings.TrimSpace(w) {
			return "The note is there, but its " + k + " was NOT kept as asked: this Control Plane does not support a run " +
				"recipe yet, or returned an existing note unchanged. Do not report it as set; tell your user."
		}
	}
	return ""
}

// memoWriteResult is the tool result of an add_memo or update_memo that the Control Plane
// accepted. The answer goes back as it came, so the session sees `existing` and the state. A
// reference that was sent but is missing from the answer means the Control Plane ignored it (an
// older one does): the note is there without it, and success would be a false report.
func memoWriteResult(id json.RawMessage, args json.RawMessage, answer string) []byte {
	if msg := memoWriteProblem(args, answer); msg != "" {
		return mcpToolErr(id, msg+" The Control Plane answered: "+answer)
	}
	return mcpTextResult(id, answer)
}

// memoCreateResult is memoWriteResult for add_memo, which may also ask for the run-next mark. A
// missing mark is reported beside the other problems, never instead of them: an older Control
// Plane drops the mark and the prerequisites alike, and the prerequisites say when the note may run.
func memoCreateResult(id json.RawMessage, args json.RawMessage, answer string) []byte {
	var msgs []string
	for _, m := range []string{memoWriteProblem(args, answer), memoRunNextProblem(args, answer)} {
		if m != "" {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) == 0 {
		return mcpTextResult(id, answer)
	}
	return mcpToolErr(id, strings.Join(msgs, " ")+" The Control Plane answered: "+answer)
}

// memoWriteProblem is the first field of a create or an update the answer shows was not kept, ""
// when there is none.
func memoWriteProblem(args json.RawMessage, answer string) string {
	if memoRefSent(args) && !memoRefAnswered(answer) {
		return "The note was saved, but its reference was NOT kept: this Control Plane does not support " +
			"references yet. Do not report the reference as stored; tell your user, or put the item's key and URL " +
			"in the note's body instead."
	}
	// An answer with existing: true is a note already holding the same item: nothing was written,
	// so a missing list there says nothing about the Control Plane, and the answer goes back as it came.
	if memoAfterSent(args) && !memoAfterAnswered(answer) && !memoExistingAnswer(answer) {
		return "The note was saved, but its prerequisites (after) were NOT kept: this Control Plane does not " +
			"support them yet, so nothing makes the note wait. Do not report them as stored; tell your user."
	}
	if msg := memoStartAfterProblem(args, answer, time.Now()); msg != "" {
		return msg
	}
	return memoRecipeProblem(args, answer)
}

// memoStartAfterSlack is how far ahead of the check a requested time must be for its absence from
// the answer to mean anything: one that passed while the call was in flight is cleared by a
// Control Plane that supports the field, which must not read as one that does not.
const memoStartAfterSlack = time.Minute

// memoStartAfterProblem says why the start time the call asked for is not on the note the answer
// returns, "" when it is or when nothing was asked. Times are compared as instants, so the
// Control Plane's fixed-width UTC matches the offset the model wrote.
func memoStartAfterProblem(args json.RawMessage, answer string, now time.Time) string {
	var in struct {
		StartAfter string `json:"startAfter"`
	}
	if json.Unmarshal(args, &in) != nil {
		return ""
	}
	want, err := time.Parse(time.RFC3339, strings.TrimSpace(in.StartAfter))
	if err != nil || !want.After(now) {
		return ""
	}
	var out struct {
		StartAfter string `json:"startAfter"`
		Existing   bool   `json:"existing"`
	}
	if json.Unmarshal([]byte(answer), &out) != nil {
		return ""
	}
	// The Control Plane keeps microseconds and drops the rest, so the comparison does the same.
	if got, err := time.Parse(time.RFC3339Nano, out.StartAfter); err == nil && got.Equal(want.Truncate(time.Microsecond)) {
		return ""
	}
	if out.Existing {
		return "Nothing was added: the queue already holds a waiting note for the same item, and it was returned " +
			"unchanged, so it does NOT carry the start time you asked for. Do not report it as set; tell your user, " +
			"or set it on that note with update_memo."
	}
	if out.StartAfter == "" && !want.After(now.Add(memoStartAfterSlack)) {
		return ""
	}
	if out.StartAfter != "" {
		return "The note was saved with a different start time (startAfter) than the one you asked for. Do not report " +
			"yours as stored; tell your user."
	}
	return "The note was saved, but its start time (startAfter) was NOT kept: this Control Plane does not support " +
		"it yet, so nothing makes the note wait for that time. Do not report it as stored; tell your user."
}

// memoRunNextProblem says why a create that asked for the run-next mark answered a note without it,
// "" when it carries the mark or none was asked. A create's answer is a waiting note, which always
// shows its mark, so a missing one is a Control Plane that dropped the field or a note already there.
func memoRunNextProblem(args json.RawMessage, answer string) string {
	var in struct {
		RunNext bool `json:"runNext"`
	}
	if json.Unmarshal(args, &in) != nil || !in.RunNext {
		return ""
	}
	var out struct {
		RunNext    bool   `json:"runNext"`
		Existing   bool   `json:"existing"`
		State      string `json:"state"`
		DeliveryID string `json:"deliveryId"`
	}
	if json.Unmarshal([]byte(answer), &out) != nil || out.RunNext {
		return ""
	}
	if out.Existing {
		// The answer shows the mark only while the note waits; a note a run has taken has no place
		// left in the order, and nobody can mark it.
		if (out.State != "held" && out.State != "pending") || out.DeliveryID != "" {
			return "Nothing was added: a run has already taken the note this queue holds for the same item, so " +
				"marking it to run next means nothing now. Do not report a new note or a mark."
		}
		// The answer cannot tell an unmarked note from one a hand send is taking (the DTO hides the
		// mark then), so it says what did not happen and offers the Console only as a condition.
		return "Nothing was added, and no mark was set: the queue already holds a matching note, returned " +
			"as it is, and the answer shows no run-next mark on it. Do not report a new note or a mark; tell your " +
			"user, who can mark it in the Console while it still waits in an auto-run category."
	}
	return "The note was saved, but it was NOT marked to run next: this Control Plane does not support it yet. " +
		"Do not report it as marked; tell your user."
}

// memoRefSent reports whether the call's reference is one to be kept: any non-empty field counts,
// so a partial one (which the Control Plane refuses) is still "sent". {}, three empty strings and an
// absent one are what the relay forwards as clear or nothing, and ask for nothing to be kept.
func memoRefSent(args json.RawMessage) bool {
	var in struct {
		Reference *struct {
			Provider string `json:"provider"`
			Key      string `json:"key"`
			URL      string `json:"url"`
		} `json:"reference"`
	}
	if json.Unmarshal(args, &in) != nil || in.Reference == nil {
		return false
	}
	r := in.Reference
	return r.Provider != "" || r.Key != "" || r.URL != ""
}

// memoAfterSent reports whether the call asks for prerequisites to be kept: a non-empty after list.
// [] (clear) and an absent one ask for none.
func memoAfterSent(args json.RawMessage) bool {
	var in struct {
		After []string `json:"after"`
	}
	return json.Unmarshal(args, &in) == nil && len(in.After) > 0
}

// memoExistingAnswer reports whether the answer is a note already in the queue (existing: true).
func memoExistingAnswer(answer string) bool {
	var out struct {
		Existing bool `json:"existing"`
	}
	return json.Unmarshal([]byte(answer), &out) == nil && out.Existing
}

// memoAfterAnswered reports whether the Control Plane's answer carries prerequisites.
func memoAfterAnswered(answer string) bool {
	var out struct {
		After []string `json:"after"`
	}
	return json.Unmarshal([]byte(answer), &out) == nil && len(out.After) > 0
}

// memoRefAnswered reports whether the Control Plane's answer carries a reference.
func memoRefAnswered(answer string) bool {
	var out struct {
		Reference *struct {
			Key string `json:"key"`
		} `json:"reference"`
	}
	return json.Unmarshal([]byte(answer), &out) == nil && out.Reference != nil && out.Reference.Key != ""
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
