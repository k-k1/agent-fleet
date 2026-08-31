---
audience: "「あの処理はどのファイルか」を探している人"
source_of_truth: "コード"
updated: "2026-07"
---

# 90. コードマップ

[English](90-code-map.md) | 日本語

ここに書くのは**grep の起点**であって、目録ではない。各サブシステムの設計は担当の章にあり、
本書は「どこから探し始めるか」だけを言う。

## 90.1 トップレベル

| ディレクトリ | 中身 |
|---|---|
| `console/` | Console の SPA（§90.4、設計は [02](02-console.ja.md)）|
| `control-plane/` | CP。Go 1 モジュール 1 バイナリ（egress proxy はサブコマンド）（§90.2）|
| `workspace/` | Workspace イメージ一式: `agent/` ＋ Dockerfile / entrypoint（§90.3, §90.5）|
| `deploy/` | 形態ごとの runbook と定義 |
| `e2e/` `console-e2e/` | フリート E2E と UI E2E（[10 §10.4](10-development.ja.md)）|
| `docs/` | 読者で切った棚（[CONVENTIONS](../CONVENTIONS.ja.md)）|

## 90.2 `control-plane/` — 責務で分けた 1 つの `package main`

配線は `main.go`、ルート登録は機能別に `routes.go`。まずそこから。

| 関心事 | ファイル |
|---|---|
| ログインと authGate | `oauth*.go` — 汎用 OIDC クライアント、GitHub アダプタ（OIDC ではないので専用）、テナントのログイン規則、テナント定義 provider とその実行時レジストリ |
| git プロバイダの OAuth | `oauth_bitbucket.go` / `oauth_github_device.go` / `git_oauth_bridge.go` / `tenant_git_oauth*.go` — **いまは全部 CP 側**（[08 §8.4.1](08-integrations.ja.md)）|
| テナント・identity・membership | `tenants.go` / `resolver.go` / `manager.go` / `pat.go` |
| Workspace のライフサイクル | `workspace_*.go` / `agent_client.go` / `dek.go` |
| Runtime アダプタ | `runtime.go` ＋ `runtime_{docker,ecs,ecs_ec2,native}.go` |
| 中継 | `proxy.go`（REST / SSE / 端末）・`preview.go`・`browser.go`・`events.go` |
| ストア | `store.go` と `store_{sqlite,sql,postgres}.go`（両系列の migrations を embed）|
| 暗号 | `custodian.go` |
| 内部 git | `internal_git*.go` / `git_http.go` / `git_lfs*.go` / `git_gc.go`（[91](91-internal-git.ja.md)）|
| egress | `egress*.go` |
| 監査・メトリクス・使用量 | `audit.go` / `claude_audit.go` / `metrics.go` / `usage.go` |
| メモ・通知・定時実行 | `memo*.go` / `notification.go` / `schedule*.go` / `scheduler*.go` |
| MCP | `mcp.go` / `mcp_era.go` / `mcp_server*.go` |
| ロール別 docs | `workspace_docs.go`（ステージ）と `docs_bridge.go`（取得経路）|
| アイドル停止 | `reaper.go`（接続追跡を含む）|

## 90.3 `workspace/agent/` — HTTP 配線層 ＋ `internal/` ドメイン層

ルートの package は HTTP・Console ワイヤ・サブシステム配線を持つ。共有モデルと
kind 固有実装は `internal/` にある。

| 関心事 | ファイル |
|---|---|
| 起動とルート | `main.go` / `routes.go` |
| セッション | `session_*.go` — lifecycle・tmux・driver 切替・driver 非依存の turn API・IO・状態・transcript・タイトル |
| チャットとアシスタント | `chat*.go` / `assistants.go` / `chat_report.go` |
| チャットブリッジ | `bridge_*.go` |
| ブラウザペイン | `browser_*.go`（[04 §4.10](04-agent.ja.md)）|
| エージェントメモリ | `memory_*.go` |
| 使用量台帳 | `usage_*.go` |
| git とファイル | `git*.go` / `fs*.go` / `fetch_loop.go` / `cred_helper.go` / `connections.go` |
| MCP | `mcp_*.go`（実装本体は `internal/mcpreg`）|
| 端末と preview | `terminal.go` / `preview.go` |
| 指示とツールチェーン | `agent_instructions.go` / `agent_rtk.go` / `env_*.go` / `jdk_install_http.go` |

| `internal/` パッケージ | 責務 |
|---|---|
| `session` | Session の wire / meta、kind・driver、ID、永続化 |
| `agents` | read インタフェース、managed Driver と ThreadHandle、通知 seam |
| `agents/<kind>` | kind ごとに 1 パッケージ。**種別を足すときはこの型をなぞる**（[20](20-add-an-agent.ja.md)）|
| `bridge` | チャットブリッジの配送層 |
| `mcpreg` | MCP レジストリ本体（CLI 別 materialize を含む）|
| `hostcaps` | ホストの実行能力の検知（動かせない kind は提示せず隠す）|
| `userinstr` / `mdblock` | ユーザー指示の正本と、共有ファイル内で AF が所有する markdown ブロック |
| `status` / `tmuxx` / `transcript` | 共通状態ストア、tmux の exact 操作と probe、transcript の wire |
| `httpx` / `gitx` / `fstore` / `paths` / `secrets` / `notice` | 小さな共有ユーティリティ |

## 90.4 `console/src/`

設計は [02](02-console.ja.md)。ここは配置だけ。`features/` に機能単位の 19 ディレクトリが
あり、各々がコンポーネント・ストア・API スライス・CSS を持つ。ほかは `app/`・
`core/{api,store}/`・`layout/`・`terminal/`・`agents/`・`ui/`・`lib/`・`types/`・`styles/`。

**ファイル数はすぐ古くなる**——ここの数字を信じず `find console/src/features -type f` で測ること。

## 90.5 `workspace/`（agent/ 以外）

| ファイル | 責務 |
|---|---|
| `Dockerfile` | Workspace イメージ。**配布の既定はエージェント CLI を焼かない**（[04 §4.9](04-agent.ja.md)）|
| `entrypoint.sh` | 起動時の seed と agent 起動。**利用ガイドの配布は agent 側の仕事**で、entrypoint ではない |
| `workspace-notes.md` | 全コンテナへ配る運用ポリシー |
| `opencode-plugin/` / `tmux.conf` / `vendor/` | プラグイン、tmux 設定、静的バイナリの置き場 |
| `.dockerignore` | ⚠️ `**/*.md` を除外したうえで embed 対象を `!` で戻している——**編集前に読むべき罠** |
