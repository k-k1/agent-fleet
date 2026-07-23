# 40. `kind=cursor`（Cursor CLI）実装計画 — Terminal + Managed 両対応

- 状態: **計画・Track 0 プローブ実施済み**（2026-07-23 事前調査＋認証済み実測完了・実装未着手）。
  採用判断は [decisions/0023](decisions/0023-cursor-agent-kind.md)。
  実 CLI の実測は本ドキュメント末尾 §実測記録・§Track 0 実測結果（v2026.07.20-8cc9c0b を本コンテナで実測）。
  **managed 可否の分水嶺（ACP `session/load`）は合格 — v1 Terminal + Managed 両対応で確定。**
  ※ docs/39 / ADR0022 はエージェントメモリ版管理（未マージブランチ）が使用中のため 40/0023 を採番。
- ゴール: `cursor`（Cursor CLI, `cursor-agent` / `agent`）を第7のエージェント種別として組み込む。
  **copilot（docs/36）と同じく v1 から Managed driver ＋ Terminal (CLI) の両対応**を狙う。
- 根拠: Cursor CLI は `agent acp`（ACP = JSON-RPC over stdio, 公式ドキュメント有）・
  `-p --output-format json|stream-json`（Claude Code 類似のイベントストリーム）・
  `agent create-chat`（chat ID 事前採番）・`--resume <chatId>` クロスディレクトリ resume・
  **hooks（`hooks.json`、入力に `transcript_path`）**・Claude Code 互換 JSONL 転写を備え、
  copilot に匹敵するオーケストレーター向きの口が揃っている（CLI フラグ・hooks イベント名は
  実バイナリで確認済み、ACP/JSONL の挙動詳細は §プローブ一覧 で実測してから着工する）。
- 教訓の反映: docs/32（agy）・docs/36（copilot）の指摘を §教訓反映表 で個別対応。

## 表示順（ユーザー指定・最初に確定）

起動 UI・設定カードの並びは **Claude, Codex, Cursor, GitHub Copilot, Antigravity, OpenCode**。
`cursor` を codex と copilot の間に挿入する。編集箇所は 3 つ（他はここから派生）:

| 箇所 | 変更 |
|---|---|
| `console/src/types/session.ts` `SESSION_KINDS` | `["claude","codex","cursor","copilot","agy","opencode","shell","ssm"]` |
| `console/src/agents/registry.ts` `repoLaunchKinds` | `["claude","codex","cursor","copilot","agy","opencode","shell"]` |
| `console/src/features/settings/AgentsTab.tsx` カード順 | CodexCard と CopilotCard の間に CursorCard |

## 先に固定する契約

**命名・色・アイコン**（bfa80ae 教訓: 最初に確定）:

| 項目 | 決定 |
|---|---|
| kind | `cursor`（`session.go` `KindCursor`） |
| 3段命名 | label=`Cursor` / displayName=`Cursor` / assistantName=`Cursor` / short=`cu` |
| 色 | `--kind-cursor`: ローズ/マゼンタ系（dark `#d96ba1` / light `#b0316e` 起点で実装時に微調整）。ブランドは白黒モノクロでテーマ背景・opencode グレーと衝突するため不採用。既存 7 色（橙/緑/青/紫/灰/シアン/藍）と重ならない唯一の暖色系空き色相 |
| i18n | `agent.launch_hint.cursor`（ja/en）＋ `i18n:lint` 通過 |

**認証は専用フロー型**（claude/agy 型。copilot の GitHub 相乗りとは異なる）:

| 項目 | 決定 |
|---|---|
| 対話ログイン | `NO_OPEN_BROWSER=1 agent login` が URL を標準出力に出す（実測）→ claude/agy と同じ start/complete 連携フロー（URL 抽出→Console 表示→ユーザーがブラウザで承認→完了検知）。OSC-8 汚染（agy 26c875f）に注意して URL 抽出 |
| API キー | `CURSOR_API_KEY` / `--api-key`（実測）→ codex 型の手動キー登録経路も併設（Dashboard 発行キー） |
| 状態判定 | `agent status --format json`（実測: `{status:"authenticated", isAuthenticated, hasAccessToken, hasRefreshToken, userInfo:{email,userId,...}}` のクリーンな JSON） |
| 資格情報の保存先 | **`~/.config/cursor/auth.json`**（実測・mode 600・`accessToken`/`refreshToken` の平文 JSON。**`~/.cursor` ではない**）→ `fs.go` denylist は **`.config/cursor` と `.cursor`** の両方（copilot `.copilot` と同じ平文トークン対策）。ホームボリュームに載るため recreate を跨いで持続 |
| バックエンド | `api2.cursor.sh`（プロバイダ直結ではない）。`-e/--endpoint`・`-H/--header`・`NODE_USE_ENV_PROXY=1` があるが v1 では触らない |

**launch 契約**（TUI・managed 共通のセッション同一性）:

- **chat ID は `agent create-chat` で事前採番**（実測でコマンド存在確認）→ sid-store に保存 →
  resume は TUI `--resume=<chatId>` / managed は ACP の load 系（要プローブ）。
  copilot `--session-id` と同じ「外部採番で resume ID 捕獲問題を構造的に回避」の路線。
- 権限は fleet 既定の bypass 相当: `--force`（=`--yolo`。**deny リストは引き続き有効**な設計 — 実測 help）。
  plan 起動は `--mode=plan`。ask モードは v1 非露出。
- ヘッドレス経路の workspace trust は `--trust`。TUI 初回の trust プロンプト有無は要プローブ
  （出るなら copilot 同様に設定ファイル事前追記 or `--trust` 相当で毎回スキップ、agy 3a2c9df 教訓で起動毎に再固定）。
- sandbox は既定 disabled（`cli-config.json` 実測）。v1 はそのまま（fleet コンテナ自体が隔離境界）。
- **Cursor 自前の worktree 機能（`-w/--worktree`）は使わない** — セッション隔離は Console の
  worktree が正（`~/.cursor/worktrees/` に勝手に増えるのを避ける）。
- 自己更新封殺: 既定で auto-update ON・**公式な無効化手段なし**（Track 0 で env/config を
  探索したが AUTO_UPDATE 系は存在せず — `CURSOR_AGENT_*` 環境変数群に該当なし）。
  AUR 方式 = versions ディレクトリの書込禁止（chmod）で封殺し、e2e-smoke の版ピン検証で監視。
  自己更新 opt-in（`AF_AGENT_SELF_UPDATE`）への追加は rtk/agy と同じ「~/.local/bin shadow」系の
  設計になる見込み（npm ではないため claude/codex 経路は使えない）。
- モデル: `--model <id>`（`claude-opus-4-8[context=1m,effort=high]` 形式のパラメータ付きあり — 実測 help）。

**read 正本（transcript/状態）— Track 0 実測で経路別に確定**:

- 真の正本は `~/.cursor/chats/<workspace-hash>/<chatId>/store.db`（SQLite blob・**非公開形式**）。
  **これは読まない**（opencode ストア契約変更で false-idle を踏んだ教訓 — 非保証内部への依存禁止）。
- **経路によって出るものが違う**（実測 — copilot の「全経路同一 events.jsonl」とは異なる）:

| 経路 | hooks 発火 | JSONL 転写 | 状態/転写のソース |
|---|---|---|---|
| TUI | **beforeSubmitPrompt / beforeShellExecution / stop 全発火** | 書く | hooks（一次）＋JSONL |
| `-p` | beforeShellExecution のみ（submit/stop 出ない） | 書く | プロセス終了＝ターン終了 |
| `agent acp` | **発火しない** | **書かない**（ローカル痕跡ゼロ・履歴はサーバ側） | ACP updates/`session/load` リプレイから driver が構築 |

- **TUI**: claude と同型の working マーカー（beforeSubmitPrompt=turn 開始、stop=turn 終了）を
  `~/.cursor/hooks.json`（起動前に AF が配線・起動毎再固定）から status ファイルに書く →
  `LiveState` の一次ソース。報告消失の教訓（1f64c57）を踏まえ同じ seam に乗せる。
  転写は hooks payload の `transcript_path` が指す
  `~/.cursor/projects/<cwdスラグ>/agent-transcripts/<chatId>/<chatId>.jsonl`。
  形式は `{"role","message":{"content":[...]}}`（Anthropic content block 型・`tool_use` あり）＋
  `{"type":"turn_ended","status"}`。**tool_result は転写に載らない**（ツール出力は store.db のみ）
  → ミラーはツール名/引数まで、出力はプレースホルダ表示。claude パーサ流用は不可（uuid/timestamp 無し）
  だが専用パーサは簡単。resume で同一ファイルに追記（実測）。
- **managed（ACP）**: JSONL/hooks が無いため transcript は driver が `session/update`
  通知（`agent_message_chunk`/`agent_thought_chunk`/`tool_call`）から構築し、
  `session/load` の全量リプレイ（user_message_chunk から再生 — 実測）で復元。
  状態は runTurn 境界（codex/copilot と同じ）。
- TUI 文字列（スピナー/フッタ）には依存しない（false-idle 第3〜6次の教訓。週次リリースでドリフト前提）。
  paneMode 分岐（8780956 教訓）用のフッタは §Track 0 実測結果 に採取済み。

**managed 契約（ADR0015 / driver.go 準拠・Track 0 プローブで主要項目を確定）**:

| 項目 | 決定 | 根拠 |
|---|---|---|
| Runtime | **per-session child**: `agent acp` stdio JSON-RPC（copilot 型・NDJSON） | 実測: initialize→session/new→session/prompt→`stopReason:"end_turn"` 一巡成功 |
| 新規/再開 | `session/new`（sessionId が返る）→ sid-store。**`session/load` でクロスプロセス resume 成功**（`loadSession:true` 宣言・履歴全量リプレイ・文脈保持を実測 — 別プロセスから前ターンのトークンを正答） | 実測 probe |
| ACP セッションの性質 | **ローカル痕跡ゼロ**（chats/ にも転写にも出ない・履歴はサーバ側保持）。`-p --resume <acp-sid>` は文脈を復元しない（実測）→ **resume は session/load 一択**。TUI⇄managed の相互乗り入れはしない | 実測 |
| streaming | `session/update` 通知: `agent_message_chunk` / `agent_thought_chunk` / `session_info_update` / `available_commands_update`（実測）＋ `tool_call`（公式 docs） | 実測 probe |
| capabilities | `promptCapabilities.image:true`（画像添付可）・`sessionCapabilities.list`・`mcpCapabilities.http/sse`・modes は `agent`/`plan`（session/new 応答で列挙 — 実測） | 実測 |
| Interrupt | `session/cancel`（公式 docs・copilot 同型。実装時に実測） | docs |
| 質問/許可 | `session/request_permission`（allow-once/allow-always/reject-once — 公式 docs）→ Interaction(question)。`cursor/ask_question`（blocking 拡張）も question に写像。防御実装（agy 1af1be9 教訓） | docs |
| Steer/Fork | ネイティブ無し見込み → driver 内キュー / `Fork:false`（TUI には `/fork` あり） | 実装時確認 |
| Mode/Model | mode は session/new/load 応答に `modes` があり切替口あり → `DynamicMode` は実装時に set 系メソッドを確認。model は ACP で per-session 指定が見当たらず → copilot 同様 **子プロセス毎 `--model` フラグ**・`DynamicModel:false` | 実測＋docs |
| 完了報告 | runTurn 境界で `MarkTurnStart/End`（notify seam、5facc6e/dffd84c 教訓） | — |
| TUIAttach | `false`（codex/copilot 型の排他切替） | — |
| 認証 | 子プロセスは `~/.config/cursor/auth.json` の ambient ログインで動く（実測: env 注入なしで完走）。`--api-key` 前置も可 | 実測 |

## トラック分割

### Track 0 — 着工前プローブ — **実施済（2026-07-23・認証済み実測）**

結果は §プローブ一覧（結果） と §Track 0 実測結果。要点: managed 可否（ACP load）**合格**、
JSONL 転写パス・形式確定（claude パーサ流用は不可だが専用パーサ容易）、hooks は TUI 経路で
全発火（-p は shell のみ・ACP は不発火）、資格情報は `~/.config/cursor/auth.json`、
auto-update 無効化の公式手段は**未発見**（versions 書込禁止 fallback で確定）。
tmux 検証は `-L cursor-probe` 専用ソケット隔離を遵守（84139d2 教訓）。

### Track A — workspace agent 本体（read 層 + TUI）

1. `workspace/agent/internal/agents/cursor/` 新設（テンプレ: copilot）:
   - `cursor.go` — `agentImpl`（Kind/Caps/BuildLaunch/WireLive/ClearResume/Transcript）
   - `program.go` — buildProgram（上記 launch 契約。`AGENT_CURSOR_CMD`/`AGENT_CURSOR_FLAGS` override 慣習）
   - `auth.go` — `Status()`（`agent status --format json` or 資格情報ファイル）＋ login start/complete フロー
   - `hooks.go` — `~/.cursor/hooks.json` へ AF 状態フック（stop/beforeSubmitPrompt）を起動前に配線
     （起動毎に再固定、3a2c9df 教訓。ユーザー既存 hooks.json とのマージ規則を決める）
   - `sids.go` — sid-store（slotSid → cursor chatId。create-chat 事前採番）
   - `transcript.go` — JSONL 転写パーサ（Turn.Idx 単調採番 — 30c5e21 教訓・pending 検知・ツール正規化）
   - `state.go` — `LiveState`（hooks 由来 status ファイル一次＋JSONL 末尾で補強）
   - `models.go` — `agent models` によるアカウント連動ライブカタログ（10 分キャッシュ・stale-if-error、
     copilot models.go と同じ agents.Flow 基盤。**スクレイプ不要でコマンドが公式にある**分 copilot より楽な見込み）
2. 登録: `internal/session/session.go` `KindCursor`、`agent.go` agentRegistry、
   `connections.go` Status 集約、`agent_models.go` switch、`fs.go` denylist **`.cursor`**
3. paneMode（`session_io.go`）: フッタ実測パターンで分岐（8780956 教訓・Track 0 で採取）。
   tmux 内の改行は Ctrl+J（公式 docs — Shift+Enter 非対応）→ 投入経路 5 つ
   （launch-seed / send_to_session / /turn / 保留中 / paste）を監査（54e1fec 教訓）
4. GracefulStop: TUI の終了コマンド実測（Ctrl+D 二度押し — 実測 help/docs）→ `stop.go`

### Track A2 — managed driver（probe 合格が前提）

1. `driver.go` — `managedDriver{agentImpl}` + `threadHandle`（copilot driver.go 踏襲）
2. `serve.go` — per-session child supervisor（`agent acp` stdio）。`cmd.Wait()` →
   `status.PersistExit`（OOM 帰属）→ runtimeLost → 次 Resume で再 spawn+load
3. 登録: `session_turn.go` managedDrivers、`session_handlers.go` の managed 系 switch 全部、
   `main.go` `ReconcileManaged`、`shutdown.go`、`control-plane/scheduler_wake.go` の managed switch
4. 実 CLI 契約テスト（`AF_CURSOR_LIVE=1` opt-in、copilot live_contract_test.go 踏襲）

### Track B — 配備

1. `workspace/Dockerfile` — `CURSOR_CLI_VERSION` ARG ＋版付き URL
   `downloads.cursor.com/lab/<版>/linux/<arch>/agent-cli-package.tar.gz` から焼き込み
   （**非公開 URL 仕様** — e2e-smoke の版ピン検証を必ず付けてドリフトを一次検知）。
   auto-update 無効化（Track 0 の結果で手段確定）。
2. `env_tool_versions.go` toolSpecs 追加＋`versions.json` `"cursor"`。
   **版数は日付形式（`2026.07.20-8cc9c0b`）で semver でない** — 版比較ロジックに注意
   （rtk-always-baked の版比較スキップ実装と同類の扱い）。
3. 自己更新 opt-in（`AF_AGENT_SELF_UPDATE`）へ cursor 追加（`~/.local/bin` shadow 系）。
4. boot-install fallback（`BAKE_AGENT_CLIS=false` 経路）対応。
5. **linux arm64 は要実機確認**（Raspberry Pi 5 で動かない報告あり — ECS/native 展開前提条件）。

### Track C — control-plane + Console

1. CP: `/api/connections` 集約＋cursor login start/complete ルート（**routes.go 二重登録** —
   `workspace/agent/routes.go` と `control-plane/routes.go` の両方。cp-rest-proxy-allowlist 教訓）。
   `GET /api/agents/{kind}/models` はパターンルートで自動対応。
2. MCP kind enum: `mcp_stdio.go`（create_session/list_models/検証 whitelist）＋
   `control-plane/mcp.go`（kind 記述・usage ツール）の**両方**（194695f 教訓）。
3. Console:
   - `types/session.ts` — union / SESSION_KINDS（表示順どおり挿入）/ ConnectionsStatus.cursor
   - `agents/registry.ts` — descriptor（caps 全項目を明示決定 — 1854dc2/fe84b06 教訓、
     `managedDriver` は Track A2 結果で、`tuiMemoryCost`、launchHintKey、planCycleKey 等）
   - `tokens.css` `--kind-cursor`（**dark/light 両テーマ**）＋ `app.css`/`ui.css` の色クラス
   - `AgentsTab.tsx` — CursorCard（ログインフロー＋API キー欄。CodexCard の構成に近い。
     ClaudeCard と同じ autofill ガード）
   - i18n ja/en ＋ `i18n:lint`
   - `questionKeys.ts` — managed 質問は /respond 経路。TUI 保留カードは probe 結果次第
4. availability: `connsDone` ゲート（443fc8a 教訓）、選別は `caps.runsInDir`（f8a7bbe 教訓）。
5. bridge: `internal/bridge/format.go` kindLabel に case 追加（registry.ts と同期コメント維持）。

### Track D — 残課題・将来

1. **WS バー使用量チップは v1 スキップ**: プラン残量の公式 API/コマンドが無い
   （CLI から見えるのは context 使用量のみ — フォーラム確認）。非公式 API
   （`cursor.com/api/usage`・`api2.cursor.sh GetCurrentPeriodUsage`）は usage-chip 429 事件と
   同じ脆さのため不採用。stream-json の per-turn トークンでセッション単位表示は将来可。
2. rtk: base-URL 差し替え不可（プロバイダ直結でない）→ **hooks seam**（`rtk hook cursor` を
   rtk 側に新設し `beforeShellExecution` に配線）。cursor hooks がコマンド書換
   （copilot `modifiedArgs` 相当）を許すかは要プローブ — 許さなければ指示ベース（codex/agy 同格）。
3. アシスタントチャット headless バックエンド（`-p --output-format json`。chat_providers.go）。
4. 画像添付・`/fork`・`/summarize`（コンテキスト圧縮）・Cloud Agent（`&` プレフィックス）連携。
5. Claude Code 会話インポート（`import_cc_conversation` フラグをバンドル内に確認 — 将来の移行導線）。

## 教訓反映表（agy docs/32・copilot docs/36 → cursor での対応）

| 過去の指摘 | cursor での対応 |
|---|---|
| resume ID 取得不能（agy d24c2f0） | `agent create-chat` 事前採番で構造的に回避（copilot `--session-id` と同型） |
| auth URL の OSC-8 汚染（agy 26c875f） | login start/complete フローの URL 抽出で同対策を最初から適用 |
| tmux kill-server 全滅（agy 84139d2） | probe/E2E は `-L cursor-probe` 専用ソケット隔離 |
| Turn.Idx 未採番（30c5e21） | transcript.go 単調採番＋単調増加テスト |
| paneMode 分岐漏れ（8780956） | Track 0 でフッタ実測 → 実装。改行 Ctrl+J の投入経路監査（54e1fec）込み |
| MCP ツール欠落（194695f） | mcp_stdio.go / CP mcp.go 総ざらいを Track C 明記 |
| turn 終端未検出→報告不発（5facc6e/1f64c57） | hooks stop を status seam に乗せ、managed は MarkTurnStart/End |
| caps 明示漏れ（1854dc2/fe84b06） | descriptor caps 全項目を表で決定してから実装 |
| 権限「発生しない前提」崩壊（agy 1af1be9） | request_permission / ask_question を防御実装 |
| conns ローディング中の誤露出（443fc8a） | connsDone ゲート |
| 設定の一回きり固定（agy 3a2c9df） | hooks.json / trust / auto-update 封殺は起動毎に再固定 |
| ストア内部依存の false-idle（opencode 教訓） | store.db（SQLite blob）は読まない。hooks＋JSONL の公式契約のみ |
| CP allowlist 漏れ（cp-rest-proxy-allowlist） | 新設ルートは両 routes.go 同時登録をレビュー項目に |
| 非公式 API の突然死（usage-chip 429） | 使用量チップ v1 不採用（Track D） |
| インストーラ既存あり no-op（agy 41b1c83） | tarball 直展開のため非該当だが、焼き込み検証を e2e-smoke に追加 |

## 実測記録（2026-07-23、v2026.07.20-8cc9c0b linux-x64、本コンテナ）

- インストール: `curl https://cursor.com/install | bash` →
  `~/.local/share/cursor-agent/versions/2026.07.20-8cc9c0b/` ＋ `~/.local/bin/{agent,cursor-agent}` symlink。
  DL URL は版付き `https://downloads.cursor.com/lab/2026.07.20-8cc9c0b/linux/x64/agent-cli-package.tar.gz`。
- 実体: Node.js バンドル（node バイナリ・`node_sqlite3.node`・`pty.node`・ripgrep・
  `cursorsandbox`・`crepectl` 同梱、webpack chunk 群）。`cursor-agent` は node 起動の薄いラッパ。
- `--help` 実測: `-p/--print`・`--output-format text|json|stream-json`・`--stream-partial-output`・
  `--resume [chatId]`・`--continue`・`--model`（`claude-opus-4-8[context=1m,effort=high,fast=false]`
  形式のブラケットパラメータ）・`--list-models`・`-f/--force`＝`--yolo`（"unless explicitly denied"）・
  `--auto-review`・`--sandbox enabled|disabled`・`--approve-mcps`・`--trust`・`--workspace`・
  `--add-dir`・`--plugin-dir`・`-w/--worktree`・`-e/--endpoint`（既定 `https://api2.cursor.sh`）・
  `-H/--header`。サブコマンド: `login`（NO_OPEN_BROWSER 対応）/`logout`/`status|whoami`/`models`/
  `mcp`/`plugin`/`worker`/`about`/`install-shell-integration`。
- 未認証時: `agent status` → "Not logged in"。`--list-models` →
  `Error: Authentication required. Run 'agent login', pass --api-key/--auth-token, or set CURSOR_API_KEY/CURSOR_AUTH_TOKEN.`
- `~/.cursor/cli-config.json`（初回起動で生成・実測）: `permissions.allow/deny`（`Shell(ls)` 形式）、
  `approvalMode: "allowlist"`、`sandbox.mode: "disabled"`、`editor.vimMode`、
  `network.useHttp1ForAgent`、`attribution.attributeCommitsToAgent` 等。
- バンドル内文字列（grep 実測）: hooks イベント `beforeShellExecution`/`afterShellExecution`/
  `beforeSubmitPrompt`/`stop`/`beforeMCPExecution`/`afterFileEdit`/`beforeReadFile`＋`hooks.json` 参照。
  `~/.cursor/` 配下パス: `chats`/`rules`/`commands`/`mcp`/`skills`/`worktrees`/`projects`/`agents`/
  `cli`/`sandbox`/`settings`。feature flag に `hooks_stdin_transport`・`import_cc_conversation`。

## 外部調査の要点（2026-07-23、公式 docs ほか）

- ヘッドレス/出力形式: stream-json は NDJSON で `system/init`（session_id/model/permissionMode）→
  `user`/`assistant`/`tool_call`（started/completed・型付き payload）→ 終端 `result`
  （duration_ms/is_error/session_id）。**異常時は整形 JSON なしで終わり得る**（終端イベント欠落あり得る）
  — パーサは防御的に。cursor.com/docs/cli/reference/output-format
- hooks: 公式 docs（cursor.com/docs/hooks）。project `<root>/.cursor/hooks.json` / user `~/.cursor/hooks.json`。
  stdin JSON に `conversation_id`・`generation_id`・`hook_event_name`・`transcript_path`・`workspace_roots`。
  exit 0=許可 / 2=ブロック / 他=fail-open。イベントは sessionStart/End・preToolUse/postToolUse・
  beforeShellExecution 等（実測と一致）。
- ACP: 公式 docs（cursor.com/docs/cli/acp）。`agent acp`、JSON-RPC 2.0 / NDJSON / stdio。
  permission request（allow-once/allow-always/reject-once）、拡張 `cursor/ask_question`（blocking）・
  `cursor/update_todos`、モデル/モード選択、thinking ブロック streaming。`--api-key` 前置可。
- 転写: changelog に「persistent transcripts」「**Claude Code-compatible JSONL transcripts**」。
  セッション実体は `~/.cursor/chats/<ws-hash>/<chatId>/store.db`（SQLite: meta 1行＋blobs、
  **非公開・予告なく変更あり得る** — coder/registry #747、vibe-replay 解析）。
  IDE と CLI のセッションストアは非互換（forum #165486）。
- TUI: tmux 明示対応（改行 Ctrl+J）。Shift+Tab でモード循環、Ctrl+R レビュー、Ctrl+D×2 終了。
  スラッシュ: `/model`・`/resume`・`/rewind`・`/fork`・`/summarize`・`/sandbox`・`/copy-conversation-id` 等。
- 版管理: 週次リリース・日付版数。公式ピン手段なし（AUR は版付き URL＋versions chmod -x で対処）。
  auto-update 既定 ON・`agent update` 手動更新あり。
- 既知の問題: arm64（RPi5）起動不能報告 forum #148408、Docker 内 CURSOR_API_KEY 不調 forum #143995、
  転写ファイル空化 forum #158251、`agent ls` タイトル未設定 forum #143731。
- 料金: サブスク（Pro $20/Pro+ $60/Ultra $200）＋API 従量。プラン残量の公式 CLI/API なし（forum #154101）。
- AGENTS.md はプロジェクト root で読まれる（Docker sandbox docs で確認）。`.cursor/rules/` も可。

## プローブ一覧（結果 — 2026-07-23 実施）

| # | 項目 | 結果 |
|---|---|---|
| 1 | ACP 一巡＋load resume | ✅ **合格**: initialize（`loadSession:true`）→ new → prompt（chunk streaming）→ `end_turn`。**別プロセス `session/load` で履歴リプレイ＋文脈保持を実証**。cancel は docs のみ（実装時に実測） |
| 2 | JSONL 転写 | ✅ `~/.cursor/projects/<cwdスラグ>/agent-transcripts/<chatId>/<chatId>.jsonl`。Anthropic content block 型（`tool_use` あり・**tool_result 無し**・uuid/timestamp 無し）＋`turn_ended`。TUI/-p は書く・**ACP は書かない** |
| 3 | hooks 実発火 | ✅ TUI: beforeSubmitPrompt/beforeShellExecution/stop 全発火。`-p`: beforeShellExecution のみ。ACP: 不発火。payload に conversation_id/`transcript_path`/cursor_version/user_email。コマンド書換の可否は未検証（rtk 実装時） |
| 4 | 資格情報 | ✅ `~/.config/cursor/auth.json`（600・accessToken/refreshToken 平文 JSON）。ホームボリュームで持続。`status --format json` はクリーンな構造化 JSON |
| 5 | auto-update 封殺 | ◐ 公式手段なし（AUTO_UPDATE 系 env/config 不存在を確認）→ versions ディレクトリ書込禁止 fallback で確定 |
| 6 | `agent models` | ✅ `id - 表示名` のテキスト行（`--format json` は無い）。アカウント連動（auto/composer/claude/gpt/grok 系を確認）。要認証 |
| 7 | TUI 実測 | ✅ §Track 0 実測結果（trust プロンプト・フッタ・許可プロンプトのキー列） |
| 8 | create-chat→resume | ✅ `create-chat` が UUID を即返し、`-p --resume <id>` でそのチャットにターンが乗る（result の session_id 一致・転写も同一ファイルに追記） |
| 9 | `-p` result 形状 | ✅ `{"type":"result","subtype":"success","is_error",duration_ms,result,session_id,request_id,usage:{inputTokens,outputTokens,cacheReadTokens,cacheWriteTokens}}`・exit 0。stream-json は system/init→thinking delta→assistant→型付き tool_call（`shellToolCall`）→result |
| 10 | linux arm64 | ⏭ 未実測（本コンテナ x64。ECS/native 展開前に要実機） |

## Track 0 実測結果（2026-07-23、v2026.07.20-8cc9c0b、認証済み・本コンテナ）

- ログインフロー: `NO_OPEN_BROWSER=1 agent login` →
  `Open a browser and navigate to this link: https://cursor.com/loginDeepControl?challenge=...&uuid=...&mode=login&redirectTarget=cli`
  を標準出力に出し、承認までポーリング → `✓ Logged in as <email>` で exit 0。URL 抽出は素直（OSC-8 無し）。
- TUI（tmux 120x30・`-L cursor-probe`）:
  - 初回 trust ダイアログ「⚠ Workspace Trust Required」— `[a] Trust this workspace` / `[q] Quit`（key 駆動）。
  - idle: プレースホルダ `→ Plan, search, build anything`（初回）/ `→ Add a follow-up`（ターン後）、
    下部に `Auto`（モデル名）と cwd。ターン後は `Auto · 5.1%` — **コンテキスト使用率%が常時表示**
    （ContextBar 実装の素材）。
  - working: 点字スピナー＋`Running`＋トークン数（例 `⠘⠆ Running  67 tokens`）＋`ctrl+c to stop`。
  - 許可プロンプト（allowlist 外コマンド）: `Run this command?` `Not in allowlist: <cmd>` —
    `→ Run (once) (y)` / `Add Shell(<cmd>) to allowlist? (tab)` / `Run Everything (shift+tab)` /
    `Skip & tell the agent what to do instead (esc or n)`（キー駆動・AUQ 型の質問カード素材）。
- **残存プロセス注意**: cursor-agent 実行後に `index.js worker-server` 常駐プロセスが残る（実測 2 個）。
  セッション終了時の掃除（プロセスグループ kill）を stop.go / driver 側で必ず行う。
- ACP セッション（dd16d662）は `~/.cursor` にファイルを一切残さず、`-p --resume <acp-sid>` は
  エラーにならないが文脈は復元されない（モデルがローカル転写を検索して別セッションの答えを返した）
  — TUI⇄managed 相互乗り入れ禁止の根拠。
- trust の永続先は未特定（`agent-cli-state.json` には無し。`--trust` フラグ運用で回避可能なため深追いせず）。
- 実測に使った chat: `d78190b4-...`（-p/hooks 検証）・`3d3a3c8f-...`（TUI）・`dd16d662-...`（ACP）。
  probe 用 hooks.json は撤去済み。
