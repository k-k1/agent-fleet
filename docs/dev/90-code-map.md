# 90. コード地図

> ⚠️ 本書は dev/ で唯一、ファイルパス・パッケージ名の列挙を許すファイルであり、**陳腐化しうる**。Go 内部リファクタ（docs/23、別ブランチで進行中・ワイヤ完全互換）がパスを動かす。**パス移動を伴う PR は本書の更新を必須とする**。正はコード。
>
> 正: コード / 主な更新トリガ: ファイル・パッケージの移動 / 最終確認: 2026-07

本書は **main の現状**を基準に書く（grep の起点になるため）。Go リファクタの到達点・目標構造は
各節末の注記に分離してある — 別ブランチ（`refactor/agent-control-plane-refactor`、docs/23 は同ブランチにのみ存在）
をチェックアウトしない限り、注記のパスは手元に存在しない。

## 90.1 トップレベル

| ディレクトリ | 中身 |
|--------------|------|
| `console/` | Console SPA（React+Vite+zustand+xterm.js）。`console/src/` 配下（§90.4）|
| `control-plane/` | CP（Go 単一モジュール・単一バイナリ。egress proxy はサブコマンド）（§90.2）|
| `workspace/` | Workspace イメージ一式: `agent/`（Go）+ Dockerfile/entrypoint（§90.3, §90.5）|
| `deploy/` | デプロイ 3 形態（local / compose / aws）の runbook と定義（§90.6）|
| `e2e/` | フリート E2E（独立 Go モジュール・stdlib のみ、`-tags e2e`）。CP + 実コンテナ疎通（[10 §10.4](10-development.md)）|
| `docs/` | dev/（本体系）・guide/・decisions/（ADR）・HANDOFF・番号付き計画文書（19〜23…）|

## 90.2 control-plane/ — フラットな `package main`

実装 35 + テスト 20 = 55 ファイル。ルート登録（約 180 本）と authGate 配線は `main.go` にインライン。

| ファイル | 責務 |
|----------|------|
| `main.go` | 起動と配線: env/フラグ → Store/Runtime/manager/バックグラウンドジョブ初期化 → 全ルート登録。`writeJSON`/`writeErr` もここ |
| `oauth_google.go` | L1 認証（AUTH=oauth/proxy/dev）・authGate・署名 cookie・`isAuthExempt` |
| `oauth_bitbucket.go` | Bitbucket OAuth ブローカ（Connections 向けトークン取得の CP 側） |
| `pat.go` | PAT（Bearer トークン）発行・ハッシュ・スコープ天井 |
| `tenants.go` | tenant / identity / membership の CRUD・limits・admin API |
| `manager.go` | god オブジェクト: identity/RBAC 解決・workspace lifecycle・runtime キャッシュ・DEK unwrap・env 注入（`workspaceExtraEnv`）・docker exec |
| `runtime.go` | `Runtime`/`RuntimeFactory` ポート + docker 実装 + workspace 系 HTTP ハンドラ（同居） |
| `runtime_ecs.go` | AWS ECS アダプタ 🚧 |
| `ssm.go` | SSM プロファイル/ホスト管理 API（kind=ssm の土台） |
| `proxy.go` | 中継 4 経路のうち 3 つ: `proxyAgentREST` / `proxyAgentStream`(SSE) / `proxyTerminal`(WS リレー) |
| `preview.go` | 4 経路目: preview 中継（→ Agent `/proxy/{port}`） |
| `reaper.go` | 接続追跡（`connRegistry`）とアイドル session/workspace の自動停止 |
| `store.go` / `store_sqlite.go` / `store_sql.go` / `store_postgres.go` | `Store` 抽象（~85 メソッド）と型 / 単一 `sqlStore` 実装 + `migrations/` embed / `?` rebind / pg 方言 + `migrations-pg/` embed |
| `custodian.go` | `KeyCustodian` ポート + `localCustodian`（封筒暗号の KEK） |
| `internal_git.go` / `internal_git_browse.go` | 内部 git 管理 API（作成/削除/rename/branches/quota）/ clone なし tree/blob/commits 閲覧（[91](91-internal-git.md)） |
| `git_http.go` / `git_lfs.go` / `git_lfs_locks.go` / `git_gc.go` | smart-HTTP + HMAC トークン / LFS batch・転送・クォータ / LFS ロック API / gc cron + LFS 孤児 GC |
| `egress.go` / `egress_policy.go` / `egress_proxy.go` | ポリシー API + 監査 ingest / allowlist 判定 / forward proxy（サブコマンド起動） |
| `audit.go` / `claude_audit.go` | admin 監査閲覧 API / Claude transcript から監査イベントを抽出する常駐 sweeper |
| `metrics.go` / `usage.go` | ホスト・コンテナ統計（cgroup 読み）/ 使用時間サンプラ + showback 集計 |
| `memo.go` | メモキュー API（docs/21） |
| `mcp.go` | CP 側 MCP サーバ（`/mcp`） |
| `ws_settings.go` | workspace 毎の設定（agent 自動更新など） |
| `admin_sessions.go` / `admin_stats.go` | admin 横断セッション一覧 / メンバー統計 |

**リファクタ到達点（別ブランチ・main 未マージ）**: パッケージは flat のまま、`main.go` を配線のみに縮小し
`routes.go`（機能別 register 関数 17 + authGate 除外レジストリ）へ分散。`manager.go` を
`resolver.go` / `workspace_lifecycle.go` / `workspace_handlers.go` / `dek.go` / `agent_client.go` に分割、
`runtime.go` をポートのみに縮小し `runtime_docker.go` / `httpapi.go` を分離。`Store` は機能別
サブインターフェース 17 個に再構成。`errcodes.go` / `limits.go` 追加。
**目標（docs/23、未着手分）**: `config`（133 ハンドラ）の機能別ハンドラ struct 化 + `resolvedFor` プリアンブルの
ラッパー化、`internal/runtime` などへのパッケージ層化。

## 90.3 workspace/agent/ — フラットな `package main`

実装 42 + テスト 23 = 65 ファイル + `knowledge/af-usage.md`（`//go:embed`。移動時は
`workspace/.dockerignore` の `!` 復帰と同伴必須）。main には `internal/` は**無い**。

| ファイル | 責務 |
|----------|------|
| `main.go` | 起動・ルート登録（インライン）・`requireToken`・`writeJSON`/`writeErr` |
| `agent.go` | `Agent` インターフェース + kind registry + caps（kind 分岐の集約点） |
| `agent_rtk.go` | rtk（トークン節約 proxy）の on/off を CLI 3 種の成果物（hook / plugin / AGENTS.md）へ反映 |
| `session.go` | セッション中核: メタ永続化・tmux 起動・CLI コマンド構築・ワイヤ変換・HTTP ハンドラ（約 1,200 行の god） |
| `session_io.go` / `session_paste.go` | pane capture・入力送信・slash コマンド / 画像ペーストの保存・配信 |
| `session_status.go` / `session_terminal_state.go` / `session_bg.go` / `session_context.go` | 状態フック（per-sid ファイルストア）/ 端末状態判定 / バックグラウンド busy（/proc 走査）/ コンテキスト使用率 |
| `session_name.go` / `session_title.go` | セッション slug 採番 / 自動タイトル + ブランチ名提案（headless claude） |
| `session_transcript.go` | transcript ウィンドウ API（パーサ 3 本の共通の出口・ページング） |
| `session_ssm.go` | kind=ssm の起動・ログイン状態検出 |
| `chat.go` / `assistants.go` | アシスタントチャット（headless CLI、docs/19。約 970 行）/ アシスタント定義（persona・knowledge） |
| `claude_auth.go` / `claude_settings.go` / `claude_usage.go` | Claude: /login コード貼り戻し / settings.json・trust 管理 / usage 読み取り |
| `codex_auth.go` / `codex_usage.go` / `codex_transcript.go` | codex: 認証 / usage / transcript パーサ |
| `opencode_auth.go` / `opencode_transcript.go` | opencode: 認証 / transcript パーサ |
| `git.go` | 作業コピー操作: clone（`ensureRepo`）・status・checkout・worktree・push 等 |
| `git_view.go` / `fs_git.go` | SCM 閲覧（changes/diff/log/graph）/ エディタ用の行差分マーク |
| `git_remote.go` / `git_oauth.go` / `git_identity.go` | リモート repo/branch 一覧（GitHub/Bitbucket REST）/ GitHub device flow・Bitbucket creds / commit identity |
| `fetch_loop.go` | origin 自動 fetch + FF 可否バッジ |
| `fs.go` | ファイル閲覧・操作 API（`fs/*`） |
| `secrets.go` | 暗号ストア `secrets.enc`（AES-256-GCM）・統一 cred helper・内部 git seed |
| `connections.go` | Connections 状態 API（git ホスト / internal / bitbucket） |
| `mcp_stdio.go` | コンテナ内 stdio MCP サーバ（read-only fleet ツール） |
| `preview.go` / `terminal.go` | `/proxy/{port}` コンテナ内中継 / `/ws/pty`（WebSocket PTY） |
| `env_toolchains.go` / `ui_prefs.go` / `repo_prompts.go` | Java/Node/TZ ツールチェーン解決 / UI プリファレンス / リポの command・skill テンプレ列挙 |
| `shutdown.go` / `uuid.go` | graceful shutdown（作業中セッション考慮）/ ID 生成 |

**リファクタ到達点（別ブランチ・main 未マージ）**: `internal/{httpx,gitx,fstore,transcript}` の 4 パッケージを
切り出し済み（docs/23 の表では `internal/store` だが実装名は `fstore`）。`session.go` は
`session_handlers.go` / `session_meta.go` / `session_program.go` / `session_tmux.go` 等に分割、`agent.go` は
`agent_{claude,codex,opencode,shell_ssm}.go` に CLI 縦割り（ファイルレベル）。`routes.go` / `paths.go` /
`errcodes.go` 追加。
**目標（docs/23 P1 の最終形、未着手分）**: `internal/{httpx,store,gitx,agents/{claude,codex,opencode},transcript,session,chat,title,remote}`。
残りは agents の**パッケージ**化（`internal/session` 抽出が前提）と `chat.go` の分割。

## 90.4 console/src/（設計は [02](02-console.md)。ここでは配置と規模のみ）

| ディレクトリ | 責務（ファイル数） |
|--------------|--------------------|
| `app/` | エントリ・App・TopBar・WsBar・viewport（8） |
| `core/api/` `core/store/` | API クライアント + エラーコード文言 `ERR_TEXT`（1）/ tenant・workspace ストア（2+1） |
| `layout/` | ペインレイアウトの状態・操作・バッジ（6） |
| `features/` | 機能単位の縦割り: `sessions`(13) `settings`(16) `repos`(12) `scm`(8) `viewer`(8) `chat`(7) `memo`(6) `project`(5) `panes`(4) `mirror`(4) `terminal`(4) `files`(2)。各 dir = コンポーネント + store/api/css |
| `agents/` `terminal/` | kind レジストリ（1）/ xterm サービス（2） |
| `ui/` `lib/` `types/` | 汎用 UI 部品（13）/ 汎用ヘルパ（24）/ 手書きワイヤ型（6） |
| `styles/` `assets/` `marp-themes/` | tokens/base CSS（2）/ ファイルアイコン 4 セット（約 120）/ Marp テーマ（1） |

## 90.5 workspace/（agent/ 以外）

| ファイル | 責務 |
|----------|------|
| `Dockerfile` / `jvm.Dockerfile` | Workspace イメージ（multi-stage golang→node:22-slim。CLI 3 種 + rtk + git-lfs 焼き込み）/ JDK 追加バリアント |
| `entrypoint.sh` | コンテナ起動時の seed（settings / AGENTS.md / plugin / notes）と agent 起動 |
| `workspace-notes.md` | 全コンテナに `/etc/claude-code/CLAUDE.md` として配布する運用ポリシー |
| `opencode-plugin/` | `agent-fleet-status.js`（状態バッジ）・`rtk.ts`（bash 書き換え） |
| `tmux.conf` / `.dockerignore` / `vendor/` | tmux 設定 / `**/*.md` 除外 + embed 対象の `!` 復帰（罠）/ rtk 静的バイナリ置き場（git 管理外） |

## 90.6 deploy/

| ディレクトリ | 主要ファイル |
|--------------|--------------|
| `local/` | `run-dev.sh`（dev 一括起動）・`restart-cp.sh`・`e2e-smoke.sh`（イメージスモーク）・`provision-jvm.sh`・`oauth.env.example` |
| `compose/` | `docker-compose.yml`・`Caddyfile`・`release.sh` / `load-images.sh`・`backup.sh` / `restore.sh`・`.env.example`・README（**運用 runbook の正**） |
| `aws/ec2-single/` | `cfn.yaml` + README（単一 EC2 に compose 構成） |
| `aws/ecs/` | `cfn/{00-network,10-data,20-platform,30-ingress}.yaml` + README 🚧 |
