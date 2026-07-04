# 19. アシスタント・チャット（ヘッドレスCLI方式）

Agent Fleet 利用者の作業を補助する **LLM チャット/翻訳アシスタント**機能。
Markdown ドキュメントの翻訳を投げる、チャットセッションを開始する、といった用途を想定。
既存の tmux コーディングエージェント（claude/codex/opencode/shell/ssm）とは**別物**として追加する。

> ステータス: 設計確定 + Phase A 実装中（`feat/assistant-chat`）。

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
- **Phase C**: 用途アクション（Files/DocView から「翻訳」）、プロンプトテンプレ。
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

### Q3: セッションと .claude を分ける → **専用 config-dir（実装済）**
共有のままだと (1) セッション状態フック汚染 (2) MCP 設定の対話セッションへの漏れ (3) 会話 jsonl の混在。
→ チャット専用 `CLAUDE_CONFIG_DIR`（`~/.config/agent-fleet/chat-claude`、`AF_CHAT_CLAUDE_DIR` で上書き可）を
`cmd.Env` で与え、**`.credentials.json` のみ共有 dir へシンボリックリンク**（サブスク認証は共有＝再ログイン不要）。
`chat.go` の `ensureChatClaudeConfig`/`reconcileChatCreds`/`envWith`/`chatClaudeCmd`。
creds は単一共有が必須（OAuth refresh はリフレッシュトークンをローテートするため、二重コピーは片方が失効）。
**strace で実測確定**: claude は JSON 状態（creds 含む）を **tmp＋rename 置換（アトミック）** で書く
（`.claude.json.tmp.* → rename(.claude.json)`、inode が変わる）。帰結:
- 対話セッション/Agent 再認証 = **共有ファイル**を rename → symlink はパス解決なので追従（最新を取得）、対処不要。
- チャット自身の refresh = **リンクのパス**を rename → symlink が実ファイル化して共有と乖離。
  → `reconcileChatCreds` が「新しいトークンを shared へ戻して再リンク」。実行**前後**両方で走らせ、1ターン以内に共有へ反映。
- ファイル bind-mount は不可（source の rename でマウントが陳腐化、マウント点への rename は EBUSY）→ **symlink＋copy-back が最適解**。

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

### Q2 → 「アシスタント・テンプレート」機能（常設 AF + ユーザー定義、方針決定・未実装）
単一ペルソナでなく、**アシスタントを設定可能なエンティティ**にする（カスタム GPT 的）:
- **Assistant** = {id, name, icon?, builtin, agent(claude/codex), model?, persona(system prompt),
  tools(af_read / af_write / none), knowledge?(USAGE 等 doc を --add-dir で読ませる)}。
- **常設ビルトイン**: 「Agent Fleet アシスタント」(削除不可、persona=使い方案内, tools=af_read,
  knowledge=利用者向け USAGE)。汎用/翻訳ビルトインも可。
- **ユーザー定義**: 名前＋persona＋model＋ツール許可を作成/編集（`~/.config/agent-fleet/assistants/<id>.json`、
  ビルトインはコードで注入してマージ）。**書き込みツール(af_write=send_to_session 等)は各アシスタントで
  ユーザーが明示 opt-in** した時だけ `mcp_stdio` に公開/`--allowedTools` 許可。
- **会話**は `assistant_id` を持ち、作成時にアシスタントの agent/model/persona/tools を継承。
  `chat.go` provider が persona→`--append-system-prompt`、model→`--model`、knowledge→`--add-dir`、
  tools→`AFTools`＋（将来）書き込みツール公開を出し分け。
- **Console**: AssistantSection を「アシスタント（常設 AF ＋ ユーザー定義）＋各会話」に再構成、
  「＋新規チャット（アシスタント選択）」、アシスタントの作成/編集 UI（名前/persona/model/ツール許可）。
- 利用者向け USAGE/FAQ は別途用意（内部 docs/ は出さない）。

### 実装順
Q3（済）→ Q1（mcp-stdio, read-only, 済）→ **Q2=アシスタント・テンプレート**（データモデル拡張 + Console UI）
→ 書き込みツール opt-in → Phase C（Files/DocView から翻訳）。

## 制約（Console 側、docs/18 と同じ）

no-store 配信 / URL は `document.baseURI` 相対 / `X-AF-Tenant` 注入 / xterm 内部は不可侵。
ヘッドレス CLI は既存のコンテナ内認証をそのまま継承するため**新しい資格情報・接続 UI は不要**。
