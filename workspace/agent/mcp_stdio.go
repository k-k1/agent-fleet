package main

// Local stdio MCP server (docs/19 Q1). Spawned by the assistant chat's
// `claude -p --mcp-config` as `workspace-agent mcp-stdio`, it exposes READ-ONLY
// "Agent Fleet" tools over newline-delimited JSON-RPC 2.0 on stdio. Each tool calls
// the local Agent's REST (127.0.0.1:<AGENT_ADDR>, AGENT_TOKEN) so the assistant can
// inspect the user's OWN workspace with no PAT and no network egress — unlike the CP
// /mcp server (PAT + public-URL hairpin), which stays for external/admin use.
//
// Write tools (send_to_session, …) are exposed ONLY when the server is started with
// --write, which chat.go passes exclusively for conversations whose assistant granted
// af_write (docs/19 Q2). An af_read conversation's server never advertises or accepts a
// write tool — the gate is the advertised tool set, not just a permission prompt (the
// chat runs claude with --dangerously-skip-permissions, so a prompt would not gate).

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const mcpStdioProtocol = "2025-06-18"

// mcpWriteEnabled gates the write tools. Set once from the `--write` arg before the
// stdio loop starts; a global is safe because each spawn is a fresh short-lived process
// serving exactly one chat conversation.
var mcpWriteEnabled bool

// mcpConvID is the owning conversation's id, passed as `--conv <id>` by chat.go's
// MCP config (docs/30). create_session / send_to_session forward it as report_to so
// the spawned/steered session reports back to THIS conversation automatically — the
// link is tool-side plumbing, never something the model has to remember.
var mcpConvID string

// runMCPStdio is the `workspace-agent mcp-stdio` subcommand: a blocking stdio loop.
// Pass --write to additionally expose the write tools (docs/19 Q2 af_write opt-in).
func runMCPStdio(args []string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--write":
			mcpWriteEnabled = true
		case "--conv":
			if i+1 < len(args) {
				i++
				mcpConvID = args[i]
			}
		}
	}
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	w := bufio.NewWriter(os.Stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if resp := dispatchMCPStdio(line); resp != nil {
				_, _ = w.Write(resp)
				_ = w.WriteByte('\n')
				_ = w.Flush()
			}
		}
		if err != nil {
			return // stdin closed (claude shut the server down)
		}
	}
}

type mcpReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // number|string for requests; absent/null for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func mcpResult(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return b
}

func mcpError(id json.RawMessage, code int, msg string) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
	return b
}

func dispatchMCPStdio(line []byte) []byte {
	var req mcpReq
	if err := json.Unmarshal(line, &req); err != nil {
		return nil
	}
	isNotif := len(bytes.TrimSpace(req.ID)) == 0 || string(bytes.TrimSpace(req.ID)) == "null"
	switch req.Method {
	case "initialize":
		return mcpResult(req.ID, map[string]any{
			"protocolVersion": mcpStdioProtocol,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agent-fleet-local", "version": "q1"},
		})
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "ping":
		if isNotif {
			return nil
		}
		return mcpResult(req.ID, map[string]any{})
	case "tools/list":
		return mcpResult(req.ID, map[string]any{"tools": mcpStdioToolList()})
	case "tools/call":
		return mcpStdioCall(req)
	default:
		if isNotif {
			return nil
		}
		return mcpError(req.ID, -32601, "method not found: "+req.Method)
	}
}

// mcpStdioToolList is the advertised tool set: the read-only tools always, plus the
// write tools when the server was started with --write (docs/19 Q2 af_write opt-in).
func mcpStdioToolList() []map[string]any {
	if mcpWriteEnabled {
		return append(append([]map[string]any{}, mcpStdioTools...), mcpStdioWriteTools...)
	}
	return mcpStdioTools
}

// mcpStdioTools — read-only Agent Fleet tools (names are prefixed mcp__af__<name> by
// claude). Descriptions are prescriptive about WHEN to call (better trigger rate).
var mcpStdioTools = []map[string]any{
	{
		"name":        "list_my_sessions",
		"description": "利用者自身のワークスペースで稼働中のセッション一覧（名前・種別・状態・作業ディレクトリ）を返す。「今どのセッションが動いている?」等に答える時に呼ぶ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "get_session_status",
		"description": "指定セッションのライブ状態（working/idle/question/plan 等）を返す。保留中の質問と選択肢は questions、承認待ちのプラン本文は plan として付く（claude）。特定セッションが動作中か聞かれた時や、質問への回答（answer_session_question）・プランへの応答（respond_session_plan）の前に呼ぶ。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "セッション名（例: s7）"},
			},
			"required": []string{"name"},
		},
	},
	{
		"name":        "get_session_output",
		"description": "指定セッションの端末出力（任意で since バイトオフセット以降のみ）を返す。あるセッションの最近の出力/結果を要約・確認する時に呼ぶ。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":  map[string]any{"type": "string", "description": "セッション名"},
				"since": map[string]any{"type": "integer", "description": "この出力オフセット以降のみ取得（任意）"},
			},
			"required": []string{"name"},
		},
	},
	{
		"name":        "list_cleanup_candidates",
		"description": "溜まったセッション・worktree の掃除候補を点検して返す。各候補は type（session|worktree）・action（archive_session|delete_worktree|空=手動のみ）・safety（safe=マージ済みクリーン等で安全／review=停止中セッションや未マージ worktree で要確認／keep=稼働中や未コミット・未pushで触らない）・reason を持つ。『リポジトリが散らかってきた・不要なものを片付けたい』時にまず呼び、safe/review の候補を利用者に提示してから archive_session / delete_worktree で実行する。keep は掃除せず、必要なら利用者が Console で対応する。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "list_cleanup_archives",
		"description": "掃除で退避したアーカイブ（削除したセッションやブランチの gz 安全網）の一覧を返す。delete_session / delete_branch は消す前に必ずここへ退避するので、消しすぎた時は restore_cleanup_archive で復元、不要になったら purge_cleanup_archive で完全削除して容量を回収できる。各アーカイブは id・日時・理由・含まれるセッション/ブランチを持つ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "get_session_usage",
		"description": "各セッションのコンテキスト使用量と累積消費トークンを返す。name 指定で1セッション、省略で transcript を持つ全セッション（shell / SSM は対象外）。context は現在のコンテキスト量（tokens と read/create/fresh の内訳、window に対する pct%。最初の応答が返るまでは無く、自動圧縮後は圧縮後の値）。cumulative は累積消費（論理ターン数 turns、inTok/outTok/cacheRead/cacheCreate、spend=inTok+cacheCreate+outTok の合計）。注意: agy / cursor は一覧に含まれるが transcript にトークン情報が無いため context は空・cumulative は全て 0 になる（消費ゼロの意味ではない。agy の残枠は get_agent_usage を見る）。kiro は転写にトークンが無いが、managed（ACP）セッションが稼働中は _kiro.dev/metadata のライブ値から context（pct＋実 window に対する概算 tokens）と cumulative.credits（消費クレジット）を返す（停止中や TUI 実行は context 空）。copilot は outTok のみ記録され inTok/cache は 0・context は無い。『どのセッションがコンテキスト逼迫か』『どれだけ消費したか』を聞かれた時や、引き継ぎ・圧縮・新セッション分割の判断材料に呼ぶ。サブスクリプション枠の残量は get_agent_usage（別ツール）。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "セッション名（任意。省略で全セッション）"},
			},
		},
	},
	{
		"name":        "get_agent_usage",
		"description": "各エージェント CLI のサブスクリプション使用量とレート制限を返す（claude / codex / agy。opencode / copilot / cursor / kiro は使用量ソースが無いため含まれない）。claude / codex は fiveHour（5時間枠）と sevenDay（週間枠）の pct が使用率（0–100）、resetsAt が解除日時（ISO 8601）で、codex は planType や resetCredits も付く。agy は形が異なり、account / plan と groups（クォータ枠ごとに label・remainingPct・resetsAt。実験枠 Starter 等）を返す。authed=false はその CLI に未ログイン、ageSec は計測の古さ（秒）。『あとどれくらい使える?』『制限はいつ解除?』と聞かれた時や、大きなタスクをセッションに振る前の判断材料に呼ぶ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "list_repos",
		"description": "利用者のワークスペースにある git 作業コピー（~/repos 配下）の一覧を返す。新規セッションをどのディレクトリ（リポジトリ）で起こすか決める時に、まだセッションが動いていないリポジトリも含めて選ぶために呼ぶ。返る各リポジトリの path を create_session の dir に渡す。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "list_memos",
		"description": "メモキュー（溜めて一括でセッションへ送るメモ）の一覧を返す。未送信＋保持期間内の送信済みを含む。各メモは id/repo/category/kind(file|text)/body/refPath を持つ。利用者に「今どんなメモが溜まっている?」と聞かれた時や、flush_memos / update_memo / delete_memo で対象の id を選ぶ前に呼ぶ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "list_schedules",
		"description": "定時実行スケジュールの一覧を返す（docs/38）。各スケジュールは id / spec_kind(cron|interval|once) / spec / spec_label(登録時の自然言語) / tz / enabled / next_run(次回発火 UTC) / next_run_local(tz でのわかりやすい表記) / last_run / last_status / prompt などを持つ。利用者に「今どんな定時タスクがある?」と聞かれた時や、update_schedule / delete_schedule / pause_schedule / run_schedule_now で対象 id を選ぶ前に呼ぶ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "get_schedule_runs",
		"description": "指定スケジュールの実行履歴（最新50件）を返す。各行は fired_at（発火時刻 UTC）と status（fired / skipped_* / error:...）。定時タスクがちゃんと動いているか／失敗していないかを確認する時に呼ぶ。id は list_schedules で取得する。",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id（list_schedules で取得）"}},
			"required":   []string{"id"},
		},
	},
}

// mcpStdioWriteTools — Agent Fleet write/orchestrate tools, advertised only under --write
// (docs/19 af_write opt-in): drive tmux sessions (send_to_session) AND consult other
// assistants (list_assistants / ask_assistant). Consults are advisory-only by construction
// (the sub-turn runs with no tools), so they can't loop or escalate.
var mcpStdioWriteTools = []map[string]any{
	{
		"name":        "list_models",
		"description": "指定エージェントで現在選べるモデル一覧を返す。model 指定で create_session する前には必ず呼び、返った id を使うこと。claude は固定のティア別名（fable/opus/sonnet/haiku）、codex／opencode／agy／copilot／cursor／kiro は接続状態を反映したライブカタログ（copilot はプラン反映 — Free は Auto のみで空になる。cursor は effort をモデル id に畳んだアカウント連動カタログ。kiro は Free でも named 指定可・既定は auto。未指定は auto ルーティング）。利用者が terra のような略称で指定した場合も、一覧から対応する完全な id（例: gpt-5.6-terra）を選ぶ。opencode は同じモデルが 2 つの課金経路で並ぶことがある（opencode-go/… = Go サブスクの範囲内、opencode/… = Zen の従量課金）。同名が両方にある場合は先に並んでいる opencode-go/… を選ぶこと（一覧の並びは利用者の設定で整形済み）。利用者が Zen を明示した場合だけ opencode/… を使う。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "description": "claude / codex / opencode / agy / copilot / cursor / kiro"},
			},
			"required": []string{"kind"},
		},
	},
	{
		"name": "create_session",
		"description": "新しいコーディングセッションを起こす。dir（作業ディレクトリ）で指定したリポジトリで claude 等を起動する。" +
			"worktree=true なら dir のリポジトリから新しい独立 worktree を作って起動する（branch は基点、省略時は現在の HEAD。new_branch は新規ブランチ名、省略時は仮ブランチを自動生成）。" +
			"initial_prompt を渡すと、起動後に最初のタスクとして自動で送信される（別コールの send_to_session は不要）。" +
			"用途例：あるセッションの内容を引き継いで別セッションで続ける（先に get_session_output で文脈を読み、要約を initial_prompt に入れる）／壁打ちで固めた作業を新規セッションで開始する。" +
			"dir は list_my_sessions の dir（走っているセッションと同じ場所）か list_repos の path から選ぶ。新規セッションはリソースを消費するので、起こす前に利用者へ一言確認すること。" +
			"Codex／OpenCode を指定する場合は managed で開始する（TUI は使わない）。model を指定する前には必ず list_models で利用可能な id を確認し、その id を渡す。作成したセッションが入力待ちになる／異常終了すると、この会話に自動で報告が届くのでポーリングは不要。報告が届いたら内容（必要なら get_session_output）を確認して次の行動を決める。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dir":            map[string]any{"type": "string", "description": "作業ディレクトリ（リポジトリの作業コピー等）。省略時はホーム。list_my_sessions の dir か list_repos の path を渡す。"},
				"title":          map[string]any{"type": "string", "description": "セッションの表示名（任意）。何のタスクかが分かる短い名前。"},
				"kind":           map[string]any{"type": "string", "description": "エージェント種別（任意）。claude（既定）| codex | opencode | agy | copilot | cursor | kiro | shell。agy は Antigravity CLI（接続済みのときのみ起動可）。copilot は GitHub Copilot CLI（GitHub 連携＋Copilot サブスクが前提）。cursor は Cursor CLI（接続済みのときのみ起動可）。kiro は Kiro CLI（接続済みのときのみ起動可・既定は managed ドライバ）。shell は生のシェルで initial_prompt/送信文字列がそのままコマンド実行される（エージェントのガードレール無し）ため、起動前に実行内容を利用者へ確認すること。"},
				"model":          map[string]any{"type": "string", "description": "モデル上書き（任意）。"},
				"initial_prompt": map[string]any{"type": "string", "description": "起動後に自動送信する最初のタスク/引き継ぎ文（任意）。"},
				"worktree":       map[string]any{"type": "boolean", "description": "dir から新しい独立 worktree を作成して起動する（任意、既定 false）。"},
				"branch":         map[string]any{"type": "string", "description": "worktree の基点ブランチ（任意、省略時は現在の HEAD）。"},
				"new_branch":     map[string]any{"type": "string", "description": "worktree に作る新規ブランチ名（任意、省略時は仮ブランチを自動生成）。"},
			},
		},
	},
	{
		"name":        "add_memo",
		"description": "メモキューに1件追加する。kind=text は body（メモ本文）、kind=file は refPath（~/repos/... パス）が必須で body は任意コメント。repo（''=共通/未分類）と category（サブプロジェクトの自由ラベル）で仕分ける。チャット中に出た TODO・依頼・後で渡したい対象を溜めておく時に呼ぶ。",
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
	},
	{
		"name":        "update_memo",
		"description": "既存メモ（id 指定）を編集する。渡したフィールドだけ変わり、省略した項目はそのまま。文言の整形・カテゴリ変更・並び替え(position)に使う。id は list_memos で得る。",
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
	},
	{
		"name":        "delete_memo",
		"description": "メモを id で削除する。id は list_memos で取得する。",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "メモ id（list_memos で取得）"}},
			"required":   []string{"id"},
		},
	},
	{
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
	},
	{
		"name": "create_schedule",
		"description": "定時実行スケジュールを登録する（docs/38）。指定時刻に、必要なら停止中のワークスペースを起こして新規セッションを起動し、prompt を最初のタスクとして投入する。完了報告はこの会話に自動で届く。" +
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
			},
			"required": []string{"spec_kind", "spec", "prompt"},
		},
	},
	{
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
			},
			"required": []string{"id"},
		},
	},
	{
		"name":        "delete_schedule",
		"description": "スケジュールを id で削除する。id は list_schedules で取得する。",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id（list_schedules で取得）"}},
			"required":   []string{"id"},
		},
	},
	{
		"name":        "pause_schedule",
		"description": "スケジュールを一時停止する（発火しなくなる。定義は残る）。id は list_schedules で取得。再開は resume_schedule。",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id"}},
			"required":   []string{"id"},
		},
	},
	{
		"name":        "resume_schedule",
		"description": "一時停止したスケジュールを再開する（次回発火を今から再計算）。id は list_schedules で取得。",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id"}},
			"required":   []string{"id"},
		},
	},
	{
		"name":        "run_schedule_now",
		"description": "スケジュールを今すぐ発火させる（動作確認用）。定時発火と同じ経路（wake ポリシー・冪等・keep-alive）を通る。次のスケジューラ tick（最大約1分後）で実行される。停止中（pause 済み）のスケジュールは先に resume すること。id は list_schedules で取得。",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "スケジュール id"}},
			"required":   []string{"id"},
		},
	},
	{
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
	},
	{
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
	},
	{
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
	},
	{
		// 停止は /halt（再開可能）への中継。破壊的な /stop（メタごと忘却）は意図して
		// 公開しない — 広告ツール集合がゲートなので、不可逆操作は Console に残す。
		"name":        "stop_session",
		"description": "指定セッションを停止する（停止中＝再開可能。会話履歴と作業ディレクトリは保持され、resume_session や Console から再開できる）。暴走している・不要になった・リソースを空けたいセッションを畳む時に呼ぶ。実行中の作業は中断され、そのセッションへの未達の自動報告は取り消される。実行前に『どのセッションを止めるか』を一言添えて利用者に確認すること。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "停止するセッション名（list_my_sessions の name）"},
			},
			"required": []string{"name"},
		},
	},
	{
		"name":        "resume_session",
		"description": "停止中のセッションを再開する（会話履歴を引き継いで再起動。稼働中なら何もしない）。stop_session で止めたセッションや停止中のセッションを再び動かす時に呼ぶ。再開後に作業を指示するには send_to_session を使う。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "再開するセッション名（list_my_sessions の name）"},
			},
			"required": []string{"name"},
		},
	},
	{
		"name":        "archive_session",
		"description": "終わったセッションをアーカイブして普段の一覧から隠す（会話履歴は残り、Console から復元できる＝可逆）。list_cleanup_candidates で action=archive_session の候補を片付ける時に使う。稼働中の作業は中断されるので、完了済みかどうかを含め実行前に利用者へ確認すること。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "アーカイブするセッション名（list_my_sessions / list_cleanup_candidates の name）"},
			},
			"required": []string{"name"},
		},
	},
	{
		"name": "delete_worktree",
		"description": "不要になった worktree（作業コピー）を削除する。list_cleanup_candidates で action=delete_worktree の候補（マージ済みクリーン＝safe、未マージだがクリーン＝review）を片付ける時に使う。" +
			"未コミット/未pushの変更がある worktree は保護のため削除できない（keep 候補。Console で強制削除するよう案内する）。削除でその worktree に紐づく停止中セッションも一覧から整理される。ローカルの作業コピーだけが消え、履歴・リモート・ブランチは残る。破壊的操作なので、どの worktree を消すかを一言添えて実行前に必ず利用者へ確認すること。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "削除する worktree の名前（list_cleanup_candidates の worktree 候補の id）"},
			},
			"required": []string{"name"},
		},
	},
	{
		"name":        "delete_session",
		"description": "アーカイブ済み／不要なセッションを完全に削除して容量を回収する（会話ログ jsonl も消す）。archive_session が一覧から隠すだけなのに対し、これは実体を消す。消す前に jsonl とメタを gz アーカイブ（安全網）へ退避するので復元可能（list_cleanup_archives → restore_cleanup_archive）。稼働中のセッションは削除できない（先に停止）。list_cleanup_candidates で action=delete_session の候補を片付ける時に使う。不可逆に近い操作なので、どのセッションを消すかを明示して実行前に必ず利用者へ確認すること。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "削除するセッション名（list_cleanup_candidates / list_my_sessions の name）"},
			},
			"required": []string{"name"},
		},
	},
	{
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
	},
	{
		"name":        "restore_cleanup_archive",
		"description": "掃除で退避したアーカイブ（gz 安全網）を復元する。delete_session なら会話ログ jsonl とセッション一覧の行が戻り、delete_branch ならブランチが再作成される。消しすぎた・やっぱり要る時に使う。id は list_cleanup_archives で取得する。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "復元するアーカイブ id（list_cleanup_archives で取得）"},
			},
			"required": []string{"id"},
		},
	},
	{
		"name":        "purge_cleanup_archive",
		"description": "掃除で退避したアーカイブを完全に削除して容量を回収する（復元不可になる）。もう戻す必要がないと確認できたアーカイブにのみ使う。id は list_cleanup_archives で取得する。完全削除なので実行前に利用者へ確認すること。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "完全削除するアーカイブ id（list_cleanup_archives で取得）"},
			},
			"required": []string{"id"},
		},
	},
	{
		"name":        "list_assistants",
		"description": "利用可能なアシスタント（常設ビルトイン＋ユーザー定義）の一覧を返す。ask_assistant で誰に相談するか選ぶ前に呼ぶ。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "ask_assistant",
		"description": "別の専門アシスタントに助言を求める。相手は読み取り専用で1ターンだけ走り、助言テキストのみ返す（副作用なし・こちらの作業は代行しない）。例：SRE アシスタントにインシデント状況を見てもらう、ユーザー定義の専門アシスタントに設計を確認する。まず list_assistants で相手を選ぶこと。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"assistant": map[string]any{"type": "string", "description": "相談相手のアシスタント名または id"},
				"prompt":    map[string]any{"type": "string", "description": "相手に尋ねる内容（必要な文脈も含める）"},
			},
			"required": []string{"assistant", "prompt"},
		},
	},
}

func mcpStdioCall(req mcpReq) []byte {
	var p struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)
	var a struct {
		Name      string `json:"name"`
		Since     int64  `json:"since"`
		Prompt    string `json:"prompt"`
		Assistant string `json:"assistant"`
		// create_session args
		Dir           string `json:"dir"`
		Title         string `json:"title"`
		Kind          string `json:"kind"`
		Model         string `json:"model"`
		InitialPrompt string `json:"initial_prompt"`
		Worktree      bool   `json:"worktree"`
		Branch        string `json:"branch"`
		NewBranch     string `json:"new_branch"`
		// answer_session_question args: 質問順の 1-based 選択肢番号。
		Choices []int `json:"choices"`
		// respond_session_plan args
		Decision string `json:"decision"`
		Feedback string `json:"feedback"`
		// memo args (id in the path; the rest are forwarded verbatim via p.Args).
		// ID doubles as the cleanup-archive id (restore/purge). Repo names the branch's repo.
		ID   string `json:"id"`
		Repo string `json:"repo"`
	}
	_ = json.Unmarshal(p.Args, &a)

	// Memo-queue tools relay to the CP's /internal/memos bridge (the queue lives in the
	// CP store, not the Agent), authenticated by AF_MEMO_TOKEN. list_memos is read-only
	// (available to af_read too); the mutating ones require --write. The tool args match
	// the CP wire shape, so p.Args is forwarded as the request body verbatim.
	switch p.Name {
	case "get_agent_usage":
		// Read-only merge of the two WsBar usage endpoints (5h/weekly windows captured
		// locally from statusline / rollout — no network call). opencode has no usage
		// source, so it is intentionally absent from the result (said in the tool desc).
		cl, err := agentGET("/claude/usage")
		if err != nil {
			return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
		}
		cx, err := agentGET("/codex/usage")
		if err != nil {
			return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
		}
		// agy usage lives under the connections path and has a different shape
		// ({account, plan, groups}); it self-reports authed=false when signed out
		// and never 500s, so a merge is safe. Absent when unsupported on this host.
		ag, err := agentGET("/connections/agy/usage")
		if err != nil {
			return mcpToolErr(req.ID, "使用量の取得に失敗しました: "+err.Error())
		}
		b, _ := json.Marshal(map[string]any{
			"claude": json.RawMessage(cl),
			"codex":  json.RawMessage(cx),
			"agy":    json.RawMessage(ag),
		})
		return mcpTextResult(req.ID, string(b))
	case "list_models":
		if a.Kind != "claude" && a.Kind != "codex" && a.Kind != "opencode" && a.Kind != "agy" && a.Kind != "copilot" && a.Kind != "cursor" && a.Kind != "kiro" {
			return mcpToolErr(req.ID, "kind には claude / codex / opencode / agy / copilot / cursor / kiro のいずれかを指定してください")
		}
		out, err := agentGET("/agents/" + url.PathEscape(a.Kind) + "/models")
		if err != nil {
			return mcpToolErr(req.ID, "モデル一覧の取得に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "list_memos":
		out, err := cpMemoDo(http.MethodGet, "/internal/memos", nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "add_memo":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはメモの追加を許可されていません")
		}
		out, err := cpMemoDo(http.MethodPost, "/internal/memos", []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "update_memo":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはメモの編集を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（メモ id）が必要です")
		}
		out, err := cpMemoDo(http.MethodPatch, "/internal/memos/"+url.PathEscape(a.ID), []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_memo":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはメモの削除を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（メモ id）が必要です")
		}
		out, err := cpMemoDo(http.MethodDelete, "/internal/memos/"+url.PathEscape(a.ID), nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "flush_memos":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはメモの一括送信を許可されていません")
		}
		out, err := cpMemoDo(http.MethodPost, "/internal/memos/flush", []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "list_schedules":
		out, err := cpScheduleDo(http.MethodGet, "/internal/schedules", nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "get_schedule_runs":
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		out, err := cpScheduleDo(http.MethodGet, "/internal/schedules/"+url.PathEscape(a.ID)+"/runs", nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "create_schedule":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの登録を許可されていません")
		}
		// Route completion reports back to THIS operator conversation (docs/30): stamp
		// owner_conv = the operator's own conv id, overriding any client-supplied value.
		out, err := cpScheduleDo(http.MethodPost, "/internal/schedules", withOwnerConv(p.Args, mcpConvID))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "update_schedule":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの編集を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		out, err := cpScheduleDo(http.MethodPatch, "/internal/schedules/"+url.PathEscape(a.ID), []byte(p.Args))
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_schedule":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの削除を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		out, err := cpScheduleDo(http.MethodDelete, "/internal/schedules/"+url.PathEscape(a.ID), nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "pause_schedule", "resume_schedule", "run_schedule_now":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはスケジュールの操作を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（スケジュール id）が必要です")
		}
		action := map[string]string{"pause_schedule": "pause", "resume_schedule": "resume", "run_schedule_now": "run-now"}[p.Name]
		out, err := cpScheduleDo(http.MethodPost, "/internal/schedules/"+url.PathEscape(a.ID)+"/"+action, nil)
		if err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		return mcpTextResult(req.ID, out)
	}

	// Write/orchestrate tools — only when this server was started with --write.
	switch p.Name {
	case "create_session":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの作成を許可されていません")
		}
		// P3: a raw shell session executes arbitrary commands (no agent guardrails) — gate
		// its creation on a Discord approval when this is an unattended operator turn.
		if a.Kind == "shell" {
			if err := bridgeApprovalGate(approvalLabel("create_session_shell"), shellCreateTarget(a.Dir, a.InitialPrompt)); err != nil {
				return mcpToolErr(req.ID, err.Error())
			}
		}
		driver := ""
		if a.Kind == "codex" || a.Kind == "opencode" || a.Kind == "copilot" || a.Kind == "cursor" || a.Kind == "kiro" {
			driver = "managed"
		}
		// Deterministic idempotency key (conversation + launch intent): an LLM re-issuing
		// the same create_session reproduces it, so a timed-out-then-retried create
		// collapses onto the first session instead of spawning a duplicate.
		idemKey := createSessionKey(mcpConvID, a.Dir, a.Kind, a.Model, a.InitialPrompt, a.Worktree, a.Branch, a.NewBranch)
		reqBody, _ := json.Marshal(map[string]any{
			"dir":             a.Dir,
			"title":           a.Title,
			"kind":            a.Kind,
			"model":           a.Model,
			"initial_prompt":  a.InitialPrompt,
			"worktree":        a.Worktree,
			"branch":          a.Branch,
			"new_branch":      a.NewBranch,
			"driver":          driver,
			"report_to":       mcpConvID, // docs/30: 完了報告をこの会話へ（空なら無効）
			"idempotency_key": idemKey,
			// ADR 0029 §6: オペレーターが立てたセッションであることを出自として明示する。
			// 無人で増える消費（自動走行・定時実行との組み合わせ）を使用量集計で
			// 「人が開いたセッション」と分けるための軸。
			"origin":      session.OriginOperator,
			"origin_conv": mcpConvID,
		})
		out, err := agentCreateSession(reqBody, idemKey)
		if err != nil {
			return mcpToolErr(req.ID, "セッションの作成に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "send_to_session":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
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
		// confirm（docs/38 配達検証）: オペレーター送信は無人経路 — 打鍵 200 では
		// なく「ターンが実際に始まった証拠」まで待つ。飲まれた場合は Agent 側が
		// 自己修復（Enter 再送/再タイプ）し、それでも未確認なら delivery_unconfirmed
		// がツールエラーとして返る（停止中セッションへの指示空振り bc5d685e の対策）。
		reqBody, _ := json.Marshal(map[string]any{"prompt": a.Prompt, "report_to": mcpConvID, "confirm": true})
		out, resumed, err := agentSendToSession(a.Name, reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "Agent への送信に失敗しました: "+err.Error())
		}
		result := map[string]any{"sent": true, "resumed": resumed, "session": a.Name}
		if json.Valid([]byte(out)) {
			result["agent_result"] = json.RawMessage(out)
		}
		b, _ := json.Marshal(result)
		return mcpTextResult(req.ID, string(b))
	case "answer_session_question":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if len(a.Choices) == 0 {
			return mcpToolErr(req.ID, "choices（質問順の 1-based 選択肢番号の配列）が必要です")
		}
		reqBody, _ := json.Marshal(map[string]any{"choices": a.Choices})
		out, err := agentPOST("/sessions/"+url.PathEscape(a.Name)+"/answer-question", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "質問への回答に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "respond_session_plan":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは書き込みツールを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if a.Decision != "approve" && a.Decision != "reject" {
			return mcpToolErr(req.ID, "decision は approve か reject を指定してください")
		}
		reqBody, _ := json.Marshal(map[string]string{"decision": a.Decision, "feedback": a.Feedback})
		out, err := agentPOST("/sessions/"+url.PathEscape(a.Name)+"/plan-respond", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "プランへの応答に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "stop_session":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの停止を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		// disarm_report: オペレーターの停止＝指示の取り消しなので、arm 済みの
		// ワンショット報告を握りつぶす（後日の再開完了で古い報告が届かないように）。
		reqBody, _ := json.Marshal(map[string]bool{"disarm_report": true})
		out, err := agentPOST("/sessions/"+url.PathEscape(a.Name)+"/halt", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "セッションの停止に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "resume_session":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの再開を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		out, err := agentPOST("/sessions/"+url.PathEscape(a.Name)+"/start", nil)
		if err != nil {
			return mcpToolErr(req.ID, "セッションの再開に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "archive_session":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションのアーカイブを許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		out, err := agentPOST("/sessions/"+url.PathEscape(a.Name)+"/archive", nil)
		if err != nil {
			return mcpToolErr(req.ID, "セッションのアーカイブに失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_worktree":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは worktree の削除を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（worktree 名）が必要です")
		}
		if err := bridgeApprovalGate(approvalLabel("delete_worktree"), a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		// prune_sessions=1 で紐づく停止中メタも整理。force は付けない — dirty/ahead は
		// 保護のまま Agent 側で拒否させ、理由（要 push / Console で強制）を返す。
		out, err := agentDo(http.MethodDelete, "/repos/"+url.PathEscape(a.Name)+"?prune_sessions=1", nil)
		if err != nil {
			return mcpToolErr(req.ID, "worktree の削除に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_session":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはセッションの削除を許可されていません")
		}
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		if err := bridgeApprovalGate(approvalLabel("delete_session"), a.Name); err != nil {
			return mcpToolErr(req.ID, err.Error())
		}
		// reclaim=1 で jsonl も回収。消す前に gz 安全網へ退避される（復元可能）。
		out, err := agentDo(http.MethodDelete, "/sessions/"+url.PathEscape(a.Name)+"?reclaim=1", nil)
		if err != nil {
			return mcpToolErr(req.ID, "セッションの削除に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "delete_branch":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはブランチの削除を許可されていません")
		}
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
	case "restore_cleanup_archive":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはアーカイブの復元を許可されていません")
		}
		if a.ID == "" {
			return mcpToolErr(req.ID, "id（アーカイブ id）が必要です")
		}
		out, err := agentPOST("/cleanup/archives/"+url.PathEscape(a.ID)+"/restore", nil)
		if err != nil {
			return mcpToolErr(req.ID, "アーカイブの復元に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "purge_cleanup_archive":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントはアーカイブの完全削除を許可されていません")
		}
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
	case "list_assistants":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは他アシスタントへの相談を許可されていません")
		}
		out, err := agentGET("/assistants")
		if err != nil {
			return mcpToolErr(req.ID, "アシスタント一覧の取得に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	case "ask_assistant":
		if !mcpWriteEnabled {
			return mcpToolErr(req.ID, "このアシスタントは他アシスタントへの相談を許可されていません")
		}
		if a.Assistant == "" || a.Prompt == "" {
			return mcpToolErr(req.ID, "assistant（相手）と prompt（相談内容）が必要です")
		}
		reqBody, _ := json.Marshal(map[string]string{"assistant": a.Assistant, "prompt": a.Prompt})
		out, err := agentPOST("/chat/ask", reqBody)
		if err != nil {
			return mcpToolErr(req.ID, "相談の実行に失敗しました: "+err.Error())
		}
		return mcpTextResult(req.ID, out)
	}

	var path string
	switch p.Name {
	case "list_my_sessions":
		path = "/sessions"
	case "list_repos":
		path = "/repos"
	case "get_session_status":
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		path = "/sessions/" + url.PathEscape(a.Name) + "/status"
	case "get_session_output":
		if a.Name == "" {
			return mcpToolErr(req.ID, "name（セッション名）が必要です")
		}
		path = "/sessions/" + url.PathEscape(a.Name) + "/output"
		if a.Since > 0 {
			path += fmt.Sprintf("?since=%d", a.Since)
		}
	case "get_session_usage":
		path = "/sessions/usage"
		if a.Name != "" {
			path += "?name=" + url.QueryEscape(a.Name)
		}
	case "list_cleanup_candidates":
		path = "/sessions/cleanup"
	case "list_cleanup_archives":
		path = "/cleanup/archives"
	default:
		return mcpError(req.ID, -32602, "unknown tool: "+p.Name)
	}

	body, err := agentGET(path)
	if err != nil {
		return mcpToolErr(req.ID, "Agent への問い合わせに失敗しました: "+err.Error())
	}
	return mcpResult(req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": body}},
	})
}

// mcpTextResult returns a tools/call RESULT carrying a single text content block.
func mcpTextResult(id json.RawMessage, text string) []byte {
	return mcpResult(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
}

// mcpToolErr returns a tools/call RESULT with isError=true — an in-band error the
// model reads and can react to — rather than a JSON-RPC protocol error.
func mcpToolErr(id json.RawMessage, msg string) []byte {
	return mcpResult(id, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": msg}},
		"isError": true,
	})
}

// cpMemoDo calls the CP's /internal/memos bridge over the public hairpin (AF_CP_BASE_URL)
// authenticated by the per-membership AF_MEMO_TOKEN — the queue lives in the CP store,
// not the local Agent. Both env vars are injected by the CP only when PUBLIC_BASE_URL is
// set; absent them the memo feature is unavailable and we say so in-band.
func cpMemoDo(method, path string, body []byte) (string, error) {
	base := os.Getenv("AF_CP_BASE_URL")
	if base == "" || os.Getenv("AF_MEMO_TOKEN") == "" {
		return "", fmt.Errorf("メモ機能はこの環境では利用できません（CP の公開URL/トークンが未設定）")
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("AF_MEMO_TOKEN"))
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("CP メモAPI エラー (%d): %s", resp.StatusCode, string(b))
	}
	return string(b), nil
}

// cpScheduleDo calls the CP's /internal/schedules bridge over the public hairpin
// (AF_CP_BASE_URL) authenticated by the per-membership AF_SCHEDULE_TOKEN — schedules
// live in the CP store (docs/38), not the local Agent. Mirrors cpMemoDo; both env vars
// are injected by the CP only when PUBLIC_BASE_URL is set.
func cpScheduleDo(method, path string, body []byte) (string, error) {
	base := os.Getenv("AF_CP_BASE_URL")
	if base == "" || os.Getenv("AF_SCHEDULE_TOKEN") == "" {
		return "", fmt.Errorf("定時実行機能はこの環境では利用できません（CP の公開URL/トークンが未設定）")
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("AF_SCHEDULE_TOKEN"))
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("CP 定時実行API エラー (%d): %s", resp.StatusCode, string(b))
	}
	return string(b), nil
}

// withOwnerConv stamps owner_conv onto a create_schedule body so the schedule's
// completion reports (docs/30) land in the operator's own conversation. A client-
// supplied owner_conv is overridden — the operator only ever reports to itself. On a
// parse failure the original body is returned unchanged (the CP then validates it).
func withOwnerConv(args json.RawMessage, conv string) []byte {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil || m == nil {
		return []byte(args)
	}
	m["owner_conv"] = conv
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(args)
	}
	return b
}

// agentGET calls the local Agent REST with the shared AGENT_TOKEN.
func agentGET(path string) (string, error) { return agentDo(http.MethodGet, path, nil) }

// agentPOST calls the local Agent REST with a JSON body and the shared AGENT_TOKEN.
func agentPOST(path string, body []byte) (string, error) {
	return agentDo(http.MethodPost, path, body)
}

func agentDo(method, path string, body []byte) (string, error) {
	return agentDoTimeout(method, path, body, 15*time.Second)
}

func agentDoTimeout(method, path string, body []byte, timeout time.Duration) (string, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, agentBaseURL()+path, rdr)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := os.Getenv("AGENT_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &agentHTTPError{StatusCode: resp.StatusCode, Body: string(b)}
	}
	return string(b), nil
}

// createSessionKey derives a STABLE idempotency key from the launch intent so the LLM
// re-issuing create_session with the same arguments reproduces it — that is what lets a
// timed-out-then-retried create collapse onto the first session (see session_idempotency.go).
// Scoped by the conversation id so unrelated conversations never collide.
func createSessionKey(conv, dir, kind, model, prompt string, worktree bool, branch, newBranch string) string {
	h := sha256.New()
	for _, f := range []string{conv, dir, kind, model, prompt, strconv.FormatBool(worktree), branch, newBranch} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return "cs_" + hex.EncodeToString(h.Sum(nil))
}

// agentCreateSession POSTs /sessions and, crucially, does NOT let a client-side timeout
// turn a successful backend launch into a "failed" tool result the model would retry into
// a duplicate. It waits longer than the shared 15s client (launch does real work), and on
// a timeout OR a create_in_progress conflict it reconciles via the idempotency ledger:
// poll GET /sessions-idempotency/{key} until the session materializes, then return it. If
// the create genuinely failed the ledger clears and the original error is surfaced.
func agentCreateSession(body []byte, key string) (string, error) {
	out, err := agentDoTimeout(http.MethodPost, "/sessions", body, 40*time.Second)
	if err == nil {
		return out, nil
	}
	if key != "" && (isTimeoutErr(err) || isCreateInProgress(err)) {
		if out, ok := agentAwaitCreated(key, 45*time.Second); ok {
			return out, nil
		}
	}
	return "", err
}

func isTimeoutErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isCreateInProgress(err error) bool {
	var he *agentHTTPError
	return errors.As(err, &he) && he.hasCode("create_in_progress")
}

// agentAwaitCreated polls the idempotency lookup until the create resolves. Because the
// lookup returns 202 while still launching (which agentDo would treat as success), it
// inspects the raw status itself: 200 => the session, 202 => keep waiting, a short run of
// 404s => the create failed (or never registered) so give up, transport errors => retry.
func agentAwaitCreated(key string, deadline time.Duration) (string, bool) {
	url_ := agentBaseURL() + "/sessions-idempotency/" + url.PathEscape(key)
	end := time.Now().Add(deadline)
	notFound := 0
	for time.Now().Before(end) {
		status, body := agentRawGET(url_)
		switch {
		case status == http.StatusOK:
			return body, true
		case status == http.StatusAccepted:
			notFound = 0 // still launching
		case status == http.StatusNotFound:
			if notFound++; notFound >= 3 {
				return "", false
			}
		case status == 0:
			// transient transport error (agent momentarily unreachable) — retry
		default:
			return "", false
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return "", false
}

func agentRawGET(fullURL string) (int, string) {
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return 0, ""
	}
	if tok := os.Getenv("AGENT_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}

type agentHTTPError struct {
	StatusCode int
	Body       string
}

func (e *agentHTTPError) Error() string {
	return fmt.Sprintf("Agent API エラー (%d): %s", e.StatusCode, e.Body)
}

func (e *agentHTTPError) hasCode(code string) bool {
	var body struct {
		Code string `json:"code"`
	}
	return json.Unmarshal([]byte(e.Body), &body) == nil && body.Code == code
}

// agentSendToSession makes the orchestration contract atomic from the model's point
// of view: try delivery, resume only on the explicit stopped-state response, then
// retry delivery. Other conflicts (for example question_pending) remain errors and
// can never be reported as successful sends.
func agentSendToSession(name string, body []byte) (out string, resumed bool, err error) {
	inputPath := "/sessions/" + url.PathEscape(name) + "/input"
	state, err := agentSessionStatus(name)
	if err != nil {
		return "", false, fmt.Errorf("送信前の状態確認に失敗しました: %w", err)
	}
	if !state.Alive {
		return agentResumeAndSend(name, inputPath, body)
	}
	// /input with confirm (配達検証) blocks until the turn provably started — up to two
	// evidence windows plus one self-heal — so the client budget must exceed the
	// server-side worst case (agentPOST's default 15s does not).
	out, err = agentDoTimeout(http.MethodPost, inputPath, body, 45*time.Second)
	if err == nil {
		return out, false, nil
	}
	var httpErr *agentHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict || !httpErr.hasCode("not_running") {
		return "", false, err
	}
	// The session stopped between the status read and input POST. Apply the same
	// resume path as an initially stopped session.
	return agentResumeAndSend(name, inputPath, body)
}

func agentResumeAndSend(name, inputPath string, body []byte) (out string, resumed bool, err error) {
	if _, err = agentPOST("/sessions/"+url.PathEscape(name)+"/start", nil); err != nil {
		return "", false, fmt.Errorf("停止中セッションの再開に失敗しました: %w", err)
	}
	if err = agentWaitSessionReady(name, 30*time.Second, 500*time.Millisecond); err != nil {
		return "", true, err
	}
	// 45s for the same reason as the alive path: /input with confirm blocks until the
	// prompt provably became a turn (配達検証), beyond agentPOST's default 15s.
	out, err = agentDoTimeout(http.MethodPost, inputPath, body, 45*time.Second)
	if err != nil {
		return "", true, fmt.Errorf("再開後の送信に失敗しました: %w", err)
	}
	return out, true, nil
}

type agentSessionState struct {
	Alive bool `json:"alive"`
	Ready bool `json:"ready"`
}

func agentSessionStatus(name string) (agentSessionState, error) {
	var state agentSessionState
	out, err := agentGET("/sessions/" + url.PathEscape(name) + "/status")
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return state, err
	}
	return state, nil
}

func agentWaitSessionReady(name string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		state, err := agentSessionStatus(name)
		if err != nil {
			return fmt.Errorf("再開後の状態確認に失敗しました: %w", err)
		}
		if state.Alive && state.Ready {
			return nil
		}
		if time.Now().Add(interval).After(deadline) {
			return errors.New("セッションを再開しましたが、入力可能になる前にタイムアウトしました")
		}
		time.Sleep(interval)
	}
}

// agentBaseURL derives the loopback URL of the in-container Agent from AGENT_ADDR.
func agentBaseURL() string {
	addr := envOr("AGENT_ADDR", ":7700")
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "7700"
	}
	return "http://127.0.0.1:" + port
}
