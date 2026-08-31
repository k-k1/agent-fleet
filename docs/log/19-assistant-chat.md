# 19. アシスタント・チャット（ヘッドレスCLI方式）

Agent Fleet 利用者の作業を補助する **LLM チャット/翻訳アシスタント**機能。
Markdown ドキュメントの翻訳を投げる、チャットセッションを開始する、といった用途を想定。
既存の tmux コーディングエージェント（claude/codex/opencode/shell/ssm）とは**別物**として追加する。

> ステータス: 🗄 **実装済（歴史的記録）**。当初「Phase A 実装中（`feat/assistant-chat`）」として書き起こしたが、
> その後 Phase B/C・アシスタント・テンプレート・書き込みツール opt-in まで実装済み（本文の各節に「実装済」を追記）。

## 背景と外部依存の実態（深掘りの結論）

当初 Copilot / Gemini を検討したが、**「Office 365 / Google Workspace に含まれる = API で叩ける」ではない**ことが判明:

- **M365 Copilot**: フル機能は $30/user/月 の有償アドオン。無償の Copilot Chat には公開チャット API が無い。
  Copilot Chat API（Graph `/copilot/`）は**プレビュー(/beta・本番非サポート)＋有償アドオン必須**。
- **Gemini**: Workspace の Gemini は製品機能であって API 利用権ではない。API には AI Studio キーか
  Vertex AI（GCP プロジェクト＋課金）が別途必要。

→ 方針転換: **既に使っている claude / codex を使う**。

### 「CLI ではなく API を使えるか？」への答え

- ❌ **サブスクの OAuth トークン（`CLAUDE_CODE_OAUTH_TOKEN`）を生 API に流用**: ToS 違反。
  2026-02 の ToS 改定でサブスク OAuth は **Claude Code / Claude.ai 専用**に限定
  （製品を作るなら API キー認証を使え、と明記）。Codex/ChatGPT サブスクも同様。
- ✅ **生 API（API キー）**: クリーンだがサブスクとは別の従量課金＝新規コスト＋新規資格情報。
- ✅✅ **CLI をヘッドレス/非対話モードで叩く** ← **採用**。今の per-user サブスク認証をそのまま使い、
  ToS 姿勢も不変（各ユーザーのトークンが自分の作業に対して動く）、**追加課金ゼロ・追加資格情報ゼロ**。
  - claude: `claude -p <prompt> --output-format json --session-id <uuid>`（継続は `--resume <uuid>`）
  - codex: `codex exec --json <prompt>` / `codex exec resume <id> <prompt>`

将来 API キー方式が必要になれば、下記プロバイダ抽象の背後に差し替えで足せる。

## アーキテクチャ

既存コードの2大制約に基づく:

1. **既存セッションは全て tmux の PTY**。今の "チャット"(`MirrorView`)は TUI トランスクリプト読み取り＋
   `tmux send-keys` のファサードで、本物のチャットチャネルではない。→ LLM チャットはこれとは別実装。
2. `Agent` インターフェース(`agent.go`)は `buildLaunch` が tmux プログラムを返す契約。チャットを
   無理に `SessionKind` として通すとセッション機構(一覧/アーカイブ/idle 判定)を壊す。

→ **チャットは tmux セッションではなく、Agent 内の並列サブシステム(`chat.go`)**として作る。

```
Console: PaneKind "chat" + ChatView（xterm 非依存）
          ├ 左レール「アシスタント」セクション（会話一覧 + 新規チャット）
          └ Files/DocView から「この .md を翻訳」→ chat ペインを開く（Phase C）
   │ (JSON / SSE、X-AF-Tenant)
CP:  /api/chat/... を proxyAgentREST に登録（kind 非依存。非ストリームは既存プロキシで素通り）
   │
Agent: chat.go = 会話ストア(home 内 JSON) + ChatProvider 抽象
          ├ claudeChat（claude -p）… Phase A
          └ codexChat （codex exec）… Phase A.2（実機検証後）
```

### kind は2層に分かれる

- **ビュー層（PaneKind）は1つ**: `"chat"`。チャット UI は claude/codex で同じ描画なので1種類。
- **プロバイダ/会話タグ層は agent 毎**: 会話は `agent: claude|codex` を持ち、`chat.go` の
  `chatProviders[agent]` で `claude -p` / `codex exec` に分岐。**既存のエージェント種別レジストリを再利用**し、
  `caps.headlessChat` を追加して「新規チャット」ピッカーに出す kind を制御する（新レジストリは作らない）。

## API（Agent、CP が `/api` 前置でプロキシ）

- `GET  /chat/conversations`            会話一覧（メタのみ）
- `POST /chat/conversations`            作成 `{agent, title?, model?}`
- `GET  /chat/conversations/{id}`       会話全文
- `PATCH /chat/conversations/{id}`      表示名変更 `{title}`（一覧のタイトルを後から編集）
- `POST /chat/conversations/{id}/messages`  送信 `{content}` → user 追記 → プロバイダ実行 → assistant 追記
- `DELETE /chat/conversations/{id}`     削除

会話は `~/.config/agent-fleet/chats/<id>.json`（1会話1ファイル、`0o600`）。プロバイダの継続 ID
（claude session-id / codex session-id）も会話レコードに保持。

## フェーズ

- **Phase A**: claude プロバイダで縦切り1本（非ストリーム）。会話ストア + `chat.go` + エンドポイント +
  CP ルート + `PaneKind "chat"` + 最小 `ChatView` + 左レール + `headlessChat` cap（claude のみ）。
- **Phase A.2**: codex プロバイダ実機検証 → cap に codex 追加。
- **Phase B（実装済）**: トークンストリーミング。Agent `handleChatStream`（`claude -p --output-format stream-json
  --verbose --include-partial-messages` の JSONL を parse、`content_block_delta`/`text_delta` を SSE 配信）、
  CP `proxyAgentStream`（チャンク毎に Flush）、Console `chatStream`（fetch ストリーム読取り）+ ChatView 逐次表示。
  会話継続は非ストリームと同じ session_id 保持。ストリーミング非対応プロバイダは send() へフォールバック。
- **Phase C（Files 右クリック連携＝実装済）**: Files のファイル/ディレクトリ右クリックに
  「アシスタントで開く ▸」を追加（詳細は下記）。**残**: FileView/DocView ヘッダにも同じ導線のボタン。
- **Phase D**: （任意）API キー方式プロバイダ（生 Anthropic/OpenAI API、従量課金）を抽象越しに追加。

## Agent Fleet アシスタント拡張（検討・決定）

Phase C の前に、アシスタントを「Agent Fleet の操作・使い方案内」に踏み込ませる方向を検討し、以下を決定。

### 前提（現物確認）
- **AF MCP サーバ** = CP `control-plane/mcp.go`、パス `/mcp`、`AF_MCP_ENABLED=true` で有効化（既定 OFF）、
  Streamable-HTTP、**PAT(Bearer) 認証・テナントはトークン固定**。member ツール
  (`list_my_sessions`/`get_session_status`/`get_session_output`/`send_to_session`) は内部で Agent REST を叩く。
- **コンテナ→CP の直接到達は不可**（専用 docker ブリッジ網 / CP はホスト 127.0.0.1:8099 専用、URL 未注入）。
  唯一の経路は `<PUBLIC_BASE_URL>/mcp` へのヘアピン egress。Agent(localhost:7700) は REST のみで MCP なし。
- `CLAUDE_CONFIG_DIR=/var/lib/af/claude`（専用マウント）を全セッションで共有。チャットの `claude -p` も継承。

### Q3: セッションと .claude を分ける → **認証だけは単一パスへ再統合（2026-07修正）**
当初はチャット専用 `CLAUDE_CONFIG_DIR` を作り、`.credentials.json` だけ共有 dir への symlink
にした。しかし Claude の refresh は資格情報を tmp＋rename で置換するため、チャット側の
symlink が実ファイルへ変わる瞬間がある。copy-back までの間に対話セッションも refresh すると、
異なる refresh token が並行して保存され、片方が失効し得る。

修正後はチャットも共有 `CLAUDE_CONFIG_DIR` を直接使い、OAuth ファイルを物理的に1個へ戻した。
分離したかった他の要素は CLI 契約で遮断する:
- user/project/local settings・hooks: `--setting-sources ""`
- 他の MCP: `--strict-mcp-config`（チャット用 `--mcp-config` だけ明示）
- shell/file mutation: provider別 deny/read-only sandbox

旧 `chat-claude/projects` の jsonl は、既存会話の `--resume` を保つため初回実行時に共有
projects へ create-only でコピーする。資格情報・settings は移行対象外。

### Q1: ws から AF 操作 → **コンテナ内ローカル stdio MCP（実装済・リードオンリー）**
CP `/mcp` はヘアピン＋PAT 発行が要り筋が悪い（外部/管理者/横断向けに温存）。自ワークスペース操作は
**`workspace-agent mcp-stdio` サブコマンド**（`mcp_stdio.go`：newline-delimited JSON-RPC 2.0 over stdio。
読み取りツール `list_my_sessions`/`get_session_status`/`get_session_output` が **localhost:7700 の Agent REST＋
`AGENT_TOKEN`** を叩く。`initialize`/`tools/list`/`tools/call`/`ping` 対応、tools/call の失敗は in-band `isError`）。
`main.go` に `mcp-stdio` サブコマンド登録。`chat.go` の `chatMCPArgs()` がチャットの `claude -p` に
`--mcp-config '{"mcpServers":{"af":{"command":"<agentExe>","args":["mcp-stdio"]}}}' --strict-mcp-config` を付与。
PAT 不要・egress 不要・設定漏れなし・身元＝自コンテナ。会話フラグ `AFTools`（claude は既定 on）でゲート。
protocol はスタンドアロン検証済（実 Agent 越しのツール実行はコンテナ内で要目視）。
**リードオンリー（書き込みツール未公開）**。`send_to_session` 等は**後で公開＋ユーザーが明示有効化**（Q2 のペルソナ/トグル）。

### 保守メモ: 2つの MCP 面は別実装（自動同期しない）
CP `mcp.go`（外部/PAT/admin・別モジュール）と Agent `mcp_stdio.go`（ws内/read-only member）は独立。
ツール追加は載せたい面を手で追記（両方に出すなら2箇所）。数が増えて面倒なら
「name/description/schema→REST パス対応表」を共有パッケージ/JSON カタログに1本化して両面が読む形へ。
当面は意図的に別スコープ（read-only vs admin）として二重管理を許容。

### Q2 → 「アシスタント・テンプレート」機能（常設 AF + ユーザー定義、**実装済**）
単一ペルソナでなく、**アシスタントを設定可能なエンティティ**にする（カスタム GPT 的）:
- **Assistant** = {id, name, icon?, builtin, agent(claude/codex), model?, persona(system prompt),
  tools(af_read / af_write / none), knowledge?(USAGE 等 doc を --add-dir で読ませる)}。
- **常設ビルトイン**（5種。※時系列は下記補記参照）: ①「Agent Fleet アシスタント」(af_read, 案内役・観測役, USAGE知識)
  ②「フリート・オペレーター」(af_write, 司令塔＝セッションに send_to_session で指示を出す実行役)
  ③「整合性チェッカー」(tools=none, 添付対象の食い違い/乖離/表記ゆれ/設定矛盾を列挙、dev/docs/小説横断)
  ④「汎用」⑤「翻訳」。①と②の差は read/write のみ＝既定を書き込み化せず分離（af_write は明示 opt-in 原則）。
  - **時系列補記（5種↔3種の食い違いの整理）**: 初期実装（`0f94a413`, 2026-07-05）はビルトイン**3種**
    （AF アシスタント/汎用/翻訳）＝下記実装メモの「3種」はこの時点の記録。同日 `6bf14876` で
    オペレーター・整合性チェッカーを追加し設計どおり**5種**に。その後 docs/30 ②（`0cd15874`, 2026-07-17）で
    整合性チェッカー/汎用/翻訳を削除し、現行ビルトインは AF アシスタント/フリート・オペレーター/
    SRE アシスタント（docs/25 で追加）の3種（`assistants.go` `builtinAssistants()` が正）。
- **ユーザー定義**: 名前＋persona＋model＋ツール許可を作成/編集（`~/.config/agent-fleet/assistants/<id>.json`、
  ビルトインはコードで注入してマージ）。**書き込みツール(af_write=send_to_session 等)は各アシスタントで
  ユーザーが明示 opt-in** した時だけ `mcp_stdio` に公開/`--allowedTools` 許可。
- **会話**は `assistant_id` を持ち、作成時にアシスタントの agent/model/persona/tools を継承。
  `chat.go` provider が persona→`--append-system-prompt`、model→`--model`、knowledge→`--add-dir`、
  tools→`AFTools`＋（将来）書き込みツール公開を出し分け。
- **Console**: AssistantSection を「アシスタント（常設 AF ＋ ユーザー定義）＋各会話」に再構成、
  「＋新規チャット（アシスタント選択）」、アシスタントの作成/編集 UI（名前/persona/model/ツール許可）。
- 利用者向け USAGE/FAQ は別途用意（内部 docs/ は出さない）。

**会話開始 UX（実装済）**:
- **description（自己紹介）**: assistant に user-facing な `description` を追加（persona＝モデル指示とは別）。
  builtin 5種に一人称の挨拶文を用意。作成/編集 UI にも「説明（会話開始時の挨拶）」欄。
- **挨拶カード**: 会話が未開始（draft or messages 空）の間、ChatView が assistant の description を
  挨拶カードとして表示（**静的**＝ライブのモデル turn を消費しない。req: 会話開始時に説明表示／自己紹介）。
- **draft モード（未開始は保存しない）**: 左レールでアシスタントを選ぶと **conversation を作らず** draft ペイン
  （Pane.draftAssistantId、conversationId=null）を開く。**最初のメッセージ送信時に初めて `chatCreate`**→
  `promoteDraft(paneId, id)` でペインを実会話に昇格→stream。load 効果は convRef ガードで昇格時の再読込
  （＝streaming 中断）を回避。会話一覧は message_count>0 のみ表示（Files 右クリックの即時作成が放置された
  空会話も一覧に出さない）。`chatListKey`/`bumpChatList` で draft→実会話化を左レールに反映。
  Files 右クリック連携は従来どおり即時作成（seed 前提のため）。
- **composer フォーカス**: ChatView は pane が active になったら composer に自動フォーカス（会話/draft を
  開いた時・対象切替時）。ただし `coarsePointer()`（タッチ端末）では自動フォーカスしない＝スマホで
  開いただけでキーボードが出るのを防ぐ（MirrorView と同じ方針。Pane が `active` を渡す）。
- **左レールの縦幅対策**: アシスタント一覧を常設リストから **「＋新規」ピッカー・ポップオーバー**へ移動
  （`useDismiss`＋`placeFixed` でアンカー）。セクション本体は**会話履歴のみ**＝一覧が縦幅を恒常的に食わない。
  作成/編集/削除もピッカー内。**z-order**: ポップオーバー/コンテキストメニュー展開中は
  `.pane-section:has(.assistant-picker/.ctxmenu)` でセクションを z-index:10 に持ち上げ、後続セクション
  （sticky ヘッダ z-index:3）に被られないようにする（既存 launch-menu と同パターン）。
- **ピッカー行の操作**: 左クリック＝アクティブペインで draft、**Ctrl/⌘+クリック・中クリック＝新ペイン**、
  **右クリック＝コンテキストメニュー**（新規チャット/新しいペインで開く/編集/削除）。会話行も Ctrl/中クリックで
  新ペイン。`shows()` に chat の同一判定（conversationId / draftAssistantId）を追加し、`openInNewPane` が
  既存の会話/draft を**重複で開かず**フォーカスする。

**実装メモ（確定事項）**:
- **継承＝作成時スナップショット**（ユーザー確定）: 会話は作成時にアシスタントの
  agent/model/persona/tools/knowledge を自レコードへコピー。以後アシスタントを編集しても
  既存会話は不変（新規会話のみ反映）＝アシスタント削除で会話が孤立しない。
- **tools は none / af_read / af_write の3値**（af_write は下記「書き込みツール opt-in」で実装済）。
- backend `workspace/agent/assistants.go`: `assistant` 型＋ビルトイン3種（コード注入）＝
  ①Agent Fleet アシスタント(削除/編集不可・af_read・USAGE知識を `//go:embed` → `~/.config/agent-fleet/knowledge/af` に materialize＝冪等/再作成自己修復・`--add-dir`)②汎用③翻訳。
  ユーザー定義は `~/.config/agent-fleet/assistants/<id>.json`（full CRUD、builtin id は shadow 不可）。
  `/assistants` 5ハンドラ（builtin は編集/削除 403）。CP は `/api/assistants*` を verbatim プロキシ。
- `chat.go`: `chatConversation` に `assistant_id/persona/tools/knowledge` スナップショット。
  provider は `personaOf()`→`--append-system-prompt`、`knowledgeArgs()`→`--add-dir`、
  `afToolsEnabled()`→MCP。旧 `af_tools` は `afToolsEnabled` で後方互換。`chatMeta` に `assistant_id`。
- Console: `AssistantSection` を「アシスタント（常設＋ユーザー定義）＋会話」に再構成、
  `AssistantModal`（作成/編集）、`chatCreate(assistant_id)`。エージェント選択は `headlessChat` cap 駆動。
- **未実施**: 実機（ブラウザ）目視＝アシスタント作成/選択→新規チャット→ persona/knowledge/tools が
  効くか、ビルトイン AF が USAGE 知識で使い方に答えるか、read-only ツールが実 Agent 越しに動くか。

### 書き込みツール opt-in（**実装済**、docs/19 Q2 の続き）
af_read の会話には書き込みツールが**そもそも見えない**ことを保証する（権限プロンプト頼みにしない）
=「広告するツール集合」をゲートにする設計:
- `mcp_stdio.go`: `runMCPStdio(args)` が `--write` を解釈し `mcpWriteEnabled` を立てる。
  `mcpStdioToolList()` は read ツール＋（write時のみ）`send_to_session` を返す。`mcpStdioCall` は
  `send_to_session` を write 有効時のみ受理（無効時は in-band `isError`）、`agentPOST` で
  `POST /sessions/<name>/input {prompt}`（＝末尾 Enter）を叩く。CP `mcp.go` の member write と等価。
- `chat.go`: `afWriteEnabled()`＝`Tools==af_write`。`chatMCPArgs(write bool)` が write 時に
  MCP サーバ args を `["mcp-stdio","--write"]` にする。`--dangerously-skip-permissions` 下では
  `--allowedTools` は権限的に無意味なので採用せず、**広告ゲート1本**に集約。
- `assistants.go`: `toolsAFWrite` 追加、`validToolGrant` が3値許可。Console `AssistantModal` に
  「AF 書き込み」選択肢（信頼用途のみ許可の注意書き）。
- **スタンドアロン検証済**: `--write` 無=tools/list に send_to_session 出ない/呼ぶと isError、
  `--write` 有=出る/prompt 未指定で検証エラー。**実 Agent 越しの送信はコンテナ内で要目視**。

この`af_read` / `af_write`は**アシスタントチャット面**のgrantである。対話セッションへmcpreg builtin `af`として
materializeする面は別の起動scopeで、docs/51の`af_report`とdocs/53のChromium Attach Viewだけを広告する。
対話セッションへ`af_write`のフリート操作一式を渡す意味ではない。

### アシスタント間連携 ask_assistant（**実装済**）
各アシスタントから他の専門アシスタントに相談できる「アシスタント＝道具」プリミティブ。
用語を割る: **指示出し（実行）＝ワーカー＝tmux セッション（send_to_session）**、
**連携（相談）＝他の専門アシスタントに照会**。確定2点（ユーザー確定）＝①相談(助言)のみ・副作用なし
②af_write に含める（＝af_write は「ワークスペースを動かす権限＝セッションへ指示＋他アシスタントに相談」）。
- **安全性は構造で担保**: 相手ターンを**強制 tools=none** で走らせる→MCP が付かない→相手は
  ask_assistant を持てない→**1ホップで必ず停止・書き込み不可**（深さカウンタ不要で再帰/権限昇格なし）。
  不変条件＝「相談は助言を返すだけ・実行するのは呼び出し元だけ」。
- backend: `chat.go` `handleChatAsk`（`POST /chat/ask`、**内部専用＝CP 非公開**、mcp_stdio→localhost）。
  `resolveAssistant`(id/名前)→persona/model/knowledge を載せた ephemeral(非永続)会話を tools=none で
  1ショット `claudeChat.send`→reply 返す。`mcp_stdio.go` の write ツールに `list_assistants`
  (GET /assistants)＋`ask_assistant`(POST /chat/ask)追加（--write 時のみ）。operator persona に相談手順を追記。
- スタンドアロン検証済（--write 有で計6ツール／無で相談ツール非表示・呼ぶと isError）。

### ツールの向き不向き（重要）: チャット vs セッション
アシスタントチャットは **チャット出力のみ・ファイル書き込み不可・1コンテキスト**（サブエージェントも
禁止＝OOM対策）。→ **短〜中の翻訳/要約/Q&A 向け**。**大きなファイルを翻訳して別ファイルに保存**のような
「ファイル出力を伴う大規模作業」は、ファイルを書けて大規模作業向けの **コーディングセッション** に投げるべき。
このため Files 右クリックには**「セッションに送る…」**を追加（下記）。実インシデント: `~/codex-manual.md`
（マニュアル1本）を翻訳アシスタントに投げ、サブエージェント fan-out で OOM＋ファイル出力失敗。

### Files 右クリック「セッションに送る…」（**実装済**）
ファイル行の右クリック→「セッションに送る…」で、対象ファイルを**パス参照**（`~/<browse相対>`＝home基準の
絶対）で稼働中セッションへ投げる。中身はインライン引用せず、セッションが自分で Read/Write するので大きな
ファイルでも安全。`SendSelectionModal` を「ファイルモード（quote 省略）」に一般化して再利用（送信先=**セッション
のみ**＝アシC添付は「アシスタントで開く」が担当。同レポ/入力待ち優先・前回セッション記憶・停止ガード・送信先
バッジトーストは共通）。翻訳なら「日本語に翻訳して <名前>.ja.md に保存」等をコメントに書いて送る。

### Phase C: Files 右クリック → アシスタント連携（**実装済**）
ファイル/ディレクトリの右クリックから、その対象をアシスタントに渡して新規チャットを開く。
UX の確定事項（ユーザー確定 2 点）:
- **出し分け＝常に1行のサブメニュー「アシスタントで開く ▸」＋対象で中身を出し分け**。dir は常に対象、
  テキストファイルは対象、**バイナリ/画像は項目ごと非表示**（`isProbablyBinary`＋`imageFormat` で判定、
  拡張子デノリスト＝未知拡張子は text 扱いの寛容判定）。クラッタ最小・筋肉記憶が安定。
- **中身＝タスク動詞（翻訳/要約）＋アシスタント一覧、送信はプリフィルのみ（自動送信しない）**。
  誤クリックでターンを消費せず、中身を見て編集できる。
実装:
- backend `chat.go`: `chatCreate` に `attach_path`（browse-root 相対）＋`seed_verb` を追加。
  `safeBrowsePath` で解決＋denylist、対象の**dir を knowledge に追記**（file はその親 dir）、
  seed プロンプトを**絶対パス入りでサーバ側composed**（`seedFor`）。返り値に transient `seed`
  （保存後にセット＝永続化されない）。knowledge スナップショットに ad-hoc dir が乗る＝`--add-dir` で読める。
- frontend: `lib/chatSeed.ts`（会話id→seed のワンショット受け渡し）、`openChat(id, seed?)` が stash、
  `ChatView` が load 時に `takeChatSeed` して composer にプリフィル。`FilesSection` の ctxmenu に
  アコーディオン式サブメニュー（flyout はタッチで壊れるので click 展開）。動詞 翻訳→translate/要約→general
  ビルトインに割当、一覧は全アシスタント。`chatCreate(assistantId, title, {attachPath, seedVerb})`。
- **未実施の目視**: 右クリック→アシスタント選択で、attach した file/dir を実際に Read して
  翻訳/要約/回答できるか（--add-dir 経由の絶対パス Read が効くか）。

### 回答言語の指定（**実装済**）
persona は当面**日本語のまま**でよい。指示プロンプトを英語化しても現行 Claude では指示追従の精度は
上がらず、persona はユーザーが UI で編集する項目なので日本語のままが編集体験上も自然。ただし従来は
「出力言語＝persona を日本語で書いていること」による**暗黙**の誘導だったのを、**指示言語と出力言語を分離**する。

- **ユーザーごとの回答言語設定**を `ui-prefs.json`（`~/.config/agent-fleet/ui-prefs.json`、既存の per-user
  設定 blob）に `outputLanguage` キーとして持つ。値は `"auto"（既定・入力に合わせる）| "ja" | "en"` の 3 値のみ。
  完全な locale マトリクスは JA-first のこの製品には過剰なので採らない（YAGNI）。
- Agent 側 `chatOutputLanguage()`（`ui_prefs.go`）が同 blob を**送信ごとに live 読み**し、`personaOf()`
  （`chat.go`）が `"ja"→langRuleJA / "en"→langRuleEN` を system prompt 末尾に追記。ルール文は**各対象言語で**
  書き（英語 persona の後ろに日本語ルールを付けると誘導が濁るため）、`"auto"` は**何も注入しない**＝入力/persona
  任せ（≒ Claude の入力言語ミラーで従来挙動を維持）。
- **送信時解決＝スナップショットしない**。persona/tools/knowledge は会話作成時にスナップショットするが、
  言語は横断的な個人設定なので `personaOf()` 実行時に ui-prefs から解決する。→ 設定変更は既存会話にも次ターンで反映。
- **翻訳アシスタント（`assistant_id == "translate"`）は対象外**。自動 JA↔EN 判定を強制言語が壊すため
  `languageRule()` で除外。
- Console: `settings.ts` の `Settings.outputLanguage`（既定 `"auto"`）＋`OUTPUT_LANGUAGES`、`AgentsTab`
  「セッション」グループに選択 UI。ui-prefs は Settings 全体を verbatim で PUT/GET するため、キー追加だけで同期に載る。

### 実装順
Q3（済）→ Q1（mcp-stdio, read-only, 済）→ **Q2＝アシスタント・テンプレート（済）**
→ **書き込みツール opt-in（済）** → **Phase C＝Files 右クリック連携（済、DocView/FileView ボタンは残）**。

## 制約（Console 側、docs/18 と同じ）

no-store 配信 / URL は `document.baseURI` 相対 / `X-AF-Tenant` 注入 / xterm 内部は不可侵。
ヘッドレス CLI は既存のコンテナ内認証をそのまま継承するため**新しい資格情報・接続 UI は不要**。

## 追記（2026-07-10）: フリート・オペレーターに新規セッション作成を追加

司令塔（af_write）に「新しいセッションを起こす」能力を足した。用途は ①あるセッションの内容を
読み、要点を要約して次の新規セッションへ引き継ぐ ②チャットで壁打ちしたあと新規セッションでタスクを
開始する、の 2 つ。**会話まるごと複製（`--fork-session`）ではなく要約引き継ぎ**（オペレーターが
`get_session_output` で文脈を読み、絞った要約を `initial_prompt` に渡す）を採る。

- **Agent REST**: `POST /sessions` に任意フィールド `initial_prompt` を追加。作成後 `deliverInitialPrompt`
  を goroutine で起動し、CLI 起動（tmux + pane 解決）→合成 2.5s 待ち→ペインへ type + Enter で最初の
  タスクを投入する（Console の client 側 `sendPromptWhenAlive`（open.ts）の**サーバ側版**。live な
  Console ミラー無しでも 1 コールで作成＋初期タスク投入が成立）。best-effort・失敗しても作成済みセッションは
  残す。type-then-Enter プリミティブ `sendSlashCommand` は `typeLineAndSubmit` に改称して共用。
- **ローカル stdio MCP（オペレーター）**: write に `create_session`（`dir`/`title`/`kind`/`model`/
  `initial_prompt` → `POST /sessions` 中継）、read に `list_repos`（`GET /repos` 中継。起こす dir を
  走っていないリポジトリも含めて選べるように）を追加。
- **CP 側 MCP（PAT）**: `memberTools` に同じ `create_session`（scopeWrite）/ `list_repos`（scopeRead）を
  Agent REST 中継で追加。write PAT を持つ外部/管理オーケストレーターからも新規セッションを起こせる。
- **ペルソナ**: `operatorPersona` に引き継ぎ/壁打ち起動の手順（元の `get_session_output`→要約→
  `create_session` の `initial_prompt`）と、**リソース消費のため作成前に『どこで・何を』を利用者へ確認**する
  ガードを追記。オペレーターは write MCP を `--dangerously-skip-permissions` で走らせるため、ゲートは
  「公開ツール集合＋ペルソナの確認指示」であってパーミッションプロンプトではない（既存方針を踏襲）。
- **worktree 分離は今回スコープ外**（プレーン dir のみ）。将来 `worktree:true`＋自動 `temp/<slug>` を
  `handleCreateSession` が既にゼロ設定で持つので、低摩擦で足せる。
