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

## 制約（Console 側、docs/18 と同じ）

no-store 配信 / URL は `document.baseURI` 相対 / `X-AF-Tenant` 注入 / xterm 内部は不可侵。
ヘッドレス CLI は既存のコンテナ内認証をそのまま継承するため**新しい資格情報・接続 UI は不要**。
