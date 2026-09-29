---
audience: "「あの処理はどのファイルか」を探している人"
source_of_truth: "コード"
updated: "2026-09"
---

# 90. コードマップ

[English](90-code-map.md) | 日本語

ここに書くのは**grep の起点**であって、目録ではない。各サブシステムの設計は担当の章にあり、
本書は「どこから探し始めるか」だけを言う。以下に挙げるファイルとパッケージは例で、どの表も
網羅していない。完全な一覧は常にディレクトリそのもの（`ls`）か、行が名指すコード側の表である。

## 90.1 トップレベル

トップレベルの各ディレクトリの役割は
[10 §10.1](10-development.ja.md#101-リポジトリ構成（責務のみ）)、どれが Go モジュールかは
[10 §10.4](10-development.ja.md#104-テスト) にある。本書はその一段下を扱う。それらの
ディレクトリの外で知っておくとよいものが 2 つある。

| パス | そこで探すもの |
|---|---|
| `.github/workflows/` | CI（`ci.yml`・`docs.yml`・`e2e.yml`）、契約ワークフロー（`*-contract.yml`）、イメージとリリースのワークフロー。それぞれが何を走らせるかは [10 §10.4](10-development.ja.md#104-テスト) |
| `.githooks/pre-commit` | ステージした内容への禁止語スキャン。`ci.yml` の `release-scan` ジョブと同じスキャナ（`deploy/release/scan-forbidden.sh`）を走らせる |

## 90.2 両方の Go モジュールで成り立つ 2 つの規則

**ルートのハンドラ。** どちらのバイナリもルート表を `buildMux`（`routes.go`）で組む。
`buildMux` は一部のルートを自分で登録し、残りはモジュール内の別ファイルにある機能別の関数に任せる
（たとえば CP の `registerEngineRoutes`、agent の `browserx.RegisterRoutes`）。だからメソッドと
パス（たとえば `"GET /api/admin/engines"`）をモジュール全体で grep する。登録している行がハンドラ名を
示し、ハンドラ名がファイルかパッケージを教えてくれる（`sessionx.HandleCreateSession` なら
`internal/sessionx`）。`testdata/routes.golden` は `TestRouteTableGolden` がテスト用の構成で組んだ
ルート表で、モジュールが何を提供しているかを手早く見るには向くが、全ルートではない: CP のエンジン
ゲートウェイのルートはエンジンが設定されているときだけ登録され、golden には 1 つも無い。各ルートの用途は [05](05-api.ja.md)。

**`internal/` 配下のパッケージは `package main` を import しない。** `main` から要るものは
渡してもらう。継ぎ目の広いパッケージはその必要を自分の `deps.go` で宣言し、`main` が起動時に
一度だけ `*_wiring.go` か `*_seam.go` から渡す（たとえば CP の `mcp_wiring.go`・`tenant_wiring.go`・
`runtime_seam.go`、agent の `session_wiring.go`・`browser_seam.go`）。コンストラクタの引数で
受け取るものもある: `internal/auth` はテナント秘密を開く関数を `auth.NewTenantIdPRegistry` で
受け取り、それを呼ぶのは CP の `main.go`。だから呼び出しを追ってこうして注入された関数フィールドや
インタフェースに行き着いたら、`main` 側の配線ファイルかコンストラクタの呼び出し元を見る。この継ぎ目の規則は [decisions/0067](../decisions/0067-parallel-refactor.ja.md)（決定 5）。
バイナリが 2 つある理由と層の分け方は
[decisions/0012](../decisions/0012-go-internal-refactor.ja.md)。

## 90.3 `control-plane/`

配線は `main.go`。`control-plane egress-proxy` で同じバイナリが egress のフォワードプロキシとして
動く。CP の大半はモジュール直下の `package main` で、関心事ごとに名前の接頭辞をそろえたファイル群に
分かれている。切り出せた系統は `internal/` にある。一覧は `ls control-plane/internal` で、
パッケージのファイル冒頭のコメント（と、あれば `deps.go`）が中身を言う。CP のイメージは
`control-plane/Dockerfile` で、リポジトリ直下をコンテキストにしてビルドする（直下の `.dockerignore`）。

| 関心事 | どこから探すか |
|---|---|
| サインイン | `oauth.go`（フローとログインセッション）・`tenant_login.go`（テナントごとのログイン規則）・`oauth_link.go`（2 つ目のサインイン方法の紐づけ）。IdP アダプタとテナント定義 provider のレジストリは `internal/auth` |
| git プロバイダと Jira の OAuth | `oauth_bitbucket.go`・`oauth_github_device.go`・`oauth_jira.go`・`tenant_git_oauth*.go`・`git_oauth_bridge.go`（[08 §8.4.1](08-integrations.ja.md#841-oauth-アプリの持ち主はテナント)）|
| テナント・identity・membership | `resolver.go`・`manager.go`・`pat.go`・`system_tenant.go`。テナントと membership の HTTP 層は `internal/tenantsrv` |
| Workspace のライフサイクル | `workspace_*.go`・`agent_client.go`・`agent_dial.go`。Runtime アダプタ（docker・native・ecs・ecs-ec2）は `internal/runtime`（[03 §3.3](03-control-plane.ja.md#33-manager-と-runtime-抽象)）|
| agent への中継 | `proxy.go`（REST・SSE ストリーム・端末の WebSocket）・`preview*.go`・`browser.go`・`fs_file_proxy.go` |
| Console への push チャネル | `events.go` |
| ストア | `internal/store`: `store.go` がポート、`store_sql.go` が共通 SQL、`store_sqlite.go` と `store_postgres.go` が方言で、それぞれ自分の migrations ディレクトリを embed する（[06 §6.1](06-data.ja.md#61-ストア構成)）|
| 鍵 | `custodian.go`・`dek.go` |
| 内部 git | `internal_git*.go`・`git_http.go`・`git_lfs*.go`・`git_gc.go`（[91](91-internal-git.ja.md)）|
| egress | `egress*.go`（[03 §3.8](03-control-plane.ja.md#38-egress-統制の-cp-側)）|
| agent が `/internal/` 配下で呼ぶ口 | `*_bridge.go`。たとえば docs・メモ・定時実行・AWS プロファイル（[05 §5.2](05-api.ja.md#52-内部面（cp-↔-agent）)）|
| 監査・メトリクス・使用量・費用 | `audit.go`（読む側。書く側は `proxy.go` の `auditActionTarget`）・`claude_audit.go`・`metrics.go`・`usage*.go`・`cloudcost.go`・`cost_*.go` |
| メモ・通知・定時実行 | `memo*.go`・`notification.go`・`schedule*.go`・`scheduler*.go`（[03 §3.6](03-control-plane.ja.md#36-memo-キュー)・[§3.7](03-control-plane.ja.md#37-バックグラウンドジョブ)）|
| MCP | `internal/mcpsrv`（`/mcp` のツールサーバとテナントのサーバ配布）と `mcp_wiring.go`（[03 §3.5](03-control-plane.ja.md#35-mcp-サーバ)）|
| 自前エンジン | `engines.go` と `engine_*.go`。Workspace が呼ぶゲートウェイは `engine_gateway.go`（[03 §3.9](03-control-plane.ja.md#39-自前エンジン)）|
| 音声合成 | `tts*.go`・`enkana*.go` |
| 共有と引き継ぎ | `session_share*.go`・`session_handoff.go` |
| ワークアイテム | `workitems*.go` |
| コンテナ内のガイド | `workspace_docs.go`（ステージ）と `docs_bridge.go`（取得経路）|
| アイドル停止 | `reaper.go`（`connRegistry` を含む）・`session_activity.go`・`idle_forecast.go` |

## 90.4 `workspace/agent/`

起動は `main.go`。`cli.go` がサブコマンドの表（`subcommands`）を持つ: インストーラ、`af-db`、
`aws-exec`、`mcp-stdio`、agent が自分で呼ぶフックとシム。機能の大半は `internal/` にあり、
ルートに残るのは配線ファイル、複数の系統をつなぐ HTTP ハンドラ、そこに置いておける程度に小さい
機能である。パッケージの一覧は `ls workspace/agent/internal` で、パッケージのファイル冒頭の
コメント（と、あれば `deps.go`）が中身を言う。

| 関心事 | どこから探すか |
|---|---|
| セッション: ライフサイクル・tmux・driver 切替・ターン・IO・transcript・タイトル・spawn・peer | `internal/sessionx`。セッションのモデル（ワイヤ・メタデータ・`Kind*` 定数）は `internal/session`、生きた状態のストアは `internal/status` |
| 1 つのエージェント種別 | `internal/agents/<kind>`（エージェント CLI ごとに 1 パッケージ。shell と ssm は `internal/sessionx/agent_shell_ssm.go`）。種別の登録は `agentRegistry`（`internal/sessionx/agent.go`）。共通のインタフェースは `internal/agents`。種別の追加は [20](20-add-an-agent.ja.md) と [04 §4.3](04-agent.ja.md#43-エージェント種別を統合する型) |
| managed 専用の種別（`internal/agents` の `ManagedOnly`）| lcpp: `internal/harness` とその MCP クライアント `internal/mcpc`。muse: `internal/msp`（プロトコル）|
| チャットとアシスタント | `internal/chatx`・`internal/assistants`。組み込みの知識（`knowledge/af-usage.md`）を embed するのは `assistants.go` |
| チャットブリッジ | `internal/bridge`・`bridge_operator.go`・`connections_slack.go` |
| ブラウザペイン | `internal/browserx`（[04 §4.10](04-agent.ja.md#410-browsermanager)）|
| エージェントメモリ | `internal/memoryx` |
| 使用量台帳 | `internal/usagex`・`usage_*.go` |
| git とファイルシステム | `internal/gitx`・`fs*.go`・`fetch_loop.go`・`cred_helper.go`・`connections.go`・`repo_jobs.go`・`worktree_*.go`・`svn*.go` |
| MCP | `internal/mcpx`（コンテナ内の `af` サーバ `workspace-agent mcp-stdio` と、レジストリの REST 面）・`internal/mcpreg`（レジストリと CLI ごとの設定ファイル書き出し）・`internal/mcpproj` と `internal/projcfg`（作業コピー自身の project スコープのファイル）|
| 指示とスキル | `agent_instructions.go`・`internal/userinstr`・`internal/mdblock`・`internal/fleetskills` |
| ツールチェーンとインストーラ | `env_*.go`・`jdk*.go`・`node_install.go`・`install_*.go`・`*_install_http.go`・`internal/afdb`（`af-db`）|
| AWS | `internal/awsx`（`aws-exec`）・`ssm_instances.go` |
| 画像生成 | `internal/imagegen` |
| 自前エンジン | `engines.go` |
| フリート俯瞰図 | `internal/fleetgraph` |
| ワークアイテム | `workitems*.go`・`connections_jira.go` |
| 秘密情報 | `internal/secrets`（[04 §4.8](04-agent.ja.md#48-秘密情報（agent-の責務）)）|
| 端末と preview | `terminal*.go`・`preview.go` |
| 後片付け | `cleanup_*.go`・`leftovers.go`・`tool_caches.go`・`cli_version_prune.go` |

ほかにも独自の機能を持つパッケージがあり、たとえば `branchrule`（ブランチ名の決定）・`uiprefs`
（保存された UI 設定）・`statemig`（agent の状態の一度きりの移動）。小さな共有ヘルパーには
`httpx`・`paths`・`fstore`・`pathguard`・`filemeta`・`tmuxx`・`transcript` がある。`wiretest` と
`ingresstest` はテストからしか import されない。

## 90.5 `console/src/`

ディレクトリの地図は [02](02-console.ja.md) の §2.2、その周りの設計は同じ章の残りにある。
機能ディレクトリの一覧は `ls console/src/features`。入り口の探し方:

- **バックエンドへのリクエスト**: パスを `console/src/` で grep する。`fetch` を包むのは
  `core/api/client.ts` で、いくつかの機能は自分の呼び出しを専用の `api.ts` にまとめている。
- **画面の文言**: 文字列を `lib/i18n/locales/` で grep し、見つかったキーを `.tsx` で探す。
- **エージェント種別ごとに違うもの**: `agents/registry.ts`。
- **実ブラウザを使う検査**（`pdf:check`・`doc:check`・スクリーンショット・スクロールの検査）:
  `console/package.json` の scripts で、実体は `console/scripts/` にある。

## 90.6 `workspace/`（agent 以外）

イメージの作り方と、何を焼き何を必要時に入れるかは
[04 §4.9](04-agent.ja.md#49-workspace-イメージと-entrypoint)。

| ファイル | 何か |
|---|---|
| `Dockerfile` | Workspace イメージ。`workspace/` をコンテキストにしてビルドする |
| `entrypoint.sh` | 起動時の seed のあと、イメージのコマンド `workspace-agent` を `exec` する。ガイドの配布はこの仕事ではない: マウントされるか、agent が取りに行く（`docs_sync.go`）|
| `workspace-notes.md` | 全コンテナが受け取る運用ポリシーの、常時読み込まれる短い部分（禁止事項と罠）|
| `notes/` | そのトピックファイル（イメージでは `/usr/local/share/agent-fleet/notes/`）。ポリシーの索引が指す手順 |
| `af-db.sh`・`af-aws-exec.sh`・`af-scratch.sh`・`af-arch-repair.sh` | `PATH` 上の `af-*` コマンド。最初の 2 つは `workspace-agent` を exec するだけ |
| `gh-auth-wrapper.sh`・`svn-auth-wrapper.sh` | 本物より前に `gh` と `svn` として置かれ、どちらも Console で保存した資格情報を使えるようにする |
| `jvm.Dockerfile` | 共有 JDK ディレクトリを作る（`deploy/local/provision-jvm.sh`）|
| `opencode-plugin/`・`tmux.conf` | opencode のプラグインと tmux の設定 |
| `.dockerignore` | ⚠️ `**/*.md` を除外したうえで、ビルドに要るものを戻している。**イメージや agent バイナリが embed する markdown を足す前に読むこと** |

## 90.7 `deploy/`

ディレクトリの地図は [deploy/README.md](../../deploy/README.md)、各デプロイ形態の runbook は
それぞれのディレクトリの README（[09](09-deploy.ja.md)）。中での探し方:

| 探すもの | どこから探すか |
|---|---|
| 開発用の起動スクリプト | `deploy/local/`（[10](10-development.ja.md) の §10.3）|
| AWS のテンプレート | `deploy/aws/ecs/cfn/`。層ごとに番号付きのテンプレートが 1 つずつあり、パラメータは `cfn/PARAMETERS.md` |
| このリポジトリがビルドするエンジンイメージ | `deploy/aws/ecs/comfyui/` と `deploy/aws/ecs/engine-tools/` |
| 実 AWS に対するプローブとベンチマーク | `deploy/aws/ecs/harness/` |
| リリース成果物のビルド | `deploy/release/build.sh`（唯一の入り口）。native パッケージのビルダーは `deploy/release/native/` |
| 公開配布リポジトリとリリースノート | `deploy/release/dist-repo/` がそのリポジトリの種、`deploy/release/notes/` が版ごとのリリースノート（英語と日本語）|
| 禁止語スキャナ | `deploy/release/scan-forbidden.sh` とその Go モジュール `deploy/release/scan/` |
