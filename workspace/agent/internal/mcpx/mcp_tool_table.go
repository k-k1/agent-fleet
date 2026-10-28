package mcpx

// mcpStdioTools — read-only Agent Fleet tools (names are prefixed mcp__af__<name> by
// claude). Descriptions are prescriptive about WHEN to call (better trigger rate).
//
// Mixed languages on purpose: the two chromium entries are advertised to every session and are
// English for the reason given above mcpStdioSelfReportTools; the rest reach the assistant
// only and stay Japanese.
var mcpStdioTools = []map[string]any{
	{
		"name": "list_chromium_targets",
		"description": "List the existing Page targets on a Chromium CDP port exposed on loopback only. " +
			"Always call it before attach_chromium and pick the target Page from the returned target_id. " +
			"Do not start Chromium on a fixed port: if another session already holds it, yours does not fail - " +
			"it silently binds another loopback family, and what is listed here is somebody else's browser. " +
			"Start with --remote-debugging-port=0 and pass the port from line 1 of " +
			"<user-data-dir>/DevToolsActivePort. The returned browser_id must equal the GUID on line 2 of that " +
			"file; if it does not, it is a different instance, so do not attach. " +
			"Never put a CDP endpoint, cookie, password or token into an answer, a log or a commit. " +
			"Where it answers code=browser_unavailable this workspace runtime has no browser features at all.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "The Chromium remote-debugging port listening on 127.0.0.1 (start with --remote-debugging-port=0 and pass line 1 of DevToolsActivePort)"},
			},
			"required": []string{"port"},
		},
		"outputSchema": chromiumTargetsOutputSchema(),
	},
	{
		"name": "get_chromium_attachment",
		"description": "Check a Chromium attachment's state, viewer connection, control mode, action result and expiry. " +
			"Do not poll it on a short cycle; call it only when you need it, such as after the user has acted.",
		"inputSchema":  chromiumAttachmentIDInputSchema(),
		"outputSchema": chromiumAttachmentOutputSchema(),
	},
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
		"description": "指定セッションの端末出力を返す。あるセッションの最近の出力/結果を要約・確認する時に呼ぶ。長い出力は末尾のみ返す（clipped=true・先頭は省略される — 直近の結果を読むにはそれで足りる）。since を省略すると、この会話で前回取得した続き（差分）から返す（同じ内容を二度読まない）。過去の出力を読み直したい時だけ since を明示する（例: since=0 で先頭から）。",
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
		"description": "各セッションのコンテキスト使用量と累積消費トークンを返す。name 指定で1セッション、省略で transcript を持つ全セッション（shell / SSM は対象外）。context は現在のコンテキスト量（tokens と read/create/fresh の内訳、window に対する pct%。最初の応答が返るまでは無く、自動圧縮後は圧縮後の値）。cumulative は累積消費（論理ターン数 turns、inTok/outTok/cacheRead/cacheCreate、spend=inTok+cacheCreate+outTok の合計）。注意: agy / cursor は一覧に含まれるが transcript にトークン情報が無いため context は空・cumulative は全て 0 になる（消費ゼロの意味ではない。agy の残枠は get_agent_usage を見る）。kiro は転写にトークンが無いが、managed（ACP）セッションが稼働中は _kiro.dev/metadata のライブ値から context（pct＋実 window に対する概算 tokens）と cumulative.credits（消費クレジット）を返す（停止中や TUI 実行は context 空）。copilot は outTok のみ記録され inTok/cache は 0・context は無い。muse は cumulative を持つが subagent / observer の呼び出しが上流のワイヤに載らないため過少（measured=partial）で、context は無い。『どのセッションがコンテキスト逼迫か』『どれだけ消費したか』を聞かれた時や、引き継ぎ・圧縮・新セッション分割の判断材料に呼ぶ。サブスクリプション枠の残量は get_agent_usage（別ツール）。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "セッション名（任意。省略で全セッション）"},
			},
		},
	},
	{
		"name":        "get_agent_usage",
		"description": "各エージェント CLI のサブスクリプション使用量とレート制限を返す（claude / codex / agy / muse。opencode / copilot / cursor / kiro は使用量ソースが無いため含まれない）。claude / codex は fiveHour（5時間利用枠）と sevenDay（週間利用枠）の pct が使用率（0–100）、resetsAt が解除日時（ISO 8601）で、codex は planType や resetCredits も付く。agy は形が異なり、account / plan と groups（クォータ枠ごとに label・remainingPct・resetsAt。実験枠 Starter 等）を返す。muse は claude / codex と同じ形だが、値はホストが最後に観測したもので、稼働中の muse セッションがターンを1つも終えていない間は ok=false になる（使用量ゼロではなく観測が無いという意味）。windowMins は現在の利用枠の長さ（分）。authed=false はその CLI に未ログイン、ageSec は計測の古さ（秒）。『あとどれくらい使える?』『制限はいつ解除?』と聞かれた時や、大きなタスクをセッションに振る前の判断材料に呼ぶ。",
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
		"description": "定時実行スケジュールの一覧を返す（docs/log/38）。各スケジュールは id / spec_kind(cron|interval|once) / spec / spec_label(登録時の自然言語) / tz / enabled / next_run(次回発火 UTC) / next_run_local(tz でのわかりやすい表記) / last_run / last_status / prompt などを持つ。利用者に「今どんな定時タスクがある?」と聞かれた時や、update_schedule / delete_schedule / pause_schedule / run_schedule_now で対象 id を選ぶ前に呼ぶ。",
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
	{
		// docs/log/39 P4. export / import are deliberately not exposed: P3 put a secret
		// scan and the user's explicit ack in front of an export, and an MCP route where
		// the model acks and writes the file itself would be a second exit bypassing that
		// defence. Export and import stay a Console action the user performs.
		"name": "list_memory_snapshots",
		"description": "エージェントメモリ（claude の auto-memory / codex の memories）の変更履歴を新しい順に返す。各行は時刻・契機（auto/manual/pre-restore/restore/import）・変更されたプロジェクトを持つ。" +
			"「メモリがいつ変わったか」「おかしくなったのはいつからか」を聞かれた時や、restore_memory_snapshot で戻す時点を選ぶ前に呼ぶ。中身の差分は get_memory_snapshot を見る。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "description": "返す件数（既定 20）"},
			},
		},
	},
	{
		"name": "get_memory_snapshot",
		"description": "指定時点のエージェントメモリの中身を返す。その時点に何が入っていたか（kind 別・プロジェクト別のファイル数）と、その snapshot が入れた変更の差分を返す。" +
			"rev（list_memory_snapshots の id）か at（日時。その時刻以前の直近 snapshot に解決）のどちらかを指定する。restore_memory_snapshot で戻す前に「戻したら何がどう変わるか」を確認するために呼ぶ。差分が大きい時は path で絞る。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"rev":  map[string]any{"type": "string", "description": "snapshot id（list_memory_snapshots の id）"},
				"at":   map[string]any{"type": "string", "description": "日時（RFC3339 等）。その時刻以前の直近 snapshot に解決する"},
				"path": map[string]any{"type": "string", "description": "差分を絞る repo 内パス（例: claude/projects/<slug>）。省略で全体"},
			},
		},
	},
}

// mcpStdioWriteTools — Agent Fleet write/orchestrate tools, advertised only under --write
// (docs/log/19 af_write opt-in): drive tmux sessions (send_to_session) AND consult other
// assistants (list_assistants / ask_assistant). Consults are advisory-only by construction
// (the sub-turn runs with no tools), so they can't loop or escalate.
var mcpStdioWriteTools = []map[string]any{
	{
		"name": "attach_chromium",
		"description": "Connect Agent Fleet's display and input route to an existing Page confirmed through list_chromium_targets. " +
			"When connecting to a Chromium you started yourself, always pass the GUID from line 2 of " +
			"DevToolsActivePort as expected_browser_id (it prevents connecting to another session's browser on a " +
			"port collision). " +
			"**The control mode right after attaching is view-only, and every scroll and key the user sends is " +
			"rejected.** " +
			"To let the user operate it, stop your own automation against that Page and move it to user-control " +
			"with request_browser_action (or set_chromium_control_mode). " +
			"Hand over the link without doing that and the user gets a pane that is visible but does nothing. " +
			"Present the returned open_url unchanged, as a Markdown link reading \"open the browser and operate it\". " +
			"The link opens as a pane in the Console, not a new tab. " +
			"Do not click a final confirming action for the user, and do not restate a successful attach as a " +
			"successful operation on the external site.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"port":                map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "The Chromium remote-debugging port you passed to list_chromium_targets"},
				"target_id":           map[string]any{"type": "string", "minLength": 1, "description": "A target_id returned by list_chromium_targets"},
				"expected_browser_id": map[string]any{"type": "string", "description": "The Chromium instance you expect to reach (line 2 of DevToolsActivePort, /devtools/browser/<GUID>, or that GUID). A mismatch refuses the attach"},
				"label":               map[string]any{"type": "string", "description": "Any short label to show in the Console"},
			},
			"required": []string{"port", "target_id"},
		},
		"outputSchema": chromiumAttachOutputSchema(),
	},
	{
		"name": "detach_chromium",
		"description": "End only Agent Fleet's Chromium connection and screencast. It does not close the owner's Page, " +
			"BrowserContext, profile or Chromium process. Call it after completion or cancellation is confirmed.",
		"inputSchema": chromiumAttachmentIDInputSchema(),
		"outputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"attachment_id": map[string]any{"type": "string"},
				"detached":      map[string]any{"type": "boolean"},
			},
			"required": []string{"attachment_id", "detached"},
		},
	},
	{
		"name": "request_browser_action",
		"description": "Create or update the instructions that hand a Chromium attachment over to the user. " +
			"Stop the owner side's automation against that Page before moving to user-control. " +
			"Do not perform the final confirming action on their behalf; completion and cancellation are the user's " +
			"own report, not proof that the operation on the external site succeeded. " +
			"When the user responds, the outcome arrives automatically as new input in this session's conversation " +
			"(a stopped session is resumed), so this tool itself returns immediately without waiting. " +
			"Once it arrives, check the structured result with get_browser_action_result.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"attachment_id":    map[string]any{"type": "string", "minLength": 1},
				"message":          map[string]any{"type": "string", "minLength": 1, "description": "The specific check or operation you are asking the user for"},
				"completion_label": map[string]any{"type": "string", "description": "Any label for the completion button"},
				"allow_cancel":     map[string]any{"type": "boolean", "description": "Whether to allow cancelling (false when omitted)"},
				"control_mode":     chromiumControlModeSchema(),
			},
			"required": []string{"attachment_id", "message"},
		},
		"outputSchema": chromiumActionRequestOutputSchema(),
	},
	{
		"name": "get_browser_action_result",
		"description": "Check the user's self-reported result (pending/completed/cancelled) for the browser action you asked for. " +
			"Do not poll it on a short cycle, and do not treat the result as proof that the operation on the " +
			"external site succeeded.",
		"inputSchema": chromiumAttachmentIDInputSchema(),
		"outputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"attachment_id": map[string]any{"type": "string"},
				"result":        map[string]any{"type": "string", "enum": []string{"pending", "completed", "cancelled"}},
			},
			"required": []string{"attachment_id", "result"},
		},
	},
	{
		"name": "set_chromium_control_mode",
		"description": "Change whether a Chromium attachment accepts input: view-only / user-control / locked. " +
			"Stop the owner side's automation against that Page before moving to user-control.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"attachment_id": map[string]any{"type": "string", "minLength": 1},
				"control_mode":  chromiumControlModeSchema(),
			},
			"required": []string{"attachment_id", "control_mode"},
		},
		"outputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"attachment_id": map[string]any{"type": "string"},
				"control_mode":  chromiumControlModeSchema(),
			},
			"required": []string{"attachment_id", "control_mode"},
		},
	},
	{
		"name":        "list_models",
		"description": "指定エージェントで現在選べるモデル一覧を返す。model 指定で create_session する前には必ず呼び、返った id を使うこと（一覧は利用者が「使わないモデル」で除外したものを除いてある — 記憶や過去の会話にあるモデル名を推測で渡さないこと。除外モデルを渡した create_session は拒否される）。claude は固定の最新ティア別名と、利用者がエージェント設定で登録した完全モデル ID を返す。Claude Code OAuth にはアカウント連動カタログがないため、登録モデルの可否は起動時に判定される。codex／opencode／agy／copilot／cursor／kiro は接続状態を反映したライブカタログ（copilot はプラン反映 — Free は Auto のみで空になる。cursor は effort をモデル id に畳んだアカウント連動カタログ。kiro は Free でも named 指定可・既定は auto。未指定は auto ルーティング）。lcpp はログイン不要で、自前エンジンの目録に載っているモデルをそのまま返す（エンジンが無い配備では空になる）。muse はログイン済みのアカウント連動カタログを返す（既定は「製品改善に使われうる」条項の付かない最新モデル）。利用者が terra のような略称で指定した場合も、一覧から対応する完全な id（例: gpt-5.6-terra）を選ぶ。opencode は同じモデルが 2 つの課金経路で並ぶことがある（opencode-go/… = Go サブスクの範囲内、opencode/… = Zen の従量課金）。同名が両方にある場合は先に並んでいる opencode-go/… を選ぶこと（一覧の並びは利用者の設定で整形済み）。利用者が Zen を明示した場合だけ opencode/… を使う。", // model-id-lint:allow an example id; it names no default.
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "description": "claude / codex / opencode / agy / copilot / cursor / kiro / lcpp / muse"},
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
			"codex／opencode／copilot／cursor／kiro は既定で managed で開始し（TUI は使わない）、lcpp と muse は常に managed で開始する。model を指定する前には必ず list_models で利用可能な id を確認し、その id を渡す。作成したセッションが入力待ちになる／異常終了すると、この会話に自動で報告が届くのでポーリングは不要。報告が届いたら内容（必要なら get_session_output）を確認して次の行動を決める。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dir":            map[string]any{"type": "string", "description": "作業ディレクトリ（リポジトリの作業コピー等）。省略時はホーム。list_my_sessions の dir か list_repos の path を渡す。"},
				"title":          map[string]any{"type": "string", "description": "セッションの表示名（任意）。何のタスクかが分かる短い名前。"},
				"kind":           map[string]any{"type": "string", "description": "エージェント種別（任意）。claude（既定）| codex | opencode | agy | copilot | cursor | kiro | lcpp | muse | shell。agy は Antigravity CLI（接続済みのときのみ起動可）。copilot は GitHub Copilot CLI（GitHub 連携＋Copilot サブスクが前提）。cursor は Cursor CLI（接続済みのときのみ起動可）。kiro は Kiro CLI（接続済みのときのみ起動可・既定は managed ドライバ）。lcpp は自前の llama.cpp ハーネス（ログイン不要・常に managed・Terminal (CLI) 経路は無い）で、model は list_models(kind=\"lcpp\") が返す自前エンジンの目録から選ぶ。muse は Meta の Muse Code（オンデマンド導入＋ログイン済みのときのみ起動可・常に managed・Terminal (CLI) 経路は無い）。shell は生のシェルで initial_prompt/送信文字列がそのままコマンド実行される（エージェントのガードレール無し）ため、起動前に実行内容を利用者へ確認すること。"},
				"model":          map[string]any{"type": "string", "description": "モデル上書き（任意）。"},
				"effort":         map[string]any{"type": "string", "description": "推論 effort（任意）。model と一緒に指定し、list_models が返すそのモデルの efforts から選ぶ（それ以外や model 省略は拒否される）。省略時はモデルの defaultEffort。opencode と kiro は一覧に efforts が無く、ここでは値を確かめない（誤った値は子の最初のターンで失敗する）。agy と cursor は effort がモデル id に含まれるので指定しない。"},
				"initial_prompt": map[string]any{"type": "string", "description": "起動後に自動送信する最初のタスク/引き継ぎ文（任意）。"},
				"worktree":       map[string]any{"type": "boolean", "description": "dir から新しい独立 worktree を作成して起動する（任意、既定 false）。"},
				"branch":         map[string]any{"type": "string", "description": "worktree の基点ブランチ（任意、省略時は現在の HEAD）。"},
				"new_branch":     map[string]any{"type": "string", "description": "worktree に作る新規ブランチ名（任意、省略時は仮ブランチを自動生成）。"},
				"subdir":         map[string]any{"type": "string", "description": "作業ディレクトリ内の相対パス（任意）。エージェントをその配下で起動する（例: console／apps/web）。worktree=true のときは新しく作られた worktree 内の相対パスとして解釈される。存在しないパスは拒否される。"},
			},
		},
	},
	{
		"name": "get_chat_plan",
		"description": "この会話に固定されている作業計画（docs/log/33 第5段）を返す。作業計画は要約を通さず**原文のまま**新しいセッションへ毎回引き継がれる枠で、" +
			"コンテキスト圧縮で会話の記憶が畳まれても消えない。利用者が Console 側で計画を書き換えていることがあるので、" +
			"長い作業の再開時や、計画に沿っているか確かめたいときに読むこと（会話履歴を読み直すより安い）。",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "set_chat_plan",
		"description": "この会話の作業計画（docs/log/33 第5段）を書き換える。**全文置換**なので、まず get_chat_plan で現在の計画を読み、それを土台に変更点だけを反映した全文を渡すこと。" +
			"ここに書いた内容は要約されず、原文のまま以降の新しいセッションへ毎回引き継がれる — つまり『圧縮されても絶対に忘れない場所』。" +
			"利用者と壁打ちして段取り・担当・順序が決まった直後や、レーンが1つ終わって次の波に進んだときに更新する。" +
			"書き方は次の3見出しで、**完了した作業を網羅列挙しないこと**（git や課題管理システムを見れば分かることは書かない）:\n" +
			"## 制約（環境・禁止事項・運用ルールなど、この先ずっと効く前提）\n" +
			"## 前提（次の一手に必要な既成事実だけ。ID・ブランチ名・意図的な例外など）\n" +
			"## これからやること（順序・依存・分岐条件）\n" +
			"判断基準は「完了したか」ではなく『これが無いと次の一手を間違えるか』。計画を空にはできない（不要になったら利用者が Console の作業計画パネルから消す）。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"plan": map[string]any{"type": "string", "description": "置き換え後の作業計画の全文（Markdown）。"},
			},
			"required": []string{"plan"},
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
				"report":                map[string]any{"type": "boolean", "description": "完了報告をオペレーター会話に届けるか（任意。false=報告しない）"},
				"stop_after_run":        map[string]any{"type": "boolean", "description": "実行が終わったらセッションを停止するか（任意。false=起動したまま）"},
				"deliver_to":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "届け先（任意）: operator | notifications | discord | slack"},
				"silent":                map[string]any{"type": "boolean", "description": "[SILENT] だけの回答を届けない（任意）"},
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
	},
	{
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
			"未コミット/未pushの変更がある worktree は保護のため削除できない（keep 候補。Console で強制削除するよう案内する）。その worktree の停止中の AI セッションはアーカイブ（棚）へ移り、shell/ssm はごみ箱へ入る（どちらも復元できる。会話は消えない）。ローカルの作業コピーだけが消え、履歴・リモート・ブランチは残る。削除した worktree もごみ箱に記録され、restore_cleanup_archive で同じパスに作り直せる。破壊的操作なので、どの worktree を消すかを一言添えて実行前に必ず利用者へ確認すること。",
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
		// Destructive but reversible: a pre-restore snapshot is always taken before the
		// restore is applied (docs/log/39 ④). Without "this can be undone" in the
		// description, the model either plays it too safe and refuses what the user asked
		// for, or takes it too lightly and skips the confirmation.
		"name": "restore_memory_snapshot",
		"description": "エージェントメモリを指定時点へ戻す。rev（list_memory_snapshots の id）か at（日時）で戻す先を指定し、範囲は all（全体）／projects（claude のプロジェクト単位）／kinds（claude・codex 単位）で指定する。範囲は必ず明示すること（省略は拒否される）。" +
			"履歴は書き換えず、戻す直前の状態も自動で snapshot に残るので、この操作自体を後から取り消せる。誤って消した／誤学習したメモリを戻す時に使う。" +
			"実行前に必ず get_memory_snapshot で戻り先の中身を確認し、『どの時点へ・どの範囲を戻すか』を利用者に示して承認を得ること。対象 kind のセッションが実行中だと結果の busy=true で返る。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"rev":      map[string]any{"type": "string", "description": "戻す先の snapshot id（list_memory_snapshots の id）"},
				"at":       map[string]any{"type": "string", "description": "戻す先の日時（RFC3339 等）。その時刻以前の直近 snapshot に解決する"},
				"all":      map[string]any{"type": "boolean", "description": "true で全体を戻す"},
				"projects": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "戻す claude プロジェクトの slug（get_memory_snapshot の projects）"},
				"kinds":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "まるごと戻す kind（claude / codex）"},
			},
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
