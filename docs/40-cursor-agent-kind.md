# 40. `kind=cursor`（Cursor CLI）実装計画 — Terminal + Managed 両対応

- 状態: **計画**（2026-07-23 事前調査完了・実装未着手）。
  採用判断は [decisions/0023](decisions/0023-cursor-agent-kind.md)。
  実 CLI の実測は本ドキュメント末尾 §実測記録（v2026.07.20-8cc9c0b を本コンテナで実測）。
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
| 状態判定 | `agent status --format json`（要プローブ: JSON 形状） |
| 資格情報の保存先 | **非公開**（"securely stored locally" のみ。`~/.cursor` 配下と推定）→ プローブで特定し、`fs.go` denylist に **`.cursor`** を追加（copilot `.copilot` と同じ平文トークン対策）。※ プロジェクト側 `.cursor/`（rules/commands）とはスコープが別であることを確認する |
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
- 自己更新封殺: 既定で auto-update ON・**公式な無効化手段が未確認**（要プローブ:
  config キー/env の探索。最終手段は AUR 方式 = versions ディレクトリの書込禁止）。
  自己更新 opt-in（`AF_AGENT_SELF_UPDATE`）への追加は rtk/agy と同じ「~/.local/bin shadow」系の
  設計になる見込み（npm ではないため claude/codex 経路は使えない）。
- モデル: `--model <id>`（`claude-opus-4-8[context=1m,effort=high]` 形式のパラメータ付きあり — 実測 help）。

**read 正本（transcript/状態）**:

- 真の正本は `~/.cursor/chats/<workspace-hash>/<chatId>/store.db`（SQLite blob・**非公開形式**）。
  **これは読まない**（opencode ストア契約変更で false-idle を踏んだ教訓 — 非保証内部への依存禁止）。
- 読むのは公式契約側 2 本:
  1. **hooks**（`~/.cursor/hooks.json`）— `stop` / `beforeSubmitPrompt` / `beforeShellExecution` 等が
     JSON over stdin で `conversation_id`・`transcript_path` を渡す（実測: バンドル内にイベント名確認、
     公式 docs にプロトコル記載）。claude と同型の working マーカー（beforeSubmitPrompt=turn 開始、
     stop=turn 終了）を status ファイルに書く → `LiveState` の一次ソース。
     報告消失の教訓（1f64c57: 自己修復の status.Remove がマーカーを消す）を踏まえ同じ seam に乗せる。
  2. **JSONL 転写**（hooks の `transcript_path` が指す Claude Code 互換 JSONL、changelog 記載の公式機能）
     → `transcript.go` のソース。形式互換なら claude パーサの流用可能性あり（要プローブ）。
- TUI 文字列（スピナー/フッタ）には依存しない（false-idle 第3〜6次の教訓。週次リリースでドリフト前提）。
  paneMode 分岐（8780956 教訓）に必要な最小限のフッタ実測のみ行う。

**managed 契約（ADR0015 / driver.go 準拠・着工前に §プローブ で確定）**:

| 項目 | 見込み | 確定条件 |
|---|---|---|
| Runtime | **per-session child**: `agent --api-key/資格情報 acp [--model]` stdio JSON-RPC（copilot 型） | probe: ACP の initialize/new/prompt/cancel 一巡 |
| 新規/再開 | ACP session/new 相当 → chatId 対応付け。クロスプロセス resume（session/load 相当）の有無が **managed 可否の分水嶺** | probe: load 系メソッドの有無・履歴リプレイ |
| streaming | ACP 通知（thinking ブロック対応あり — 公式 docs） | probe |
| 質問/許可 | `session/request_permission` 型（allow-once/allow-always/reject-once — 公式 docs）→ Interaction(question) に写像。`cursor/ask_question`（ブロッキング拡張メソッド — 公式 docs）も question に写像 | probe |
| Steer/Fork | ネイティブ無し見込み → driver 内キュー / `Fork:false`（TUI には `/fork` あり） | probe |
| Mode/Model | ACP でモデル・モード選択可（公式 docs）→ `DynamicMode`/`DynamicModel` は probe 結果で決定 | probe |
| 完了報告 | runTurn 境界で `MarkTurnStart/End`（notify seam、5facc6e/dffd84c 教訓） | — |
| TUIAttach | `false`（codex/copilot 型の排他切替） | — |

probe の結果 resume（load 相当）が欠けるなら **v1 は Terminal 専用（agy 型 MVP）に縮退**し、
managed は Track D へ送る（判断基準を先に固定しておく）。

## トラック分割

### Track 0 — 着工前プローブ（本コンテナに v2026.07.20 導入済み・要ログイン）

未実測項目の実測。**全部 §プローブ一覧 に列挙**。特に managed 可否（ACP load）、
JSONL 転写の実パスと claude 互換度、hooks の実発火、資格情報ファイルの特定、
auto-update 無効化手段の 5 点が契約を左右する。tmux 検証は `-L cursor-probe`
専用ソケット隔離（84139d2 教訓）。

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

## プローブ一覧（Track 0 — 着工前に実測で潰す）

| # | 項目 | 契約への影響 |
|---|---|---|
| 1 | `agent acp` 一巡（initialize→new→prompt→cancel）＋ **load 系 resume の有無** | **managed 可否の分水嶺**。無ければ v1 Terminal 専用に縮退 |
| 2 | JSONL 転写の実パス（hooks `transcript_path`）と claude 互換度・全経路（TUI/-p/acp）ライブ追記か | transcript.go の設計（claude パーサ流用可否） |
| 3 | hooks 実発火（stop/beforeSubmitPrompt/beforeShellExecution）・既存 hooks.json とのマージ・コマンド書換の可否 | state.go 一次ソース／rtk seam |
| 4 | 資格情報の保存ファイル特定・コンテナ間可搬性（ホームボリューム persist で生きるか）・`status --format json` 形状 | auth.go / fs.go denylist / 接続カード |
| 5 | auto-update 無効化手段（config/env 探索。無ければ versions chmod -x） | Track B 焼き込み |
| 6 | `agent models` の出力形式（構造化か）・要認証・プラン別可否 | models.go |
| 7 | TUI フッタ/スピナー/初回 trust プロンプト採取（tmux `-L cursor-probe`） | paneMode / trust 契約 |
| 8 | `create-chat` → TUI `--resume` → 別プロセス resume の一巡 | sid-store 設計 |
| 9 | `-p --output-format json` の終端 result 形状・異常系 exit code | ブリッジ/アシスタント将来対応 |
| 10 | linux arm64 動作（該当ホストがある時点で） | ECS/native 展開条件 |
