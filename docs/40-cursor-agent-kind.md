# 40. `kind=cursor`（Cursor CLI）実装計画 — Terminal + Managed 両対応

- 状態: **Track A（read＋TUI）＋Track A2（managed driver）＋Track B（配備）＋Track C
  （CP＋Console）実装済み**（2026-07-23）。残: Track D（将来）・arm64 実機実行の最終確認。
  Track C は auth.go に login start/poll/disconnect（専用フロー型・コード貼付なしの
  ブラウザ承認ポーリング）＋両 routes.go 二重登録＋mcp_stdio.go/CP mcp.go の kind enum
  総ざらい＋bridge format.go kindLabel＋Console（types union/SESSION_KINDS・registry
  descriptor 全 caps 明示・tokens.css/5 色クラスファイルの cursor twin・AgentsTab
  CursorCard・settings.ts/agentModels.ts・i18n ja/en）。`go build`/`go test`
  （cursor 12・agent+bridge 335・CP 222 緑）＋Console typecheck/i18n:lint/vitest 392/
  vite build 全緑。**v1 は login-only を確定**（API キー手動登録は Track D 送り — 下記）。
  Track A の実装は `workspace/agent/internal/agents/cursor/`（cursor.go/program.go/
  transcript.go/state.go/auth.go/models.go/stop.go/cursor_test.go）＋登録
  （session.go `KindCursor`・agent.go registry/driveState・connections.go・
  agent_models.go・fs.go denylist・session_io.go paneMode/paste/readiness）。
  Track A2 は同パッケージに `driver.go`（managedDriver＋threadHandle＋spawn/watch＋turn
  状態機械）・`acp.go`（JSON-RPC over stdio クライアント）・`mirror.go`（session/update →
  転写メモリ構築）・`driver_test.go`・`live_contract_test.go` を追加＋root 配線
  （session_turn.go managedDrivers・session_handlers.go の 4 switch・main.go ReconcileManaged・
  shutdown.go AbortManaged/Shutdown・control-plane/scheduler_wake.go injectDriver）。
  `go build ./...`・`go test`（cursor 13件・全パッケージ緑）＋`AF_CURSOR_LIVE=1` 実 CLI 契約
  テスト緑（spawn→prompt 完了→転写メモリ構築→別プロセス session/load で文脈＋転写復元を実測）。

- **Track A2 の実測反映（実装時に確定した ACP 契約 — いずれも実 CLI v2026.07.20 で検証）**:
  1. **起動は `cursor-agent acp`**（`agent` はバイナリ名で `acp` がサブコマンド。`cursor-agent
     agent acp` は誤り＝`agent` が prompt 扱いになる）。**ACP 経路も workspace trust で固まる**
     ため `--force --trust` 必須（plan は `--force` を外し承認を Interaction 化）。
  2. **転写は driver がメモリ構築**（copilot と異なる最大差分）。ACP はローカル痕跡を書かない
     ので、`session/update` の `sessionUpdate` 判別子から組み立てる: `agent_message_chunk`
     （assistant text・token 逐次）/`agent_thought_chunk`（thinking）/`user_message_chunk`
     （**replay 専用**・live turn では出ない→user ターンは driver が Send 時に確定）/`tool_call`
     （toolCallId/title/kind/rawInput）/`tool_call_update`（rawOutput の exitCode/stdout/stderr
     ＝**ツール出力が載る**・TUI JSONL より情報量が多い）/`current_mode_update`。turn 終端は
     ACP に通知が無く `session/prompt` の応答（`stopReason`）が境界。`mirror.go` の
     transcriptBuf が live/replay 両方を同じ状態機械で扱う（Idx 単調）。**停止中の managed は
     handle が無い＝ミラー空**（resume の `session/load` リプレイが再構築 — ローカル正本が無い
     設計の帰結）。
  3. **`session/set_mode`**（modeId は素の `agent`/`plan`/`ask`・応答 `{}`＋`current_mode_update`
     通知）で動的モード切替可＝`DynamicMode:true`。モデルは per-session child の `--model`
     フラグ固定（ACP に per-session 指定口なし・`cursor-agent models` の catalog id を acp
     サブコマンドが受理）＝`DynamicModel:false`。`session/cancel`（通知）→ in-flight
     `session/prompt` が `stopReason:"cancelled"` で返る（copilot 同型・実測）。
  4. **serve.go は作らず driver.go に統合**（copilot と同じ per-session child。spawn/watch/
     cmd.Wait→status.PersistExit を driver 内に置く）。worker-server 常駐の取り残し対策で
     子は専用プロセスグループ（Setpgid）にし、stopChild は `-pid` へシグナルしてグループごと落とす。
- **Track A の実測反映（計画からの改良2点 — いずれも実 CLI で検証済み）**:
  1. **セッション ID は自己採番 v4 UUID を `--resume` に渡す**（`create-chat` 事前採番は不採用）。
     実測: 未知の valid v4 UUID を `-p --resume <uuid>` に渡すとその ID で新規チャットを
     作成し以後 resume する（TUI 起動でもエラー無くコンポーザ描画を確認）。copilot の
     `--session-id` と完全同型で、起動時の追加 exec が消える。
  2. **TUI 状態は JSONL 転写末尾の分類**（copilot の events.jsonl 分類と同型）で取り、
     hooks.json 配線は v1 では**張らない**。cursor は TUI で JSONL をライブ追記する
     （user 行→応答/tool_use 行→`turn_ended`）ため、tail 分類＋driveState の cursor 分岐
     （working→idle 遷移で `MarkTurnEnd`）だけで working/idle と完了報告（docs/30）が成立する。
     グローバル `~/.cursor/hooks.json` の chatId→slot-sid キー付け問題を構造的に回避。
     許可待ち（allowlist 外コマンド確認）は JSONL に痕跡が無いため v1 は "question" を
     出さず "working" 扱い（許可カード化と rtk hook seam は Track D）。
- 状態(旧): 計画・Track 0 プローブ実施済み（2026-07-23 事前調査＋認証済み実測完了）。
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

**命名・色・アイコン**（5bf9800 教訓: 最初に確定）:

| 項目 | 決定 |
|---|---|
| kind | `cursor`（`session.go` `KindCursor`） |
| 3段命名 | label=`Cursor` / displayName=`Cursor` / assistantName=`Cursor` / short=`cu` |
| 色 | `--kind-cursor`: ローズ/マゼンタ系（dark `#d96ba1` / light `#b0316e` 起点で実装時に微調整）。ブランドは白黒モノクロでテーマ背景・opencode グレーと衝突するため不採用。既存 7 色（橙/緑/青/紫/灰/シアン/藍）と重ならない唯一の暖色系空き色相 |
| i18n | `agent.launch_hint.cursor`（ja/en）＋ `i18n:lint` 通過 |

**認証は専用フロー型**（claude/agy 型。copilot の GitHub 相乗りとは異なる）:

| 項目 | 決定 |
|---|---|
| 対話ログイン | `NO_OPEN_BROWSER=1 agent login` が URL を標準出力に出す（実測）→ claude/agy と同じ start/complete 連携フロー（URL 抽出→Console 表示→ユーザーがブラウザで承認→完了検知）。OSC-8 汚染（agy 1e8a71d）に注意して URL 抽出 |
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
  （出るなら copilot 同様に設定ファイル事前追記 or `--trust` 相当で毎回スキップ、agy 5a19080 教訓で起動毎に再固定）。
- sandbox は既定 disabled（`cli-config.json` 実測）。v1 はそのまま（fleet コンテナ自体が隔離境界）。
- **Cursor 自前の worktree 機能（`-w/--worktree`）は使わない** — セッション隔離は Console の
  worktree が正（`~/.cursor/worktrees/` に勝手に増えるのを避ける）。
- 自己更新封殺: 既定で auto-update ON。**Track B で公式手段を確定**（Track 0 の env 探索は
  空振りだったが、バンドル再解析で背景更新ゲート＝`disableAutoUpdate || channel==="static"`
  を発見）: **`--disable-auto-update` root フラグ**（全起動経路で前置）＋**`cli-config.json`
  channel:"static"**（entrypoint 再固定）の 2 経路。AUR の versions 書込禁止 fallback は不要。
  自己更新 opt-in（`AF_AGENT_SELF_UPDATE`）は rtk/agy と同じ「~/.local/bin shadow」系
  （npm でないため上流 install.sh で latest を home へ導入）。詳細は §Track B。
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
  `LiveState` の一次ソース。報告消失の教訓（19b3b92）を踏まえ同じ seam に乗せる。
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
  paneMode 分岐（1ab3eb9 教訓）用のフッタは §Track 0 実測結果 に採取済み。

**managed 契約（ADR0015 / driver.go 準拠・Track 0 プローブで主要項目を確定）**:

| 項目 | 決定 | 根拠 |
|---|---|---|
| Runtime | **per-session child**: `agent acp` stdio JSON-RPC（copilot 型・NDJSON） | 実測: initialize→session/new→session/prompt→`stopReason:"end_turn"` 一巡成功 |
| 新規/再開 | `session/new`（sessionId が返る）→ sid-store。**`session/load` でクロスプロセス resume 成功**（`loadSession:true` 宣言・履歴全量リプレイ・文脈保持を実測 — 別プロセスから前ターンのトークンを正答） | 実測 probe |
| ACP セッションの性質 | **ローカル痕跡ゼロ**（chats/ にも転写にも出ない・履歴はサーバ側保持）。`-p --resume <acp-sid>` は文脈を復元しない（実測）→ **resume は session/load 一択**。TUI⇄managed の相互乗り入れはしない | 実測 |
| streaming | `session/update` 通知: `agent_message_chunk` / `agent_thought_chunk` / `session_info_update` / `available_commands_update`（実測）＋ `tool_call`（公式 docs） | 実測 probe |
| capabilities | `promptCapabilities.image:true`（画像添付可）・`sessionCapabilities.list`・`mcpCapabilities.http/sse`・modes は `agent`/`plan`（session/new 応答で列挙 — 実測） | 実測 |
| Interrupt | `session/cancel`（公式 docs・copilot 同型。実装時に実測） | docs |
| 質問/許可 | `session/request_permission`（allow-once/allow-always/reject-once — 公式 docs）→ Interaction(question)。`cursor/ask_question`（blocking 拡張）も question に写像。防御実装（agy 3aaebf6 教訓） | docs |
| Steer/Fork | ネイティブ無し見込み → driver 内キュー / `Fork:false`（TUI には `/fork` あり） | 実装時確認 |
| Mode/Model | mode は session/new/load 応答に `modes` があり切替口あり → `DynamicMode` は実装時に set 系メソッドを確認。model は ACP で per-session 指定が見当たらず → copilot 同様 **子プロセス毎 `--model` フラグ**・`DynamicModel:false` | 実測＋docs |
| 完了報告 | runTurn 境界で `MarkTurnStart/End`（notify seam、0c80377/451ff8b 教訓） | — |
| TUIAttach | `false`（codex/copilot 型の排他切替） | — |
| 認証 | 子プロセスは `~/.config/cursor/auth.json` の ambient ログインで動く（実測: env 注入なしで完走）。`--api-key` 前置も可 | 実測 |

## トラック分割

### Track 0 — 着工前プローブ — **実施済（2026-07-23・認証済み実測）**

結果は §プローブ一覧（結果） と §Track 0 実測結果。要点: managed 可否（ACP load）**合格**、
JSONL 転写パス・形式確定（claude パーサ流用は不可だが専用パーサ容易）、hooks は TUI 経路で
全発火（-p は shell のみ・ACP は不発火）、資格情報は `~/.config/cursor/auth.json`、
auto-update 無効化の公式手段は**未発見**（versions 書込禁止 fallback で確定）。
tmux 検証は `-L cursor-probe` 専用ソケット隔離を遵守（910cc9a 教訓）。

### Track A — workspace agent 本体（read 層 + TUI）— **実装済み（2026-07-23）**

実装は上記「Track A の実測反映」の2点を織り込み済み: sids は自己採番 v4 UUID
（`create-chat` 不使用）、状態は JSONL 末尾分類（hooks.json 不使用）。auth.go は
Status() のみ（login start/complete は Track C）。以下は当初計画（差分は上記参照）。

1. `workspace/agent/internal/agents/cursor/` 新設（テンプレ: copilot）:
   - `cursor.go` — `agentImpl`（Kind/Caps/BuildLaunch/WireLive/ClearResume/Transcript）
   - `program.go` — buildProgram（上記 launch 契約。`AGENT_CURSOR_CMD`/`AGENT_CURSOR_FLAGS` override 慣習）
   - `auth.go` — `Status()`（`agent status --format json` or 資格情報ファイル）＋ login start/complete フロー
   - `hooks.go` — `~/.cursor/hooks.json` へ AF 状態フック（stop/beforeSubmitPrompt）を起動前に配線
     （起動毎に再固定、5a19080 教訓。ユーザー既存 hooks.json とのマージ規則を決める）
   - `sids.go` — sid-store（slotSid → cursor chatId。create-chat 事前採番）
   - `transcript.go` — JSONL 転写パーサ（Turn.Idx 単調採番 — 1ccb63e 教訓・pending 検知・ツール正規化）
   - `state.go` — `LiveState`（hooks 由来 status ファイル一次＋JSONL 末尾で補強）
   - `models.go` — `agent models` によるアカウント連動ライブカタログ（10 分キャッシュ・stale-if-error、
     copilot models.go と同じ agents.Flow 基盤。**スクレイプ不要でコマンドが公式にある**分 copilot より楽な見込み）
2. 登録: `internal/session/session.go` `KindCursor`、`agent.go` agentRegistry、
   `connections.go` Status 集約、`agent_models.go` switch、`fs.go` denylist **`.cursor`**
3. paneMode（`session_io.go`）: フッタ実測パターンで分岐（1ab3eb9 教訓・Track 0 で採取）。
   tmux 内の改行は Ctrl+J（公式 docs — Shift+Enter 非対応）→ 投入経路 5 つ
   （launch-seed / send_to_session / /turn / 保留中 / paste）を監査（bb81e62 教訓）
4. GracefulStop: TUI の終了コマンド実測（Ctrl+D 二度押し — 実測 help/docs）→ `stop.go`

### Track A2 — managed driver — **実装済（2026-07-23）**

1. `driver.go` — `managedDriver{agentImpl}` + `threadHandle`（copilot driver.go 踏襲）。
   spawn/watch/cmd.Wait→`status.PersistExit`（OOM 帰属）を driver に統合（**serve.go は作らず** —
   copilot と同じ per-session child のため）。子は Setpgid で専用グループにし stopChild は
   `-pid` へ SIGTERM/SIGKILL（worker-server 取り残し対策）。
2. `acp.go` — JSON-RPC over stdio クライアント（copilot acp.go とほぼ同型・プロトコル汎用）。
3. `mirror.go` — `transcriptBuf`: `session/update` からの転写メモリ構築（**cursor 固有**。ACP は
   ローカル痕跡ゼロ。live turn の user は driver が確定、replay は user_message_chunk から再生）。
   read 層 `transcript.go` は managed のとき `managedTranscript()` を返す。
4. 登録: `session_turn.go` managedDrivers、`session_handlers.go` の 4 switch（Alive/Busy/Drop/
   RemoveLedger）、`main.go` `ReconcileManaged`、`shutdown.go` AbortManaged/Shutdown、
   `control-plane/scheduler_wake.go` `injectDriver`（cursor→managed）。
5. 実 CLI 契約テスト（`AF_CURSOR_LIVE=1` opt-in・copilot live_contract_test.go 踏襲）＋
   `driver_test.go`（fake ACP で turn 状態機械・permission 往復・**session/update→転写構築**を検証）。
   HOME 隔離＋`~/.config/cursor` symlink で ambient 認証を保ちつつ AF 状態を隔離（トークンは
   読まず CLI 自身に読ませる）。

残（実装外）: managed の許可待ちで chat chip が "question" に変わらない（status を
"question" へ書かないため — copilot も同様で plan モード限定の軽微な見た目差。Pending カード
自体は managedTranscript.Pending から出る）。TUI⇄managed 相互乗り入れはしない（ACP sid と TUI
chatId は別空間）。

### Track B — 配備 — **実装済（2026-07-23）**

**auto-update 無効化は Track 0 の「公式手段なし」を覆して確定（バンドル再解析）**:
背景自己更新は起動2秒後の `setTimeout(...).unref()` で走るが、ゲートは
`disableAutoUpdate || channel==="static"` の**論理和**でスキップされる。よって公式に
2 経路ある:
- **`--disable-auto-update`**（root オプション・hideHelp だが受理・既定 false・実測合格）
  — サブコマンドの**前**に必須（`cursor-agent --disable-auto-update acp` は通り、
  `acp --disable-auto-update` は拒否 — 実測）。AF は全起動経路で前置する。
- **`cli-config.json` の `channel:"static"`**（config enum `static|prod|lab|prod-stable-internal`）
  — 恒久設定。ユーザーが素で叩いた時の背景更新（home shadow を作る）まで封じる。
  実測: channel=static で `--version`/`status`/`acp initialize` 全て正常、static は
  `lab` へ transform されず維持される。

実装:
1. `workspace/Dockerfile` — `CURSOR_VERSION`＋両 arch の `CURSOR_SHA256_*` ARG ＋版付き URL
   `downloads.cursor.com/lab/<版>/linux/<arch>/agent-cli-package.tar.gz` から
   `/usr/local/share/cursor-agent/versions/<版>/`（root 所有）へ `tar --strip-components=1`
   展開し `/usr/local/bin/cursor-agent` へ symlink（上流 install.sh と同レイアウト・wrapper が
   realpath で bundled node/index.js を解決）。**バレ名 `agent` は PATH 衝突回避で張らない**
   （AF は cursor-agent のみ呼ぶ）。**上流はチェックサム非公開のため sha256 は bake 時に
   自前計算してピン**。arch 命名は `x64`/`arm64`（install.sh 実測）。
   - **root 所有（読取専用）でも動く**ことを実測確認: CLI は版ディレクトリ内 `.running/<pid>`
     マーカーを実行時に書くが、書けなくても `--version`/`acp initialize` はグレースフルに
     動く（best-effort・claude npm-global root 所有と同構図）。
2. `env_tool_versions.go` toolSpecs に cursor 追加（Baked=`/usr/local/bin/cursor-agent`）＋
   `versions.json` に `"cursor"`＋`"cursor_sha256"`（arch 依存・agy_sha256 と同型）。
   **版数は日付形式（`2026.07.20-8cc9c0b`）で semver でない** — `extractVer` は `2026.07.20`
   を抜く（e2e-smoke は版文字列を**丸ごと**突き合わせる・semver 抽出を使わない）。
3. 自己更新 opt-in（`AF_AGENT_SELF_UPDATE`）へ cursor 追加（entrypoint）。npm でないので
   上流 install.sh（版ピン埋め込み）で latest を `~/.local` へ shadow 導入し PATH 先勝ちで
   差し替え。版比較スキップ: install.sh から latest 版を grep し実効 `--version` と一致なら
   ~100MB 再取得を省く。OFF で shadow（bin symlink 2 本＋share ツリー）を掃除し焼き込みへ復帰。
4. boot-install fallback（`BAKE_AGENT_CLIS=0` lean 経路）: `versions.json` の
   `cursor`＋`cursor_sha256` で版付き tarball を `~/.local` へ導入（agy boot と同経路）。
5. entrypoint で `cli-config.json` の `channel:"static"` を起動毎再固定（channel 鍵のみ・
   他キー保存・JSON でなければ触らない — 5a19080 教訓）。
6. `deploy/local/e2e-smoke.sh` に cursor 版検証・lean 不在検証・`versions.json`
   （cursor＋cursor_sha256）検証を追加（**非公開 URL 仕様のドリフト一次検知**）。
7. **linux arm64**: 配布資産の健全性は検証済（arm64 tarball の bundled `node` と native
   addon `node_sqlite3.node`/`file_service.linux-arm64-gnu.node`/`merkle-tree-napi...` が
   全て AArch64/glibc・strip-components=1 レイアウト正）。**実 arm64 ハードでの起動実行のみ
   未検証**（本コンテナは x64・RPi5 起動不能報告 forum #148408 は実機で要確認 — agy の
   RDRAND 実機ガードと同格の残課題）。

8. **`CI` 環境変数を渡さない（2026-08-27 追加）**。cursor CLI は `CI` を見つけると
   **対話 UI を出さない**: バナーだけ描いて composer を描画せず、打鍵も無視する
   （実測・v2026.08.25）。しかも CLI 自身の起動ログ（`/tmp/cursor-agent-logs-<uid>/
   session-*.log`）は `first_paint_ms` を出して**正常完了を報告する**ので、プロセスも
   API もヘッドレス `-p` も健康なまま UI だけが無い＝外からは「固まっている」ようにしか
   見えない。**判定は値ではなく存在**で行われ、`CI=`（空）でも死に、生き返るのは unset
   か `CI=false` だけ（実測）。Workspace のコンテナ自体は CI を設定しないが、利用者が
   Console の環境変数で足せてしまうため、AF 側で全経路から外す
   （`internal/agents/cursor/ci_env.go`）。TUI はペインのプログラム文字列を `env -u CI`
   で前置（tmux の `-e` は設定しかできず unset できない）、それ以外（ACP ドライバ・
   ログイン PTY・status/about/models プローブ・アシスタントチャットの headless）は
   `EnvWithoutCI` で `cmd.Env` から落とす。**他の kind には広げない** — copilot は
   CI 検出を自己更新の抑止に使っており（docs/36）、一律に外すと前提が壊れる。
   発見の経緯は CI の TUI 契約テストが runner でだけ 2 分間 readiness 未検出で落ち続けた
   こと（旧ピンでも同一失敗・同版がローカルでは 19.5 秒で PASS＝版ドリフトではなかった）。

### Track C — control-plane + Console — **実装済（2026-07-23）**

**実装時に確定した設計（計画からの差分）**:
- **login は start→poll（start→complete ではない）**: cursor の対話ログインは
  `NO_OPEN_BROWSER=1 cursor-agent login` が URL を出し**ブラウザ承認を CLI 自身が
  ポーリング**して `~/.config/cursor/auth.json` を書く型で、**貼り付けるコードが無い**
  （claude/agy の code paste とは異なる）。よって codex device-auth と同じ start→poll に
  した（`POST /connections/cursor/start` → `{url,flow_id}`、`POST …/poll` → `{connected}`、
  `DELETE …/cursor`）。auth.go に `HandleStart`/`HandlePoll`/`HandleDisconnect`＋uncached
  `loggedInFresh()`（poll 用・probeStatus 30s キャッシュは live poll に古すぎる）＋
  `invalidateStatus()`（login/logout 直後に /connections を即反映）。Console は
  `DeviceSteps`（`code` 省略可）を URL のみで再利用。
- **v1 は login-only（API キー手動登録は Track D 送り — ユーザー確認済み）**: cursor CLI は
  API キーの**永続化コマンドが無い**（codex の `login --with-api-key` に相当が無い・
  `CURSOR_API_KEY`/`--api-key` のアンビエント利用のみ）。活かすには暗号ストア保存＋各 exec
  への env 注入が要るが、**TUI(tmux ペイン)には注入シームが無く Program 文字列へ埋めると
  `ps` にキーが露出**（フリートの平文資格禁止に抵触）。一方 login フロー（auth.json
  アンビエント）は TUI/managed/status/models 全経路を env 注入ゼロで賄う。CursorCard は
  API キー欄を出さず login ボタンのみ（CodexCard の device 部を簡略化した構成）。

1. CP: `/api/connections` 集約（cursor は Track A で既配線）＋cursor login **start/poll**
   ルート（**routes.go 二重登録** — `workspace/agent/routes.go` と
   `control-plane/routes.go` の両方に登録済み。cp-rest-proxy-allowlist 教訓）。
   `GET /api/agents/{kind}/models` はパターンルートで自動対応（agent_models.go の cursor
   case は Track A 済み）。
2. MCP kind enum: `mcp_stdio.go`（create_session driver 注入・list_models whitelist の
   `!=` 連鎖＋エラー文＋スキーマ記述・get_session_usage/get_agent_usage の注記）＋
   `control-plane/mcp.go`（同項目＋list_my_sessions/create_session 記述）の**両方**を総ざらい
   （413b696 教訓）。
3. Console:
   - `types/session.ts` — union / SESSION_KINDS（表示順どおり codex と copilot の間へ挿入）/
     ConnectionsStatus.cursor
   - `agents/registry.ts` — descriptor（**caps 全項目を明示決定**した — chat/transcript/model/
     tuiStartMode/runsInDir/launchableFromRepo のみ true。effort は**モデル id に畳まれる**ため
     false、fork は TUI 限定で false、contextBar/imagePaste は v1 未配線で false=f49e162/41c78b0
     教訓）。`managedDriver:true`（Track A2）・`tuiMemoryCost:""`（per-session child・copilot 同型）・
     icon=`inspect`（cursor 相当の codicon が無いためポインタで代替）・color=ローズ `#d96ba1`/
     `#b0316e`・short=`cu`・repoLaunchKinds へ挿入
   - `tokens.css` `--kind-cursor`（**dark/light 両テーマ**）＋色クラスの cursor twin を
     **全ファイルに追加**（`app.css` `.kc-cursor`／`terminal.css` `.kind-tag.kind-cursor`
     dark+light／`sessions.css` `.sess-kic.kind-cursor` dark+light／`settings.css`
     `.pb-cursor`／`ui.css` seg-btn）——設定モーダルのアイコンチップ用色クラスは漏れやすい
     ので copilot の全クラス族に twin があるか grep で突き合わせ確認
   - `AgentsTab.tsx` — CursorCard（**login フローのみ**・DeviceSteps 再利用・LaunchDefaults
     kind union に cursor 追加・CodexCard と CopilotCard の間へ挿入）
   - `lib/settings.ts`（DEFAULT_AGENT_LAUNCH＋normalize ループに cursor）・
     `lib/agentModels.ts`（`isDynamic` に cursor＝ライブカタログ。effort 無しなので
     FALLBACK_EFFORTS には入れない）
   - i18n ja/en（launch_hint.cursor＋cursor カード文言 8 キー）＋ `i18n:lint` 通過
   - `questionKeys.ts`: 変更不要 — managed 質問は buildRespondAnswers（汎用・kind 非依存）、
     TUI cursor は v1 で "question" を出さない（許可待ち検知は Track D）。
4. availability: registry の `available` 述語＝`supported!==false && connected`（agy/copilot
   同型）。conns 未取得時は表示（rail の null=show-all）＝c6514b5 教訓に整合。
5. bridge: `internal/bridge/format.go` kindLabel に `case "cursor": return "Cursor"`
   （registry.ts と同期コメント維持）。

### Track D — 残課題・将来

0. **API キー手動登録経路**（v1 で login-only に確定したため送り）: cursor は API キーの
   永続化コマンドが無く `CURSOR_API_KEY`/`--api-key` のアンビエント利用のみ。実装するなら
   暗号ストア（`secrets.Data` に cursor フィールド追加）＋各 exec への env 注入だが、
   **TUI(tmux ペイン)には安全な注入シームが無い**（Program 文字列へ埋めると `ps` 露出）。
   採るなら「managed 子＋status/models プローブにのみ注入し、TUI は login 必須」と割り切るか、
   LaunchPlan に per-session env マップを足す設計変更が要る。
1. **WS バー使用量表示は v1 スキップ（プラン残量・セッション使用トークンの両方）**。
   2 種を分けて判断した（後者を §使用量表示の実現可否プローブ で実測）:
   - **プラン残量チップ**: 公式 API/コマンドが無く、非公式 API
     （`cursor.com/api/usage`・`api2.cursor.sh GetCurrentPeriodUsage`）は usage-chip 429 事件と
     同じ脆さのため不採用（この判断は不変）。
   - **セッション使用トークン数**（他エージェントの ContextBar 相当。残量ではなく消費済み）:
     **ライブ経路（managed=ACP／TUI=JSONL）には公式契約上トークン情報が一切乗らないため v1
     不採用**。実測（2026-07-23 session2）: ①**ACP** の `session/prompt` 応答は
     `{"stopReason":"end_turn"}` のみ、`session/update` 通知も text/thought/tool/mode/info の
     チャンクのみで **usage/token フィールドは皆無**。②**JSONL 転写**（"Claude Code 互換" を
     謳うが）は role/message/turn_ended だけで **`message.usage` を持たない**（本物の Claude
     Code JSONL と異なる — TUI/-p 両方の実転写で確認）。③トークンが載るのは
     **`-p --output-format json|stream-json` の終端 `result.usage`
     （`{inputTokens,outputTokens,cacheReadTokens,cacheWriteTokens}`）だけ**だが、これは
     one-shot batch 経路であってライブのオーケストレーションセッション（managed=ACP／TUI）
     では使わない。よって「stream-json の per-turn トークンでセッション単位表示」は
     **assistant チャット（下記 3・`-p` 基盤）でのみ将来可**であって、WS バーの
     セッション使用量には届かない。TUI フッタは `Running N tokens`/`context X%` を描くが、
     これは TUI 描画文字列であり JSONL に無い＝依存は false-idle 教訓のドリフト禁忌に抵触。
     実装するなら (a) cursor 上流が ACP `session/update`／prompt 応答に usage を載せるのを待つ、
     (b) managed を `-p` stream-json 駆動へ切替（＝ACP の `session/load` クロスプロセス resume を
     捨てる＝ADR0023 決定1の放棄）のいずれかが要り、v1 では割に合わない。
2. rtk: base-URL 差し替え不可（プロバイダ直結でない）→ **hooks seam**（`rtk hook cursor` を
   rtk 側に新設し `beforeShellExecution` に配線）。cursor hooks がコマンド書換
   （copilot `modifiedArgs` 相当）を許すかは要プローブ — 許さなければ指示ベース（codex/agy 同格）。
3. アシスタントチャット headless バックエンド（`-p --output-format json`。chat_providers.go）。
4. 画像添付・`/fork`・`/summarize`（コンテキスト圧縮）・Cloud Agent（`&` プレフィックス）連携。
5. Claude Code 会話インポート（`import_cc_conversation` フラグをバンドル内に確認 — 将来の移行導線）。

## 教訓反映表（agy docs/32・copilot docs/36 → cursor での対応）

| 過去の指摘 | cursor での対応 |
|---|---|
| resume ID 取得不能（agy 46271bb） | AF 側採番の chat ID を `--resume` に渡して構造的に回避（copilot `--session-id` と同型。当初計画の `create-chat` 事前採番は Track A で自己採番 v4 UUID へ変更 — §Track A の実測反映） |
| auth URL の OSC-8 汚染（agy 1e8a71d） | login start/complete フローの URL 抽出で同対策を最初から適用 |
| tmux kill-server 全滅（agy 910cc9a） | probe/E2E は `-L cursor-probe` 専用ソケット隔離 |
| Turn.Idx 未採番（1ccb63e） | transcript.go 単調採番＋単調増加テスト |
| paneMode 分岐漏れ（1ab3eb9） | Track 0 でフッタ実測 → 実装。改行 Ctrl+J の投入経路監査（bb81e62）込み |
| MCP ツール欠落（413b696） | mcp_stdio.go / CP mcp.go 総ざらい済（Track C — create_session driver・list_models whitelist・各 usage 記述） |
| turn 終端未検出→報告不発（0c80377/19b3b92） | hooks stop を status seam に乗せ、managed は MarkTurnStart/End |
| caps 明示漏れ（f49e162/41c78b0） | descriptor caps 全項目を明示決定済（Track C — effort/fork/contextBar/imagePaste は根拠付きで false） |
| 権限「発生しない前提」崩壊（agy 3aaebf6） | request_permission / ask_question を防御実装 |
| conns ローディング中の誤露出（c6514b5） | connsDone ゲート |
| 設定の一回きり固定（agy 5a19080） | hooks.json / trust / auto-update 封殺は起動毎に再固定 |
| ストア内部依存の false-idle（opencode 教訓） | store.db（SQLite blob）は読まない。hooks＋JSONL の公式契約のみ |
| CP allowlist 漏れ（cp-rest-proxy-allowlist） | cursor login start/poll/DELETE は両 routes.go に同時登録済（Track C） |
| 非公式 API の突然死（usage-chip 429） | 使用量チップ v1 不採用（Track D） |
| インストーラ既存あり no-op（agy 6111248） | tarball 直展開のため非該当だが、焼き込み検証を e2e-smoke に追加 |

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
| 1 | ACP 一巡＋load resume | ✅ **合格**: initialize（`loadSession:true`）→ new → prompt（chunk streaming）→ `end_turn`。**別プロセス `session/load` で履歴リプレイ＋文脈保持を実証**。**cancel/set_mode も Track A2 実装時に実測合格**（cancel→`stopReason:"cancelled"`・set_mode→`{}`＋`current_mode_update` 通知・modeId は素の agent/plan/ask） |
| 2 | JSONL 転写 | ✅ `~/.cursor/projects/<cwdスラグ>/agent-transcripts/<chatId>/<chatId>.jsonl`。Anthropic content block 型（`tool_use` あり・**tool_result 無し**・uuid/timestamp 無し）＋`turn_ended`。TUI/-p は書く・**ACP は書かない** |
| 3 | hooks 実発火 | ✅ TUI: beforeSubmitPrompt/beforeShellExecution/stop 全発火。`-p`: beforeShellExecution のみ。ACP: 不発火。payload に conversation_id/`transcript_path`/cursor_version/user_email。コマンド書換の可否は未検証（rtk 実装時） |
| 4 | 資格情報 | ✅ `~/.config/cursor/auth.json`（600・accessToken/refreshToken 平文 JSON）。ホームボリュームで持続。`status --format json` はクリーンな構造化 JSON |
| 5 | auto-update 封殺 | ✅ **Track B で公式手段を確定**（Track 0 の「手段なし」を覆す）。バンドル再解析で背景更新ゲート＝`disableAutoUpdate \|\| channel==="static"`。**`--disable-auto-update` root フラグ**（サブコマンドの前・実測合格）＋**`cli-config.json` channel:"static"**（実測: version/status/acp 正常・static 維持）の 2 経路で封殺。versions 書込禁止 fallback は不要に |
| 6 | `agent models` | ✅ `id - 表示名` のテキスト行（`--format json` は無い）。アカウント連動（auto/composer/claude/gpt/grok 系を確認）。要認証 |
| 7 | TUI 実測 | ✅ §Track 0 実測結果（trust プロンプト・フッタ・許可プロンプトのキー列） |
| 8 | create-chat→resume | ✅ `create-chat` が UUID を即返し、`-p --resume <id>` でそのチャットにターンが乗る（result の session_id 一致・転写も同一ファイルに追記） |
| 9 | `-p` result 形状 | ✅ `{"type":"result","subtype":"success","is_error",duration_ms,result,session_id,request_id,usage:{inputTokens,outputTokens,cacheReadTokens,cacheWriteTokens}}`・exit 0。stream-json は system/init→thinking delta→assistant→型付き tool_call（`shellToolCall`）→result |
| 10 | linux arm64 | ◐ **配布資産は健全と検証**（arm64 tarball の bundled node ＋ native addon 群が全て AArch64/glibc・レイアウト正・sha ピン済）。**実 arm64 ハード起動のみ未検証**（本コンテナ x64・forum #148408 は実機確認要・agy RDRAND ガードと同格の残課題） |

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

## 使用量表示の実現可否プローブ（2026-07-23 session2、v2026.07.20-8cc9c0b、本コンテナ・認証済み）

利用者フィードバック（「他エージェント同様のセッション使用トークン数を WS バーに出せないか」）
を受け、ライブ ACP／JSONL 転写／`-p` の 3 経路で **token/usage の有無**を実バイナリで採取した
（Track D-1 の判断根拠）。結論: **ライブ経路には usage が乗らず、載るのは `-p` batch のみ**。

| 経路 | usage/token の有無 | 実測 |
|---|---|---|
| **ACP `session/prompt` 応答** | ❌ | `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}` — stopReason だけ |
| **ACP `session/update` 通知** | ❌ | `agent_message_chunk`/`agent_thought_chunk`/`tool_call`/`tool_call_update`/`session_info_update`（title）/`available_commands_update`/`current_mode_update` のみ。token 系の update 種別は出ない |
| **JSONL 転写（TUI/-p が書く "Claude Code 互換"）** | ❌ | 行は `{"role":"user"/"assistant","message":{content:[…]}}` と `{"type":"turn_ended","status"}` のみ。**`message.usage` を持たない**（本物の Claude Code JSONL との最大差）。TUI 実転写・`-p` 実転写の両方で確認 |
| **`-p --output-format json`** | ✅ | `…"result":"pong","usage":{"inputTokens":3693,"outputTokens":65,"cacheReadTokens":22144,"cacheWriteTokens":0}` |
| **`-p --output-format stream-json`** | ✅（終端 `result` 行のみ） | 中間の `thinking`/`assistant` 行に usage は無く、終端 `result` 行に上記 usage が載る |
| **TUI フッタ描画** | ⚠️ 表示のみ | `⠘⠆ Running 67 tokens`／`Auto · 5.1%` を描くが JSONL には無い＝描画文字列。依存は false-idle 禁忌 |

- 現状のコードは cursor で usage を一切拾っていない（`driver.go onNotify` の decode 構造体に
  usage フィールド無し・`runTurn` は `session/prompt` 応答を `{StopReason}` のみで unmarshal・
  `mirror.go`/`transcript.go` は `transcript.Turn` の `InTok/OutTok/CacheRead/CacheCreate/CtxWindow`
  を設定しない）。`registry.ts` の descriptor も `contextBar:false`。**この設計は上記実測で追認**
  （拾いたくても経路にデータが無い）。
- **判断**: WS バーのセッション使用トークン表示は **v1 見送り（Track D 継続）**。実現には上流が
  ACP に usage を載せるのを待つか、managed を `-p` stream-json 駆動に替えて ACP の
  `session/load` クロスプロセス resume（ADR0023 決定1）を捨てるかが要る。`-p` の `result.usage`
  はアシスタントチャット headless バックエンド（Track D-3）でのみ活きる。

### Free プラン時のモデル制約 → **Free 検知でピッカー絞り込みを実装（session2）**

利用者から「GLM-5.2 を選んだらアップグレードしろと言われた」との報告。実測で **Free プランは
named model を一切使えない**ことを確認:

- `-p --model <named>` で `glm-5.2`/`claude-opus-4-8`/`gpt-5.2`/`grok-4.5`/`gemini-3-flash` は全て
  `ActionRequiredError: Named models unavailable Free plans can only use Auto. Switch to Auto or
  upgrade plans to continue.`。ACP 経路では同事象が assistant テキスト `Upgrade your plan to
  continue` として表面化。
- Free で通ったのは **Auto（catalog id `auto`／ACP `default[]`）と Composer 系（`composer-2.5`・
  `composer-2.5-fast`）のみ**（`result:"ok"`＋usage 正常）。`cursor-agent models` は**プランに
  関係なく全モデルを列挙**するため、ピッカーが named を見せていたのが利用者混乱の元。

**実装（利用者判断＝「Free 使用可のみに絞る」）:**
- **Free 判定は `cursor-agent about` の `Subscription Tier` 行**（実測: `Subscription Tier   Free`。
  `status --format json` にはプラン情報が無く `models` カタログもプラン非依存＝これが唯一の
  クリーンな公式シグナル）。`models.go` に `freePlan()`（`about` パース・`aboutTierRe`・10 分
  キャッシュ・stale-if-error）＋`freeUsableModels()`（`composer` 前置きのみ残す）を追加し、
  **Free のとき `Models()` を Composer 系だけに絞る**。Auto はピッカーの 既定（`["", 既定]`）として
  別枠で常に出る（`agentModels.ts` が prepend）ので、Free の見え方は **既定(Auto)＋Composer のみ**、
  named は非表示。有料／判定不能時は全カタログのまま（過剰制限しない安全側）。**FE 変更ゼロ**
  （バックエンドの絞り込みだけで成立）。ライブ実測: 本 Free アカウントで `Models()` が
  `composer-2.5`・`composer-2.5-fast` の 2 件のみを返すことを確認（`probeFreePlan→free=true`）。
- 起動既定（`model==""||"auto"` で `--model` 無し＝サーバ側 Auto）はそのまま。`about` の
  `Model  Auto` が示す通り Free アカウントのサーバ側既定は Auto で、絞り込みで named を選ばせない
  方針と併せて free wall を回避する。テスト: `TestAboutTierRe`・`TestFreeUsableModels`（go test 14 緑）。

### モデル表示（応答ごとのモデルバッジ — session2 実装）

利用者フィードバック「Cursor の応答にモデルを表示できないか」。ミラーは既に per-turn の
`turn.model` バッジ（`MirrorView` 3134 行・`prettyModel` で短縮）を持つが、cursor はそこに値を
流していなかった。**実装（利用者選択＝「応答ごとのバッジ（他エージェント共通）」）:**

- **表示できるのは「セッションの設定モデル」**（Auto／Composer／明示選択したモデル）。cursor は
  **モデルがセッション固定**（per-session child・`--model` 起動時固定・DynamicModel:false）なので、
  1 セッション内の全 assistant ターンに同じモデルが載る。
- **取得元**: managed=ACP `session/new`/`load` の `models.currentModelId`（`currentModelOf` 追加。
  Auto は `default[]`）。TUI=セッション meta の起動モデル（`m.Model`）。read 層で各 assistant
  ターンに `transcript.Turn.Model` をスタンプ（`stampModel`）→ FE の既存バッジ経路に載る＝
  **FE 変更ゼロ**。`displayModel` で正規化（ACP の `[params]` を剥がす・`` /`auto`/`default[]` は
  "Auto"）。managed は明示選択（`settings.Model`）を優先し、Auto/未指定のみ `currentModelId` に
  フォールバック。
- **制約（実測・明記）**: **Auto が各ターンで実際に解決した具体モデルは公式経路に出ない**
  （JSONL/-p result に model 無し・stream-json init は `"Auto"` のまま・ACP `session/update` に
  応答毎 model 無し — §使用量表示の実現可否プローブ と同じ経路調査）。よって Auto セッションは
  バッジに「Auto」と出るだけで、どのモデルが答えたかは示せない。明示モデル選択時のみバッジは
  正確。テスト: `TestDisplayModel`・`TestStampModelAssistantOnly`（go test 16 緑）。
