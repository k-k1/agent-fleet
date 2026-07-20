# 09. デプロイ — 3形態・ポート&アダプタ・env 索引

> 正: コード + `deploy/*/README.md`（runbook）/ 主な更新トリガ: デプロイ形態・env・アダプタの追加 / 最終確認: 2026-07

実手順（コマンド）は各 runbook が正で、本書は複製しない。本書は「どの形態があり、何が差し替わり、
どのノブで制御するか」の地図。

## 9.1 デプロイ3形態

| 形態 | 概要 | 状態 | runbook |
|------|------|------|---------|
| **local dev** | CP をホストプロセスで起動（`run-dev.sh` 一括 / `restart-cp.sh` 軽量反映）。`AUTH=dev`（単独）または `oauth`（共有）。run-dev.sh はサブコマンド式の単一エントリ（`local`/`wsl`/`native`/`reset`＝データ初期化） | ✅ 開発 + 小規模共有で運用中 | スクリプト冒頭コメント（[run-dev.sh](../../deploy/local/run-dev.sh) / [restart-cp.sh](../../deploy/local/restart-cp.sh)）。反映作法は [10](10-development.md) |
| **wsl（個人）** | local dev の WSL2 むけ即起動プリセット（native dockerd 前提・`AUTH=dev` 固定・rtk をイメージ同梱・JDK は bind-mount か on-demand）。Docker を入れられない場合は `run-dev.sh native`（コンテナレス 🚧・[34](../34-native-runtime.md)） | ✅ 個人検証 | [../../deploy/local/README-wsl.md](../../deploy/local/README-wsl.md)（`run-dev.sh wsl`。旧 `wsl-quickstart.sh` はラッパー） |
| **compose** | セルフホスト本命。CP コンテナ + Caddy（ACME 自動 TLS）。CP は loopback bind、DooD（ホストのデーモンを駆動）の3制約（host-net / `DATA_DIR` 同一絶対パス / docker gid）を compose 定義が封じ込める | ✅ | [../../deploy/compose/README.md](../../deploy/compose/README.md) |
| **aws** | ネイティブ ECS アダプタ（CFN 4段）と、compose を単一 EC2 VM に載せる ec2-single の 2 通り | 🚧 実装済・実運用実績なし | [ecs](../../deploy/aws/ecs/README.md) / [ec2-single](../../deploy/aws/ec2-single/README.md) |

- ec2-single は「AWS 上の compose」＝形態としては compose の変種（P3-10 の完成ゲート
  「clean host でリリースバンドルから起動」を実証済み）。
- 認証モード（dev / oauth / proxy）の中身は [07 §7.3](07-security.md) が正。ここでは繰り返さない。

## 9.2 ポート&アダプタ — 何をどのノブで差し替えるか

**コア（Console / CP コアロジック / Agent / Workspace イメージ）は全ターゲット同一物**で、
差し替わるのは CP 内の interface seam のみ（seam の一覧と local/aws 対応は [01 §1.6](01-architecture.md)）。
本節はその選択ノブ側:

| ポート（seam） | 切替ノブ | 選択肢 |
|---------------|----------|--------|
| `Runtime` / `RuntimeFactory` | `AF_RUNTIME` | 空・`local`・`docker` = Docker Engine（既定）/ `ecs`・`aws` = ECS 🚧 / `native`・`wsl` = コンテナレス（ホストプロセス・`AUTH=dev` 必須、[34](../34-native-runtime.md)）。未知値は起動時 fail-fast |
| `Store` | `AF_DB`（SQLite パス）/ `AF_DATABASE_URL` ほか `AF_DB_*` | SQLite（既定・pure-Go）/ Postgres |
| `KeyCustodian` | `AF_MASTER_KEY` の有無 | 設定時 = localCustodian / 未設定 = 暗号化なし（dev のみ）。KMS/Vault は 📋 seam のみ（[decisions/0005](../decisions/0005-envelope-custodian.md)）|
| `AuthGateway` | `AUTH` | `dev` / `oauth` / `proxy`（[07 §7.3](07-security.md)）|
| Ingress / TLS | （CP 外・形態で決まる）| Caddy（compose）/ Tailscale Funnel（local 運用）/ ALB+ACM（aws）|

## 9.3 入口（ingress）の選択肢と loopback 不変条件

**不変条件: CP は loopback（実運用は `CP_ADDR=127.0.0.1:8099`）に bind し、外部公開は常に入口の背後。**
入口の仕事は TLS 終端と転送のみ（`AUTH=oauth` では認証も CP 自身が担い、`AUTH=proxy` のときだけ
入口側が email ヘッダを注入する）。

| 入口 | 使いどころ | 備考 |
|------|-----------|------|
| **Caddy** | compose 標準 | `PUBLIC_DOMAIN` の DNS を向けるだけで Let's Encrypt 自動取得・更新（WS も透過）。CP と両方 host-net で loopback に到達。既存プロキシで前段する社は外せる（Caddyfile 代替2）|
| **Tailscale Funnel** | local 運用の一形態 | Funnel → `127.0.0.1:8099` 直結。ホスト固有の手順は HANDOFF の領分 |
| **ALB + ACM** | aws 🚧 | TLS 終端のみ（認証は CP ネイティブ oauth）。ALB OIDC を使う場合は `AUTH=proxy` |

入口を変えたら `PUBLIC_BASE_URL`（外部 https URL）を必ず合わせる — OAuth redirect_uri の素であり、
https 前置きが Secure cookie の前提。

## 9.4 環境変数リファレンス（索引）

**値・生成手順・注釈の正は [compose .env.example](../../deploy/compose/.env.example) と
[local oauth.env.example](../../deploy/local/oauth.env.example)**。本表は索引（グループ・変数・詳細の所在）。
括弧は未設定時の既定。

| グループ | 変数 | 役割 | 詳細 |
|----------|------|------|------|
| CP コア | `CP_ADDR`（`:8080`・実運用は `127.0.0.1:8099`）・`CONSOLE_DIR`・`AF_RUNTIME`（local）・`AF_DB`（`<WS_DATA>/control-plane.db`）・`PUBLIC_BASE_URL` | bind 先 / Console dist / Runtime 選択 / DB / 外部 URL | 本章 |
| Workspace 起動テンプレ | `WS_IMAGE`・`WS_DATA`・`WS_MEMORY`（1g）・`WS_AGENT_PORT`（7700 起点の割当）・`WS_AGENT_HOST`（127.0.0.1）・`WS_JVM_DIR`・`WS_ENV`・`WS_SESSION_CMD` | CP が `docker run` に流し込む共通テンプレ | [04](04-workspace-agent.md) |
| L1 認証 | `AUTH`（dev）・`DEV_USER`（dev）・`AUTH_EMAIL_HEADER`・`GOOGLE_OAUTH_CLIENT_ID/SECRET`・`AF_COOKIE_SECRET`・`AF_SESSION_TTL`（168h）・`AF_OAUTH_ALLOWED_{EMAILS,DOMAINS,EMAILS_FILE}` | Console ログイン。許可リスト全空 = fail-closed | [07 §7.3](07-security.md) |
| プロビジョン / 権限 | `AF_PROVISION`（auto）・`SUPER_ADMIN_EMAILS` | 未知 identity の自動受入ポリシー / 初期 super_admin | [06](06-data-model.md) |
| at-rest 暗号 | `AF_MASTER_KEY` | 未設定 = 平文（dev のみ）。**紛失 = crypto-shred**・データ領域と別金庫 | [07 §7.6](07-security.md) |
| git プロバイダ OAuth | `GITHUB_OAUTH_CLIENT_ID`・`BITBUCKET_OAUTH_KEY/SECRET` | Console の「OAuth 接続」ボタン有効化（無くても token 貼付で可）| [08](08-integrations.md) |
| scale-to-zero / showback | `AF_AUTOSTART`（on）・`AF_SESSION_IDLE_TIMEOUT`・`AF_WS_IDLE_TIMEOUT`・`AF_IDLE_SWEEP_INTERVAL`・`AF_STOP_GRACE_SEC`（30・上限 120）・`AF_USAGE_SAMPLE_INTERVAL`（5m） | 自動起動・アイドル停止・停止猶予・利用量サンプリング | [03](03-control-plane.md) |
| MCP | `AF_MCP_ENABLED` | CP `/mcp` エンドポイント有効化 | [08](08-integrations.md) |
| egress 🚧 | `AF_EGRESS_LISTEN`（:3128）・`AF_EGRESS_TOKEN`・`AF_EGRESS_{INGEST,POLICY}_URL`・`AF_EGRESS_PROXY_ADDR`・`AF_EGRESS_ENFORCE`・`AF_EGRESS_ALLOWLIST` | forward proxy サブコマンドと CP 集約 | [07 §7.8](07-security.md) |
| Postgres | `AF_DATABASE_URL` または `AF_DB_{HOST,PORT,USER,PASSWORD,NAME,SSLMODE}` | Store=postgres 選択時のみ | [06](06-data-model.md) |
| ECS アダプタ 🚧 | `AF_ECS_{CLUSTER,REGION,SUBNETS,SECURITY_GROUP,NAMESPACE_ARN,EFS_ID,EXEC_ROLE,TASK_ROLE,LOG_GROUP,TASK_CPU,TASK_MEMORY,POSIX_UID,POSIX_GID,START_TIMEOUT_SEC}` | CFN が作った静的基盤の座標を CP に渡す | [ecs runbook](../../deploy/aws/ecs/README.md) |
| native アダプタ 🚧 | `AF_NATIVE_AGENT_BIN`（PATH の `workspace-agent`） | コンテナレス実行時の workspace-agent バイナリの所在 | [34](../34-native-runtime.md) |
| コンテナ内（CP が注入・運用者は直接設定しない） | `AGENT_ADDR`（:7700）・`AGENT_TOKEN`・`AF_SECRET_KEY`・`AGENT_STOP_GRACE_SEC`・`AGENT_SESSION_CMD`・`CLAUDE_CONFIG_DIR`・`AF_AGENT_SELF_UPDATE_ALLOWED`・`AF_TMUX_SOCKET`/`AGENT_DOCS_DIR`（native のみ） | CP↔Agent 認証・DEK・停止猶予ほか | [04](04-workspace-agent.md) / [07 §7.5](07-security.md) |

網羅性の確認方法: 変数名そのものが grep アンカー。CP の読み値（`envOr` / `os.Getenv`）と
`run-dev.sh` の透過リスト・`.env.example` を突き合わせる。

**JDK の提供はランタイムで異なる（`/usr/lib/jvm` を常在と仮定しない）**。`WS_JVM_DIR` は
**local ランタイム専用**のノブで、ホストの共有 JDK を各コンテナへ `/usr/lib/jvm:ro` で
bind-mount する。**ECS はこのマウントが無い**（`home`・`claude` の EFS のみ）ので `/usr/lib/jvm`
は空になり得る。全ランタイム共通の受け皿は home ボリューム上の
`~/.local/share/agent-fleet/jvm`（local=ボリューム / ECS=EFS で永続）で、`workspace-agent
install-jdk <major>` が Adoptium から Temurin を入れる。Console のツール選択（toolchains）で
Java 版を選ぶと、entrypoint が未導入分をここへ自動導入し `JAVA_HOME` を通す。`availableJava`
相当（`GET /env/toolchains` の `java_available`）は「on-disk（両ディレクトリ）∪ install 可能 major」を返す。

## 9.5 aws ターゲットの設計（縮約）🚧

**P3-7 で実装済みだが実運用実績はない**（sandbox で deploy → E2E → teardown まで実証）。

- 対応関係: Runtime=ECS（1 Workspace = 1 Service・desired 0/1 = scale-to-zero）/ 永続ホーム=EFS
  アクセスポイント（per-workspace root・uid/gid 固定）/ 秘密=SSM SecureString（DEK は task definition に
  `valueFrom` ARN のみ・**平文 env に出さない**）/ CP→Agent 到達=Service Connect。
- **所有権の境界**: CFN（`00-network / 10-data / 20-platform / 30-ingress` の4段 + `ec2-single`）は
  静的基盤のみを 1 回構築。per-workspace リソース（Service・TaskDefinition・EFS AP・SSM param）は
  **CP が実行時に決定論的な名前で作る**（アダプタはステートレス・CFN churn ゼロ）。
- Runtime 契約の `starting` 状態は実質 ECS 専用（Fargate の cold image pull が分単位）。呼び出し側は
  収束待ちの間、再 Start もアイドル停止もしない。docker アダプタは秒で上がるため実際には報告しない。
- コスト試算は削除した（必要なら git 履歴の docs/reference/aws.md）。

## 9.6 パリティと相違点

| 観点 | local / compose | aws（ECS）🚧 |
|------|-----------------|--------------|
| Workspace イメージ / Agent | 同一物 | 同一物（移植の肝）|
| scale-to-zero | docker stop / start | Service desired 0/1。アイドル判定ロジックは共通・Runtime が実体差を吸収 |
| 隔離強度 | コンテナ境界（同一カーネル共有）| + タスク分離（Fargate はホスト共有なし）|
| egress | docker network + ホスト FW（enforce は 🚧 [07 §7.8](07-security.md)）| SG / NACL（Network Firewall は 📋）|
| ストレージ性能 | ローカルディスク（速い）| EFS はメタデータ操作の多い git で遅延しうる |
| 基盤権限 | docker.sock = ホスト root 相当（[07 §7.1](07-security.md)）| IMDS 遮断 + Task Role 最小化 |

## 9.7 バックアップ / リストア / アップグレードの設計前提

- **`WS_DATA`（compose では `DATA_DIR`）が保全対象のすべて**: DB・per-user home（`secrets.enc` 含む）・
  `claude-config`（平文 Claude 状態）・wrapped DEK・Caddy 証明書。再 provision 可能な `shared/jvm` のみ除外。
- **`AF_MASTER_KEY` はデータ領域にもバックアップにも含めない**（別金庫で独立保管）。失えば全バックアップが
  復号不能 = crypto-shred（[07 §7.6](07-security.md)）。逆にアーカイブ側には平文 Claude 状態が入るので
  アーカイブ自体も保護対象。
- リストアはパス再ルート可: `DATA_DIR` の親パスが変わっても CP が Workspace 起動時に現在値へ付け替える
  （basename は維持する契約）。
- アップグレード: migration は CP 埋め込み・起動時自動適用・**ダウングレード非対応** → 更新前に必ずバックアップ。
- 実手順（backup.sh / restore.sh / upgrade / air-gapped）: [compose runbook](../../deploy/compose/README.md)。
