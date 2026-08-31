# 36. `kind=copilot`（GitHub Copilot CLI）実装計画 — Terminal + Managed 両対応

- 状態: **実装済み**（2026-07-21 計画・同日実装完了。全トラック✅、実 CLI 契約テスト通過）。
  採用判断は [decisions/0019](../decisions/0019-copilot-agent-kind.ja.md)。事前調査・実バイナリ実測は本ドキュメント末尾 §実測記録。
- ゴール: `copilot`（GitHub Copilot CLI, npm `@github/copilot`）を第5のエージェント種別として組み込む。
  **agy と異なり v1 から Managed driver（既定）＋ Terminal (CLI) の両対応**とする。
- 根拠: Copilot CLI v1.0.73（2026-02 GA）は `--acp`（Agent Client Protocol, JSON-RPC over stdio）・
  `-p --output-format json`（JSONL イベントストリーム）・`--session-id` 外部採番・`session/load`
  クロスプロセス resume を備え、codex app-server 相当の managed 化が公式経路で成立する（全て実測済）。
- 教訓の反映: docs/32（agy）で踏んだ全指摘・修正（46 コミット）を §agy 教訓反映表 で個別対応。

## 先に固定する契約

**認証は GitHub 連携相乗り型**（claude/codex 型の専用フローなし）。**順序は
git プロバイダの GitHub 連携が先**で、それが唯一の認証源 — copilot 側に独立した
ログインは無く、`copilot.connected` は GitHub 連携の導出値（切断も GitHub 側に連動、
Copilot カードは状態表示＋起動既定のみ）:

| 項目 | 決定 |
|---|---|
| トークン供給 | `BuildLaunch`／managed 子プロセス起動時に `COPILOT_GITHUB_TOKEN="$(gh auth token)"` を注入（gh 透過認証ラッパー経由の gho_ OAuth。Copilot CLI は gh CLI アプリのトークンを公式サポート） |
| `GET /connections` の `copilot` | `connected` = `gh auth token` 成功（＝GitHub 連携済み）。専用 start/complete/DELETE ルートは**作らない**（GitHub 連携の従属） |
| 前提 | ユーザーの GitHub アカウントに Copilot サブスク（Free 枠含む）が必要。未サブスクは初回ターンでエラー表面化（カードに注記） |
| 注意 | Copilot CLI が受けるのは gh/Copilot CLI アプリの OAuth と fine-grained PAT（Copilot Requests 権限）のみ — **classic PAT（ghp_）非対応**。フリートの GitHub OAuth は gho_ を作るため通常は非該当だが、PAT 手動登録の GitHub 連携では「カード接続済みなのに copilot だけ認証エラー」があり得る（必要になったら Status にトークン種別検査を足す） |

**launch 契約**（TUI・managed 共通のセッション同一性）:

- **セッション UUID は AF 側で外部採番**する（`--session-id <uuid v4>`、RFC4122 v4 必須・実測）。
  初回起動時に採番して sid-store に保存 → resume は TUI `--resume=<sid>` / managed `session/load`。
  agy の「resume UUID が取れない」問題（202e439）は構造的に発生しない。
- 権限は fleet 既定の bypass 相当: `--allow-all`（tools+paths+urls）。
- フォルダ trust は **`$COPILOT_HOME/config.json` の `trustedFolders[]` へ起動前追記**でスキップ（実測）。
- セッションの GitHub 同期・リモート操縦は**既定オフ**: `--no-remote --no-remote-export`
  （フリート外への会話流出と二重操縦を防ぐ。env override で再有効化可能にする）。
- 自己更新封殺: `COPILOT_AUTO_UPDATE=false`（CI env 検出でも自動 OFF だが明示する）。
- モデル: `--model <id>`（未指定=auto ルーティング）。effort: `--effort <level>`。
  モード: `--mode plan` 等（interactive/plan/autopilot）。
  - ⚠️ **Auto は `--effort` 非対応**（`Model "auto" does not support reasoning effort configuration` で起動失敗）。
    Auto は copilot の既定であり **Free プランの唯一のモデル**なので、effort を既定以外にすると Free では常に落ちる
    フットガン。→ `program.go`/`driver.go` は **concrete な非 auto モデルの時だけ `--effort` を渡す**。フロント
    `useEffortOptions` も copilot+auto/未指定は effort を既定のみに寄せる（バックエンドのガードと一致）。

**read 正本（transcript/状態）は `$COPILOT_HOME/session-state/<sid>/events.jsonl`**:

- TUI・`-p`・`--acp` の**全経路で同一形式のイベント JSONL がライブ追記**される（実測）。
  `user.message` / `assistant.message` / `assistant.turn_start|turn_end` / `tool.execution_start|complete` /
  `permission.requested|completed` / `session.start|resume|model_change|usage_checkpoint`。
- Transcript() はこれを正規化（**Turn.Idx 単調採番**、7354916 教訓）。managed でも CLI 自身が書くため
  二重永続化なし（ADR0015-3 準拠）。
- セッション横断索引は `session-store.db`（SQLite）。使用中マーカー `session-state/<sid>/inuse.<pid>.lock`
  が存在（実測）→ TUI⇄managed 排他判定の材料。

**managed 契約（ADR0015 / driver.go 準拠）**:

| 項目 | 決定 | 根拠（実測） |
|---|---|---|
| Runtime | **per-session child**: セッション毎に `copilot --acp --allow-all [--model][--effort]` を stdio JSON-RPC で駆動 | ACP に per-session モデル指定が無い（configOptions は mode/allow_all のみ）→ 子プロセス毎フラグで解決。メモリは TUI pane と同等 |
| ProcessModel | `"per-session-child"` | 〃。exit/OOM 記録は子の `cmd.Wait()` で per-session に正確化 |
| 新規/再開 | `initialize` → `session/new`（返る sessionId を sid-store へ）/ `session/load <sid>`（履歴全量リプレイ・文脈保持を実測） | probe2 |
| turn | `session/prompt`（blocking、`stopReason` で終端）。streaming は `session/update` 通知（`agent_message_chunk` / `agent_thought_chunk` / `tool_call` / `tool_call_update`） | probe1 |
| Interrupt | `session/cancel` 通知（実測: 即中断・prompt が `end_turn` で返る） | probe1 |
| Steer | ネイティブ無し → **driver 内キュー（opencode 型 accept）** | ACP 仕様 |
| Fork | ネイティブ無し → `Fork:false` | — |
| Mode | `session/set_mode`（agent/plan/autopilot、実測）→ `DynamicMode:true`。AF 語彙マップ: plan→"plan"、agent→"normal"（autopilot は v1 非露出） | probe3 |
| Model/Effort | ACP に動的変更なし → v1 は `DynamicModel/DynamicEffort:false`（起動時フラグ固定。変更はセッション再作成 or 将来: idle 時の子再起動+session/load） | probe2 |
| 質問/許可 | `session/request_permission`（server-initiated request、allow_once/allow_always/reject_once・実測）→ `Interaction`（Kind:"question"）に写像し waiting_interaction。**allow-all 運転でも防御的に実装**（agy df996e4 教訓: 「発生しない前提」を信用しない）。ask_user は ACP では平文テキスト+end_turn に落ちる（実測）→ 通常チャット往復で成立 | probe2/3 |
| 完了報告 | runTurn 境界で `agents.MarkTurnStart` / `MarkTurnEnd(sid, terminal)`（notify seam、f3e63f6/eb3eb31 教訓） | — |
| TUIAttach | `false`（codex 型の排他切替。inuse.lock で busy 検知可） | — |

## トラック分割

### Track A — workspace agent 本体（read 層 + TUI）— **実施済（2026-07-21）**

1. `workspace/agent/internal/agents/copilot/` 新設（テンプレ: codex/agy）:
   - `copilot.go` — `agentImpl`（Kind/Caps/BuildLaunch/WireLive/ClearResume/Transcript）
   - `program.go` — buildProgram（上記 launch 契約。`AGENT_COPILOT_CMD`/`AGENT_COPILOT_FLAGS` override 慣習）
   - `auth.go` — `Status()`（gh token 判定）。専用フロー無し
   - `trust.go` — config.json `trustedFolders` 事前追記（起動毎、agy 00dacc5 教訓で毎回再固定）
   - `sids.go` — sid-store（slotSid → copilot session UUID。外部採番なので書くだけ）
   - `transcript.go` — events.jsonl パーサ（Turn.Idx 単調・pending 検知・ツール正規化）
   - `state.go` — `LiveState`（events.jsonl 末尾: turn_start 未閉→working、permission.requested 未完→question）
   - `models.go` — プラン連動ライブカタログ（§実装後の追加実測。当初計画の静的リストは廃止）
   - `rtk.go` — **実装済み（決定的フック方式）**。当初想定の COPILOT_CUSTOM_INSTRUCTIONS_DIRS
     指示ベースは採らず、rtk 本体の `rtk hook copilot`（preToolUse フック処理器）を
     ユーザースコープ `$COPILOT_HOME/hooks/rtk.json` の preToolUse に配線（native 形式・
     matcher `bash`）。rtk が `modifiedArgs` でシェルコマンドを `rtk <cmd>` へ透過書換 —
     claude/opencode と同格の決定的連携（codex/agy の指示ベースより上）。プラグイン
     (`--plugin-dir`)方式は plugin 定義 preToolUse が発火しない既知バグ
     (github/copilot-cli#2540)のため不採用。実 CLI 1.0.73 で end-to-end 検証済
     （フック発火・modifiedArgs 適用・`rtk gain` 増分）。durable prefs/reconcile は
     `agent_rtk.go`（`Copilot *bool`・既定 ON）、Console トグルは AgentsTab CopilotCard。
2. 登録: `internal/session/session.go` `KindCopilot`、`agent.go` agentRegistry、
   `connections.go` Status 集約、`agent_models.go` switch、`fs.go` denylist **`.copilot`**（平文トークン対策）
3. **paneMode**（`session_io.go`）: フッタ実測パターンで分岐（0b0a07f 教訓）:
   working=`◎ Working`（+`esc interrupt`）、idle=`/ commands · ? help`、下書きあり=`@ files · # issues`。
   ブート時は trust 事前追記済みなら直で idle フッタ。launch-seed は idle フッタ readiness 待ち。
4. GracefulStop: 保留カード Escape → `/exit` → Enter（スラッシュメニュー確定は実測済: /exit入力→Enter で実行）。
   exit サマリに usage が出る（実測）。
5. `questionPending`・`submitPromptTUI`・MCP ツール（list_models/create_session/get_agent_usage 等）の
   kind 分岐監査（69fde0b/351fdf6 教訓）。

### Track A2 — managed driver — **実施済（2026-07-21）**

1. `driver.go` — `managedDriver{agentImpl}` + `threadHandle`（state/queue/pump/inter/events, buffer 64）。
   `Resume` 冪等（子プロセス生存なら再利用。死んでいれば spawn → initialize → session/load）。
2. `serve.go` — per-session child supervisor（`acpClient` stdio JSON-RPC）。
   `cmd.Wait()` → `status.PersistExit`（OOM 帰属）→ runtimeLost → 次 Resume で再 spawn+load。
3. 登録: `session_turn.go` managedDrivers、`session_handlers.go` の
   managedAlive/managedBusy/dropManagedRuntime/removeManagedLedger 全 switch、
   `main.go` boot の `ReconcileManaged`、`shutdown.go` の Abort/Shutdown。
4. Capabilities: `{ProcessModel:"per-session-child", Steer:true(キュー), Fork:false,
   DynamicModel:false, DynamicEffort:false, DynamicMode:true, Questions:true, TUIAttach:false}`。

### Track B — 配備 — **実施済（2026-07-21）**

1. `workspace/Dockerfile` — 既存 npm ピン RUN に `@github/copilot@${COPILOT_VERSION}` 追加、
   `ENV COPILOT_AUTO_UPDATE=false`、`versions.json` に `"copilot"`。
2. `env_tool_versions.go` toolSpecs 追加（ピン vs 実体ドリフト表示）。
3. 自己更新 opt-in（`AF_AGENT_SELF_UPDATE`）に copilot 追加（npm 系なので claude/codex と同経路。
   7306de7 の「既存あり no-op」問題は npm には無い）。
4. entrypoint — グローバル AGENTS.md シード不要見込み（プロジェクト root AGENTS.md を読む）。**実測未（残課題）**。
5. e2e-smoke に版ピン検証（バイナリ＋versions.json）を追加済み。

### Track C — control-plane + Console — **実施済（2026-07-21）**

1. CP: `/api/connections` 集約に copilot が載るのみ（専用ルート無し）。
2. Console:
   - `types/session.ts` — union / SESSION_KINDS / ConnectionsStatus.copilot
   - `agents/registry.ts` — descriptor（`managedDriver:true`、`tuiMemoryCost`、**caps 全項目を明示決定**
     （91e209e/2aa00a5 教訓）、色・アイコン・表示順を最初に確定（cbdc4b8 教訓））
   - `AgentsTab.tsx` — CopilotCard（GitHub 連携相乗りの説明＋サブスク注記。設定セクション構造は他カード準拠）
   - i18n ja/en（launch_hint 等）＋ `i18n:lint`
   - `questionKeys.ts` — managed 質問カードは /respond 経路なのでキー列不要（TUI 側の保留カードを出す場合は追加）
3. availability: `connsDone` 分離（30886a1 教訓）、選別は `caps.runsInDir`（57f4bf7 教訓）。

### Track D — 残課題・将来

1. WS バー使用量チップ（AIC 残量。API/取得手段の実測から）
2. アシスタントチャット第5バックエンド（`-p --output-format json` ベース。chat_providers.go）
3. DynamicModel（idle 時の子再起動+session/load 方式）／autopilot モード露出／画像添付（ACP image content block）
4. Copilot cloud agent（`/delegate`・`--resume <TASK-ID>`）連携
5. モデルカタログの動的化（ACP/CLI が列挙口を持った時点で移行）

## agy 教訓反映表（46 コミット全指摘 → copilot での対応）

| agy での指摘（sha） | copilot での対応 |
|---|---|
| resume UUID 取得不能（202e439） | `--session-id` 外部採番で構造的に回避 |
| auth URL の OSC-8 汚染（a1b91c8） | 専用 auth フロー無し（該当なし） |
| tmux kill-server 全滅事故（e07671b） | E2E は `-L` 専用ソケット隔離を本計画の実測でも遵守済。live テストも 3 点隔離 |
| 子プロセス reap 漏れ（422c38c） | 独自 spawn は managed 子のみ・必ず `cmd.Wait()`。他は `agents.Flow` 経由 |
| Turn.Idx 未採番（7354916） | transcript.go で単調採番＋単調増加テスト |
| paneMode 分岐漏れ→初回プロンプト消失（0b0a07f） | フッタ実測パターンで実装（本計画 §Track A-3） |
| 投入経路の監査漏れ（69fde0b） | launch-seed / send_to_session / /turn / 保留中 / paste の 5 経路を実装時に監査 |
| MCP ツール欠落（351fdf6） | mcp_stdio.go / CP mcp.go 総ざらいを Track C チェック項目に |
| turn 終端未検出→報告不発（f3e63f6） | TUI=LiveState（events.jsonl）、managed=runTurn 境界で MarkTurnStart/End |
| caps.imagePaste 放置（91e209e）/ caps 反転漏れ（2aa00a5） | registry caps 全項目を descriptor 実装時に明示決定（表で記録） |
| 権限プロンプト「発生しない前提」崩壊・halt が承認を踏む（df996e4/c639973） | request_permission を防御実装（managed）。TUI GracefulStop は Escape 先行 |
| conns ローディング中の誤露出（30886a1） | connsDone ゲート |
| telemetry 一回きり固定（00dacc5） | trust/設定は起動毎に再固定。remote-export/remote も起動フラグで毎回オフ |
| /usage プラン別形式差（8545feb） | v1 は /usage スクレイプ非採用（events.jsonl の usage_checkpoint / result usage を利用） |
| インストーラ既存あり no-op（7306de7） | npm 導入のため非該当。自己更新は claude/codex 同経路 |
| 焼き込み再配布ライセンス（38e887c） | LICENSE.md 実測: 無改変＋実質機能要件で焼き込み可の読み。パッケージング配布物は agy 同様 boot-install 検討 |
| 質問カードキー列（86900a6/258bf1d） | managed は /respond で構造化（キー列非依存）。TUI 保留カードは v1 非対応（allow-all 運転） |
| チャット分離 HOME（8b16af5） | Track D（チャット統合時に COPILOT_HOME 分離を適用） |

## 実測記録（2026-07-21、v1.0.73 linux-x64、本コンテナ）

- 認証: env 注入なしで gh 透過認証由来の資格情報により `-p` 完走（gpt-5-mini 応答）。
  `gh auth token` は gho_ 40 桁を返す。明示注入（COPILOT_GITHUB_TOKEN）を正式経路とする。
- `-p "..." --output-format json`: JSONL イベントストリーム＋最終 `result`（sessionId/exitCode/usage）。
- ACP probe1: initialize（agentCapabilities: loadSession:true, sessionCapabilities.list）→ session/new →
  session/prompt（chunk streaming）→ session/cancel 即中断。
- ACP probe2: allow-all 無しで `session/request_permission`（allow_once/allow_always/reject_once）→
  allow_once 応答でツール実行続行。`session/load` で別プロセスから履歴リプレイ＋文脈保持。
  `session/list` 動作。configOptions は mode / allow_all のみ（モデル無し）。
- ACP probe3: `session/set_mode`（plan⇄agent）成功。ask_user 指示は構造化されず平文＋end_turn。
- TUI（tmux `-L copilot-probe` 隔離）: 初回フォルダ trust ダイアログ →「2. 記憶」で
  `config.json.trustedFolders[]` に保存。idle フッタ `/ commands · ? help · tab next tab`、
  working フッタ `◎ Working esc interrupt`＋右端 `Auto → gpt-5-mini`、下書き時 `@ files · # issues`。
  ステータス行に `Session: N AIC used`。`--session-id` は v4 検証あり（非 v4 は起動エラー）。
  `/exit` で graceful 終了＋サマリ（Changes/AI Credits/Tokens/Resume コマンド）。
  Enter はテキストと同一 send-keys では確定しない（ペースト折り畳み）→ 別キー送出で確定。
- 状態ファイル: `COPILOT_HOME`（既定 `~/.copilot`）に config.json / mcp-config.json / logs/ /
  session-store.db / `session-state/<sid>/{events.jsonl, session.db, workspace.yaml, checkpoints/, files/, inuse.<pid>.lock}`。
  events.jsonl は TUI/`-p`/ACP 全経路で同形式・ライブ追記。

## 実装後の追加実測（2026-07-21、実装検証時）

- **モデル可否はプラン依存**: 検証アカウントは Copilot Free で、TUI /model に
  「Your Copilot Free plan currently includes only Auto」— 正しい ID でも明示
  `--model` は "Model ... is not available" で失敗する。**Free では既定（auto）で
  起動すること**。/model ピッカーの実測語彙（v1.0.73）: gpt-5.6-sol/terra/luna,
  gpt-5.5, gpt-5.4, gpt-5.4-mini, gpt-5-mini, gpt-5.3-codex, claude-sonnet-5/4.6/4.5,
  claude-haiku-4.5, claude-fable-5, claude-opus-4.8(+fast)/4.7/4.6/4.5,
  gemini-3.1-pro-preview, gemini-3.5-flash, kimi-k2.7-code。
- **モデルカタログはプラン連動のライブ取得へ**（静的リストを廃止）: models.go が
  使い捨て COPILOT_HOME＋トークン明示注入で TUI を PTY 起動し `/model` ピッカーを
  スクレイプ（agents.Flow — agy /usage と同じ基盤・10 分キャッシュ・stale-if-error）。
  Free 系バナー検出 → 空リスト＝Console ピッカーは既定（auto）のみ、バナー無し →
  ピッカー行がそのままそのアカウントで選べるカタログ。実プローブ ~12 秒・
  実セッション一覧は汚さない。契約は fixture（両プラン形）＋ live テスト
  （TestLiveModels）で固定。
- **managed driver の実 CLI 契約テスト**が通過（`live_contract_test.go`,
  `AF_COPILOT_LIVE=1` opt-in）: spawn→initialize→session/new→prompt 完走→
  events.jsonl に応答反映→子 kill→respawn+session/load→文脈保持まで。
  注意: HOME 隔離は gh の ambient 認証も切る — トークンは隔離前に取得して
  `COPILOT_GITHUB_TOKEN` で明示注入する（実運用の managed 子も同様に明示注入）。
- TUI 実測の paneMode/貼り付け/GracefulStop は session_io.go / stop.go に反映済み。

## 残課題（実装済み範囲の外）

- 実フリートのイメージ再ビルド後の実機目視（起動導線・ミラー・managed 切替・
  接続カード・色）。
- ~~rtk~~ → **実装済み**（決定的フック方式・上記 §Track A 1 の rtk.go 参照）。
- ~~WS バー使用量チップ~~ → **実装済み**。当初想定の statusLine セッション消費ではなく、
  内部 API `GET copilot_internal/user`（gh 透過認証トークンで直接叩ける構造化 JSON）から
  **アカウント単位のクレジット残量%＋リセット日＋プラン**を取得（`copilot/usage.go`
  `HandleUsage`・`routes.go` `GET /copilot/usage`・FE `WsBar.tsx` `CopilotUsageChip`）。
  agy と同じ「残量%」型だが**スクレイプ不要**。plan（copilot_plan/access_type_sku）と
  can_upgrade_plan もチップ popover に表示。has_quota=true のプールのみ採用（Free は
  chat/completions、paid は premium_interactions が主）。
- アシスタントチャット headless バックエンド / 画像添付（imagePaste・managed
  Attachments）/ ContextBar（statusLine の `context_window.*` で実現可能・未実装）/
  session 単位の AI クレジット消費表示（statusLine `ai_used`・managed では非描画）— Track D のまま。
- TUI の plan モードチップ（フッタにモード表示が無く検出不能 — Shift+Tab は
  autopilot を跨ぐ 3 モード循環のためキー駆動トグルも封印中）。
