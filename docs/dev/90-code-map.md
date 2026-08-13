# 90. コードマップ

> ⚠️ 本書は dev/ で唯一、ファイルパス・パッケージ名の列挙を許すファイルであり、**陳腐化しうる**。
> **パス移動を伴う PR は本書の更新を必須とする**。正はコード。
>
> 正: コード / 主な更新トリガ: ファイル・パッケージの移動 / 最終確認: 2026-07

本書は**現在のツリー**を基準に、grep の起点だけを記す。Go リファクタの履歴と残作業は
[docs/23](../23-go-refactor.md) に分離する。

## 90.1 トップレベル

| ディレクトリ | 中身 |
|--------------|------|
| `console/` | Console SPA（React+Vite+zustand+xterm.js）。`console/src/` 配下（§90.4）|
| `control-plane/` | CP（Go 単一モジュール・単一バイナリ。egress proxy はサブコマンド）（§90.2）|
| `workspace/` | Workspace イメージ一式: `agent/`（Go）+ Dockerfile/entrypoint（§90.3, §90.5）|
| `deploy/` | デプロイ 3 形態（local / compose / aws）の runbook と定義（§90.6）|
| `e2e/` | フリート E2E（独立 Go モジュール・stdlib のみ、`-tags e2e`）。CP + 実コンテナ疎通（L2）+ 実 API スモーク（L4 `live_test.go`）（[10 §10.4](10-development.md)）|
| `console-e2e/` | Console UI E2E（Playwright、L3）。global-setup が CP/コンテナを起動、ブラウザ打鍵 → fs API で観測 |
| `docs/` | dev/（本体系）・guide/（利用者/管理者ガイド）・decisions/（ADR）・history/（旧作業ログ 19〜22 ほか）・HANDOFF・番号付き計画文書（root に 20〜30…）|

## 90.2 control-plane/ — 責務別ファイルに分割した `package main`

ルートは `routes.go` の機能別 register 関数へ集約し、`main.go` は初期化と配線を担う。

| ファイル | 責務 |
|----------|------|
| `main.go` / `routes.go` / `httpapi.go` | 起動と配線 / 機能別ルート登録 / HTTP 共通処理 |
| `oauth_google.go` | L1 認証（AUTH=oauth/proxy/dev）・authGate・署名 cookie・`isAuthExempt` |
| `oauth_bitbucket.go` | Bitbucket OAuth ブローカ（Connections 向けトークン取得の CP 側） |
| `pat.go` | PAT（Bearer トークン）発行・ハッシュ・スコープ天井 |
| `tenants.go` | tenant / identity / membership の CRUD・limits・admin API |
| `manager.go` / `resolver.go` | 依存の保持 / identity・membership・RBAC 解決 |
| `workspace_lifecycle.go` / `workspace_handlers.go` / `agent_client.go` / `dek.go` | Workspace lifecycle / HTTP / Agent 通信 / DEK unwrap・env 注入 |
| `runtime.go` / `runtime_docker.go` | `Runtime`/`RuntimeFactory` ポート / Docker 実装 |
| `runtime_ecs.go` | AWS ECS アダプタ 🚧 |
| `runtime_native.go` | ネイティブ（コンテナレス）Runtime アダプタ（Docker の無い素の WSL2 等、docs/34） |
| `ssm.go` | SSM プロファイル/ホスト管理 API（kind=ssm の土台） |
| `proxy.go` | 中継 5 経路（[05 §5.3](05-api-contracts.md)）のうち 3 つ: `proxyAgentREST` / `proxyAgentStream`(SSE) / `proxyTerminal`(WS リレー) |
| `preview.go` | preview 中継（→ Agent `/proxy/{port}`） |
| `browser.go` | ブラウザペイン中継（`/api/browser/pages*`・`/ws/browser` → Agent） |
| `events.go` | Console 向け統合 push（`GET /api/events` SSE。常時ポーリング数本を 1 コネクションへ集約） |
| `reaper.go` | 接続追跡（`connRegistry`）とアイドル session/workspace の自動停止 |
| `store.go` / `store_sqlite.go` / `store_sql.go` / `store_postgres.go` | `Store` 抽象（~85 メソッド）と型 / 単一 `sqlStore` 実装 + `migrations/` embed / `?` rebind / pg 方言 + `migrations-pg/` embed |
| `custodian.go` | `KeyCustodian` ポート + `localCustodian`（封筒暗号の KEK） |
| `internal_git.go` / `internal_git_browse.go` | 内部 git 管理 API（作成/削除/rename/branches/quota）/ clone なし tree/blob/commits 閲覧（[91](91-internal-git.md)） |
| `git_http.go` / `git_lfs.go` / `git_lfs_locks.go` / `git_gc.go` | smart-HTTP + HMAC トークン / LFS batch・転送・クォータ / LFS ロック API / gc cron + LFS 孤児 GC |
| `egress.go` / `egress_policy.go` / `egress_proxy.go` | ポリシー API + 監査 ingest / allowlist 判定 / forward proxy（サブコマンド起動） |
| `audit.go` / `claude_audit.go` | admin 監査閲覧 API / Claude transcript から監査イベントを抽出する常駐 sweeper |
| `metrics.go` / `usage.go` | ホスト・コンテナ統計（cgroup 読み）/ 使用時間サンプラ + showback 集計 |
| `memo.go` / `memo_bridge.go` | メモキュー API / セッションへの配送 |
| `notification.go` | 通知センター API と永続化 |
| `schedule.go` / `schedule_bridge.go` / `schedule_delete_guard.go` / `scheduler.go` / `scheduler_{assistant,reuse,wake}.go` | 定時実行（docs/38）: API / コンテナ内オペレーター向け内部ブリッジ / 参照中リソースの削除ガード / 発火 goroutine / アシスタント発火・セッション reuse・wake |
| `mcp_era.go` / `mcp_server.go` / `mcp_server_bridge.go` | MCP 新版（2026-07-28 ステートレス）の受理層（docs/49）/ テナント配布 MCP レジストリの admin CRUD と Workspace 向け内部ブリッジ（docs/48） |
| `mem.go` / `limits.go` | Workspace RAM 上限の bytes 正規化・runtime 別整形 / テナント limits JSON パースとアイドルタイムアウト解決 |
| `tts.go` / `tts_{polly,ecs}.go` / `enkana*.go` | 読み上げの共通面 / provider / 日本語読み正規化 |
| `workspace_docs.go` | ロール別 docs を Workspace へ read-only ステージ |
| `mcp.go` | CP 側 MCP サーバ（`/mcp`） |
| `ws_settings.go` | workspace 毎の設定（agent 自動更新など） |
| `admin_sessions.go` / `admin_stats.go` | admin 横断セッション一覧 / メンバー統計 |

## 90.3 workspace/agent/ — HTTP 配線層 + `internal/` ドメイン層

ルート直下の `package main` は HTTP・Console ワイヤ・サブシステム配線を担う。共有モデルと
エージェント固有実装は `internal/` に分離済み。

| ファイル | 責務 |
|----------|------|
| `main.go` / `routes.go` | 起動・依存配線 / ルート登録・認証 |
| `agent.go` / `agent_models.go` / `agent_shell_ssm.go` | Agent registry の main 側アダプタ / model catalog / shell・SSM |
| `agent_rtk.go` | rtk（トークン節約 proxy）の on/off を CLI 4 種の成果物（hook / plugin / AGENTS.md）へ反映 |
| `agent_instructions.go` | ユーザー指示（docs/60）の配布器と REST。フリート方針＋ユーザー指示＋rtk を各 CLI の user スコープへ 1 人の書き手として配る |
| `session_handlers.go` / `session_tmux.go` / `session_driver.go` | lifecycle とワイヤ変換 / tui 起動 / driver 切替 |
| `session_turn.go` | driver 非依存の `/turn`・`/respond`・`/settings` 意味論 API |
| `session_io.go` / `session_paste.go` | pane capture・入力送信・slash コマンド / 画像ペーストの保存・配信 |
| `session_status.go` / `session_terminal_state.go` / `session_injections.go` | 状態フック / 端末状態判定 / prompt 注入 |
| `session_name.go` / `session_title.go` | セッション slug 採番 / 自動タイトル + ブランチ名提案（headless claude） |
| `session_transcript.go` | transcript ウィンドウ API（パーサ 3 本の共通の出口・ページング） |
| `session_ssm.go` | kind=ssm の起動・ログイン状態検出 |
| `chat.go` / `chat_handlers.go` / `chat_providers.go` / `chat_store.go` | アシスタントチャット（headless CLI、docs/19）: コア面 / HTTP ハンドラ / プロバイダ実装（claude・codex CLI 駆動）/ 会話永続化。`chat.go` からの機械的分割（docs/23 残②）|
| `chat_report.go` / `assistants.go` | セッション完了報告→フリートオペレーター（docs/30）/ アシスタント定義（persona・model・knowledge・tools） |
| `bridge_{inbound,answer,operator,approval}.go` | チャットブリッジ（docs/37）の main 側: Discord/Slack 受信のセッション/オペレーター会話への注入 / ボタン押下の構造的回答（AUQ・plan・permission）/ 破壊的操作の承認ゲート |
| `browser_{manager,cdp,handlers,types,smoke}.go` | BrowserManager（[04 §4.10](04-workspace-agent.md)）: Chromium の pipe CDP 駆動・WS 配信・ハンドラ |
| `memory_*.go`（handlers / repo / snapshot / restore / roots / trigger / secrets / export / import の約 9 本） | エージェントメモリ版管理（docs/39）: bare repo・snapshot/restore・毎分トリガ・secret スキャン・bundle 移送 |
| `usage_*.go`（ledger / fold / rollup / series / tag / dedup の 6 本） | 機能別トークン台帳（docs/46）: 記帳・セッションターン折り込み・rollup・時系列 API・重複排除 |
| `cleanup_{archive,ops}.go` | 掃除の gz 安全網アーカイブと bundle-then-remove の実操作（docs/32） |
| `locks.go` / `svn.go` | 削除ロックの作業コピー側レジストリ（docs/45）/ SVN チェックアウト（docs/41） |
| `session_skills.go` | ミラーのスキルピッカー向けセッション別スキル列挙（docs/50） |
| `codex_appserver.go` | Codex app-server の起動と read-only observer（managed writer は internal 側） |
| `git.go` | 作業コピー操作: clone（`ensureRepo`）・status・checkout・worktree・push 等 |
| `git_view.go` / `fs_git.go` | SCM 閲覧（changes/diff/log/graph）/ エディタ用の行差分マーク |
| `git_remote.go` / `git_oauth.go` / `git_identity.go` | リモート repo/branch 一覧（GitHub/Bitbucket REST）/ GitHub device flow・Bitbucket creds / commit identity |
| `fetch_loop.go` | origin 自動 fetch + FF 可否バッジ |
| `fs.go` | ファイル閲覧・操作 API（`fs/*`） |
| `cred_helper.go` | 暗号ストアを使う git credential helper の CLI 受け口 |
| `connections.go` | Connections 状態 API（git ホスト / internal / bitbucket） |
| `mcp_stdio.go` / `mcp_run.go` | コンテナ内 stdio MCP サーバ / MCP プロセス実行 |
| `mcp_servers.go` / `mcp_materialize.go` / `mcp_tenant.go` | MCP レジストリの REST 面 / CLI 設定への materialize 契機 / テナント配布の取得契機（docs/48。実装本体は `internal/mcpreg`） |
| `preview.go` / `terminal.go` | `/proxy/{port}` コンテナ内中継 / `/ws/pty`（WebSocket PTY） |
| `env_toolchains.go` / `env_tool_versions.go` / `ui_prefs.go` / `repo_prompts.go` | Java/Node/TZ ツールチェーン解決 / バンドルツール版レポート（実効・焼き込み・~/.local・ピン）/ UI プリファレンス / リポの command・skill テンプレ列挙 |
| `shutdown.go` / `record_exit.go` | graceful shutdown / tui pane の終了理由記録 |

| internal package | 責務 |
|------------------|------|
| `internal/session` | Session wire/meta、kind・driver、ID、永続化 |
| `internal/agents` | read IF、managed Driver/ThreadHandle、通知 seam、message ledger |
| `internal/agents/claude` | Claude の起動・auth・settings・hooks・transcript・usage・BG 判定 |
| `internal/agents/codex` | Codex read 層、app-server managed Driver/RuntimeSupervisor、auth・models |
| `internal/agents/opencode` | OpenCode read 層、serve managed Driver/RuntimeSupervisor、auth・models |
| `internal/agents/{copilot,cursor,kiro,agy}` | 後発 kind の各実装（copilot / cursor / kiro=Terminal+Managed(ACP)、agy=Terminal 専用） |
| `internal/bridge` | チャットブリッジ配送層（Discord / Slack Gateway・承認・整形、docs/37） |
| `internal/mcpreg` | MCP レジストリ本体（定義・builtin・CLI 別 materialize・テナント配布、docs/48） |
| `internal/hostcaps` | ホスト CPU / 実行環境の能力検知（動かせない kind を Console セレクタから隠す capability ガード） |
| `internal/userinstr` | ユーザー指示の正本（`~/.config/agent-fleet/user-notes.md`）と本文組み立て。配り方は各 kind 側（docs/60） |
| `internal/mdblock` | AF が所有する markdown ブロック（`<!-- agent-fleet:… -->`）の合成・除去・旧 `cp -f` 版からの移行 |
| `internal/{status,tmuxx,transcript}` | 共通状態ストア / tmux exact 操作・probe / transcript wire |
| `internal/{httpx,gitx,fstore,paths,secrets,notice}` | HTTP / git / file store / path / 暗号ストア / 通知補助 |

## 90.4 console/src/（設計は [02](02-console.md)。ここでは配置と規模のみ）

| ディレクトリ | 責務（ファイル数は概数——増え続けるので `find console/src/features -type f` 等で再実測を） |
|--------------|--------------------|
| `app/` | エントリ・App・TopBar・WsBar・viewport（約 10） |
| `core/api/` `core/store/` | API クライアント + エラーコード文言 `ERR_TEXT`（1）/ tenant・workspace 等のストア（約 7） |
| `layout/` | ペインレイアウトの状態・操作・バッジ（約 10） |
| `features/` | 機能単位の縦割り（19 dir）: `settings`(約35) `viewer`(約25) `editor`(約25) `mirror`(約20) `repos`(約20) `sessions`(約18) `chat`(約18) `project`(約17) `keys`(約12) `browser`(約11) `memo`(約10) `scm`(約9) `panes`(約9) `usage`(約8) `schedules`(約6) `notifications`(約4) `terminal`(約4) `files`(2) `auth`(1)。各 dir = コンポーネント + store/api/css |
| `agents/` `terminal/` | kind レジストリ（2）/ xterm サービス（2） |
| `ui/` `lib/` `types/` | 汎用 UI 部品（約 17）/ 汎用ヘルパ（約 60）/ 手書きワイヤ型（約 7） |
| `styles/` `assets/` `marp-themes/` | tokens/base CSS（2）/ ファイルアイコン 4 セット（約 120）/ Marp テーマ（1） |

## 90.5 workspace/（agent/ 以外）

| ファイル | 責務 |
|----------|------|
| `Dockerfile` / `jvm.Dockerfile` | Workspace イメージ（multi-stage golang→node:22-slim。既定 `BAKE_AGENT_CLIS=0`=エージェント CLI を焼かない lean、[04 §4.9](04-workspace-agent.md)）/ JDK 追加バリアント |
| `entrypoint.sh` | コンテナ起動時の seed（settings / plugin / CLI ピン止め）と agent 起動。**利用ガイドの配布は agent 側**（`agent_instructions.go`） |
| `workspace-notes.md` | 全コンテナへ配る運用ポリシー（claude=`/etc/claude-code/CLAUDE.md` に焼込 / codex・opencode=agent が AGENTS.md へ合成） |
| `opencode-plugin/` | `agent-fleet-status.js`（状態バッジ）・`rtk.ts`（bash 書き換え） |
| `tmux.conf` / `.dockerignore` / `vendor/` | tmux 設定 / `**/*.md` 除外 + embed 対象の `!` 復帰（罠）/ rtk 静的バイナリ置き場（git 管理外） |

## 90.6 deploy/

| ディレクトリ | 主要ファイル |
|--------------|--------------|
| `local/` | `run-dev.sh`（dev 一括起動）・`restart-cp.sh`・`e2e-smoke.sh`（イメージスモーク）・`provision-jvm.sh`・`oauth.env.example` |
| `compose/` | `docker-compose.yml`・`Caddyfile`・`release.sh` / `load-images.sh`・`backup.sh` / `restore.sh`・`.env.example`・README（**運用 runbook の正**） |
| `aws/ec2-single/` | `cfn.yaml` + README（単一 EC2 に compose 構成） |
| `aws/ecs/` | `cfn/{00-network,10-data,20-platform,30-ingress}.yaml` + README 🚧 |
