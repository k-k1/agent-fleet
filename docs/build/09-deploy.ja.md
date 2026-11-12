---
audience: "デプロイ形態やアダプタを足す人"
source_of_truth: "コード ＋ 各 runbook（`deploy/*/README.md`）"
---

# 09. デプロイ — 形態・ポート&アダプタ・env 索引

[English](09-deploy.md) | 日本語

**実手順（コマンド）は各 runbook が正で、本書は複製しない。** 本書は「どの形態があり、何が
差し替わり、どのノブで制御するか」の地図。ターゲットごとに何ができて何ができないか（複数利用者・
利用者ごとの上限・エンジン・費用の按分）は [ref/deploy-targets](../../guide/ref/deploy-targets.ja.md)、
ターゲットの足し方は [21](21-add-a-deploy-target.ja.md)。

## 9.1 デプロイ形態

| 形態 | 概要 | 状態 | runbook |
|------|------|------|---------|
| **local dev** | CP をホストプロセスで起動。`run-dev.sh` はサブコマンド式の単一エントリ（`local` / `wsl` / `native` / `reset`＝データ初期化）、`restart-cp.sh` は CP だけを入れ替える軽量反映。1 人なら `AUTH=dev`、共有するなら `oauth` | ✅ 開発 + 小規模共有で運用中 | [run-dev.sh](../../deploy/local/run-dev.sh) / [restart-cp.sh](../../deploy/local/restart-cp.sh) の冒頭コメント。反映作法は [10](10-development.ja.md) |
| **wsl（個人）** | `run-dev.sh wsl`: local dev の WSL2 むけプリセット（native dockerd 前提・`AUTH=dev`） | ✅ 個人利用 | [deploy/local/README-wsl.md](../../deploy/local/README-wsl.md) |
| **native** | Docker 無し: CP と Console はホストプロセス、Workspace はダウンロードした rootfs 上の bubblewrap サンドボックスで動く。**1 人専用** — `native` ランタイムは `AUTH=dev` でないと起動を断る | ✅ パッケージとして配布 | [deploy/native/README.md](../../deploy/native/README.md) |
| **compose** | セルフホスト本命。CP コンテナ + Caddy（自動 TLS）。CP は loopback に bind し、コンテナからホストの Docker デーモンを駆動するための **3 制約**（host ネットワーク・`DATA_DIR` を同じ絶対パスでマウント・docker グループ id）を compose 定義が封じ込める | ✅ | [deploy/compose/README.md](../../deploy/compose/README.md) |
| **aws — ECS** | 静的基盤は CloudFormation、Workspace ごとのリソースは CP 自身の ECS アダプタが作る。Workspace は Fargate 上のタスク（`ecs`・テンプレートの既定）か、プールから取った EC2 スロット上のタスク（`ecs-ec2`） | ✅ 本番デプロイは `ecs-ec2` で稼働。`ecs` は sandbox で deploy → E2E → teardown まで実証 | [deploy/aws/ecs/README.md](../../deploy/aws/ecs/README.md) |
| **aws — ec2-single** | compose を EC2 VM 1 台に載せる | ✅ 「clean host でリリースバンドルから起動」ゲートの実施環境 | [deploy/aws/ec2-single/README.md](../../deploy/aws/ec2-single/README.md) |
| **kubernetes** | CP はクラスタ内の Deployment。Workspace は 0 か 1 レプリカの StatefulSet と PersistentVolumeClaim 2 本で、CP 自身の `kubernetes` アダプタが標準 API で作る。kustomize の base を持つ素のマニフェスト（`deploy/kubernetes/`）と、GKE の配備がクラスタの周りに要るものの Terraform（`deploy/gcp/gke/`） | ◐ できているが実クラスタではまだ動かしていない。GKE Standard での受け入れは #1468 | [deploy/kubernetes/README.md](../../deploy/kubernetes/README.md) |

- **ec2-single は VM 上の compose** — ランタイムは `docker` で、別のプロファイルではない。
- **kubernetes はどのクラスタにも 1 つのプロファイル。** クラウドの API を名指さない。GKE 固有のもの
  — ディスクの種類・ノードプール・Workload Identity・ロードバランサ — は StorageClass・Terraform・
  GKE のオーバーレイが選ぶ（[decisions/0106](../decisions/0106-kubernetes-runtime.ja.md) 決定 2）。
- 認証モードの中身は [07 §7.3](07-security.ja.md) が正。ここでは繰り返さない。

## 9.2 ポート&アダプタ — 何をどのノブで差し替えるか

**コアは全ターゲットで同一物**で、差し替わるのは CP 内の interface seam のみ（seam の一覧は
[01 §1.6](01-architecture.ja.md)）。本節はその選択ノブ側:

| ポート（seam） | 切替ノブ | 選択肢 |
|---------------|----------|--------|
| `Runtime` / `RuntimeFactory` | `AF_RUNTIME` | 空・`local`・`docker` = Docker Engine（既定）/ `ecs`・`aws` = Fargate 上の ECS / `ecs-ec2` = プールの EC2 スロット上の ECS（別名なし）/ `native`・`wsl` = サンドボックス化したホストプロセス（**`AUTH=dev` 必須**）/ `kubernetes`・`k8s` = Kubernetes クラスタ上の Workspace ごとの StatefulSet。**未知値は起動時に fail-fast**（`unknown AF_RUNTIME profile`・`runtime.NewFactory`） |
| `Store` | `AF_DB`（SQLite のパス）/ `AF_DATABASE_URL`、または `AF_DB_HOST` ほか `AF_DB_*` | SQLite（既定・pure Go）/ Postgres |
| `KeyCustodian` | `AF_MASTER_KEY`、次に `AF_KEY_CUSTODIAN` | 鍵あり = ローカル custodian、または `AF_KEY_CUSTODIAN=kms` + `AF_KMS_KEY_ID` で AWS KMS（master 鍵は引き続き必須）/ 未設定 = 暗号化なし（開発専用）。Vault は 📋（[decisions/0005](../decisions/0005-envelope-custodian.ja.md)） |
| `AuthGateway` | `AUTH` | `oauth`（compose と AWS のテンプレートが設定する）/ `proxy`（[07 §7.3](07-security.ja.md)）/ `dev`。既定値は無く、`native`/`wsl` 以外では未設定だと起動を拒否し、`dev` には `AF_ALLOW_INSECURE_DEV_AUTH=1` が要る |
| エンジン | エンジン表: `AF_ENGINES_SSM_PARAM` か `AF_ENGINES_JSON`、加えて `AF_LLM_URL` / `AF_COMFY_URL` からの役割ごとの 1 行 | AWS では CP が要求時に起動するエンジン。どこでも、ネットワーク上で既に動いているサーバを URL で指せる |
| Ingress / TLS | CP 外 | Caddy（compose）/ Tailscale Funnel（local）/ ALB + ACM（aws）/ クラスタの入口。GKE では Gateway API によるグローバル外部アプリケーションロードバランサ + Certificate Manager（kubernetes） |

## 9.3 入口（ingress）の選択肢と「入口からしか届かない」不変条件

**不変条件: CP には入口を通してしか届かない。** 1 台のホストでは CP は loopback に bind する —
イメージと compose は `CP_ADDR=127.0.0.1:8099` を設定する。コードの既定（`:8080`）と
`run-dev.sh` の既定（`:8099`）は全インタフェースに bind するので、開発ホストで 1 人使うなら
よいが、共有するなら誤り。AWS では CP タスクは自分のネットワークインタフェース内で `0.0.0.0` に
bind し、そのセキュリティグループはロードバランサのものだけを通す。`kubernetes` でも CP の Pod は
全インタフェースに bind し、ロードバランサが届くのはメインの Service。GKE ではそのポートに
ロードバランサだけを通す NetworkPolicy を置く。

**唯一の例外は Workspace 専用リスナー**（[decisions/0106](../decisions/0106-kubernetes-runtime.ja.md) 決定 8）。
Workspace が入口経由で CP に届かない構成——`kubernetes` ではクラスタ内のアドレスで CP を呼び戻す——では、
CP は Workspace 専用の 2 本目のポート `AF_CP_INTERNAL_LISTEN`（`deploy/kubernetes/` では `:8098`）で
応え、内部 Service（`af-cp-internal`）はそのポートだけを向く。CP はそのアドレスを `AF_CP_INTERNAL_URL`
として変更しない `AF_CP_BASE_URL` と並べて注入する。載るのは Agent が呼ぶ経路だけで、それぞれが
メンバーシップ単位の専用 bearer トークンで認証し（`control-plane/workspace_listener.go` の
`workspaceRoutes`）、それ以外——Console・管理 API・ログイン経路・egress プロキシの `/internal/egress*`——は
404 を返す。認証ゲートは無く、識別ヘッダと転送ヘッダをすべて捨て、`AF_TRUSTED_PROXY_HOPS` に
かかわらず接続そのもののアドレスを送信元とする。ブラウザや管理者が使うものに、ほかの道から届く
ことはない。未設定なら 2 本目のポートは無く、何も変わらない。

`docker`（compose）では任意で、公開側の入口が Docker のブリッジからの接続を受けないホスト（特定の
ホストインタフェースに bind したトンネルやリバースプロキシ）向け。CP は docker0 のゲートウェイアドレスに
bind し（`AF_CP_INTERNAL_LISTEN=172.17.0.1:8098`）、Workspace は
`AF_CP_INTERNAL_URL=http://host.docker.internal:8098` で届く。URL がこの名前を使うとき、アダプタが
`--add-host host.docker.internal:host-gateway` で名前を引けるようにする。docker0 のアドレスはホスト自身の
アドレスなので、どのユーザー別ネットワークからも Docker のネットワーク間分離を越えずに届き、LAN や
tailnet のインタフェースには載らない。`docker` で全インタフェースに bind すると起動時に警告を出す。
既定では無効（アドレスはホストの Docker とファイアウォールの設定次第で、存在しないアドレスを指定すると
CP が起動しない）。手順は [`deploy/compose/README.md`](../../deploy/compose/README.md) の
"Workspace listener"。

入口の仕事は TLS 終端と転送。`AUTH=oauth` では認証も CP 自身が担い、`AUTH=proxy` のときだけ
入口側が identity ヘッダを注入する。

| 入口 | 使いどころ | 備考 |
|------|-----------|------|
| **Caddy** | compose 標準 | `PUBLIC_DOMAIN` の DNS を向けるだけで Let's Encrypt 自動取得・更新（WS も透過）。CP と両方 host-net で loopback に到達。既存プロキシで前段する社は外せる（Caddyfile 代替2）|
| **Tailscale Funnel** | local 運用の一形態 | Funnel → `127.0.0.1:8099` 直結 |
| **ALB + ACM** | aws | TLS 終端のみ — 認証は CP に残る。`30-ingress.yaml` の `AuthMode` は `oauth` だけを許し（`dev` は https では拒否される。#3485）、テンプレートはロードバランサの OIDC を設定しない |
| **グローバル外部アプリケーションロードバランサ + Certificate Manager** | GKE 上の kubernetes | GKE の Gateway コントローラが作る（クラス `gke-l7-global-external-managed`。従来型は動いている WebSocket でもバックエンドのタイムアウトで切る）。アドレス・証明書・DNS は `deploy/gcp/gke`。`AF_TRUSTED_PROXY_HOPS` は 2（`<client>, <load balancer>` を足す）。アイドルな端末が 30 秒で切られないようバックエンドのタイムアウトを上げる |

- **入口を変えたら `PUBLIC_BASE_URL` を必ず合わせる** — OAuth の redirect の素であり、
  `https` 前置きが Secure cookie の前提。
- **CP の前にあるプロキシは 1 段ずつ `AF_TRUSTED_PROXY_HOPS` に数える**（既定 0 = `RemoteAddr`
  を信じる）。compose の例示 env と AWS テンプレートは 1 を設定し、ALB の前に CDN を置けば 2。
  テナントのネットワーク制限が見る送信元アドレスはこれで決まる（[07 §7.3](07-security.ja.md)）。

## 9.4 環境変数リファレンス（索引）

**値・生成手順・注釈の正は例示 env ファイル**（[compose](../../deploy/compose/.env.example)・
[local](../../deploy/local/oauth.env.example)）。AWS テンプレートはスタックのパラメータから
自分で設定する（[PARAMETERS.md](../../deploy/aws/ecs/cfn/PARAMETERS.md)）。本表は索引にすぎない。
括弧は未設定時のコードの既定。

| グループ | 変数 | 役割 | 詳細 |
|----------|------|------|------|
| CP コア | `CP_ADDR`（`:8080`）・`CONSOLE_DIR`・`AF_RUNTIME`（`local`）・`AF_DB`（`<WS_DATA>/control-plane.db`）・`PUBLIC_BASE_URL`・`AF_PREVIEW_DOMAIN`・`AF_DEV_IDP`（未設定。`0` で off に固定、それ以外は Admin モーダルの設定に従い既定は on。[ADR 0116](../decisions/0116-dev-oidc-idp.ja.md)・`PUBLIC_BASE_URL` が要る）・`AF_TRUSTED_PROXY_HOPS`（0） | bind 先・配る Console・アダプタの選択・外部 URL・プレビューのサブドメイン・送信元アドレス | 本章 |
| Workspace 専用リスナー | `AF_CP_INTERNAL_LISTEN`（未設定 = 無し）・`AF_CP_INTERNAL_URL`（未設定 = Workspace は `AF_CP_BASE_URL` を使う） | Workspace 専用のポートと、Workspace がそこへ届く URL（`deploy/kubernetes/` では `:8098` と `http://af-cp-internal.<cp-ns>.svc:8098`。`docker` では任意で、docker0 のゲートウェイアドレス（例 `172.17.0.1:8098`）と `http://host.docker.internal:8098`）。clone URL と `AF_INTERNAL_GIT_HOST` は公開のままで、Agent が Workspace の git をこの URL へ書き換え（`url.<internal>/git/.insteadOf`）、git の認証情報を両方のホストに保存し、Workspace 専用リスナーで答える LFS の転送先もこの URL を指す。`PUBLIC_BASE_URL` があるときだけ効き、反映は次の Workspace 起動から | §9.3 / [decisions/0106](../decisions/0106-kubernetes-runtime.ja.md) |
| Workspace 起動テンプレ | `WS_IMAGE`・`WS_DATA`（`/tmp/af-data`）・`WS_MEMORY`（`1g`）・`AF_MAX_WORKSPACE_MEM`・`AF_MAX_WORKSPACE_CPU`（Fargate 単位・既定 16384）・`AF_MAX_WORKSPACE_DISK_GB`（既定 1024）・`WS_AGENT_PORT`（7700・Workspace ごとのポートの起点）・`WS_AGENT_HOST`（`127.0.0.1`）・`WS_JVM_DIR`・`WS_ENV`・`WS_SESSION_CMD` | CP が Workspace を起動するときに流し込む共通テンプレ。`WS_ENV` が届くのは `docker`・`native`・Kubernetes の Workspace で、ECS 系ランタイムは渡さない | [04](04-agent.ja.md) |
| L1 認証 | `AUTH`（`dev`）・`DEV_USER`（`dev`）・`AUTH_EMAIL_HEADER`（`X-Forwarded-Email`）・`GOOGLE_OAUTH_CLIENT_ID/SECRET`・`AF_GITHUB_LOGIN_CLIENT_ID/SECRET`（または `GITHUB_OAUTH_CLIENT_ID/SECRET`）と `AF_GITHUB_ALLOWED_ORGS` ほか `AF_GITHUB_*`・`AF_OIDC_PROVIDERS` ＋ `AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST,LABEL_JA,LABEL_EN,SCOPES,PROMPT,LINK_CLAIM,ALLOWED_EMAILS,ALLOWED_DOMAINS,ALLOWED_TIDS}`・`AF_COOKIE_SECRET`・`AF_SESSION_TTL`（168h）・`AF_OAUTH_ALLOWED_{EMAILS,DOMAINS,EMAILS_FILE}` | Console ログイン。`AUTH=oauth` は有効な provider が無いと起動しない。OIDC の provider は `TRUST` の宣言が、GitHub は `AF_GITHUB_ALLOWED_ORGS` が必要で、無ければその provider は無効になる。**どの入口も受け入れないサインインは拒否される**: 入口はこれらの許可リスト・テナントの名簿・テナントの auto-join ドメイン・承認済みのテナント IdP。どれも無ければ全ログインが拒否される | [07 §7.3](07-security.ja.md) / [decisions/0043](../decisions/0043-login-idp.ja.md) |
| プロビジョン / 権限 | `AF_PROVISION`（`auto`）・`SUPER_ADMIN_EMAILS` | 未知の identity をどう受け入れるか / 誰がデプロイ管理者か | [06](06-data.ja.md) |
| at-rest 暗号 | `AF_MASTER_KEY` | 未設定 = 平文（開発専用）。**紛失 = crypto-shred** — データとは別の金庫に置く | [07 §7.6](07-security.ja.md) |
| git プロバイダ OAuth | `AF_GITHUB_BUILTIN_APPS`（on。`off` で GitHub の組み込みアプリを外す） | テナント管理者が Console で選ぶ・登録する。`BITBUCKET_OAUTH_KEY/SECRET` はもう読まれず、`GITHUB_OAUTH_CLIENT_ID` はサインイン専用 | [decisions/0052](../decisions/0052-tenant-git-oauth.ja.md) |
| scale-to-zero / showback | `AF_AUTOSTART`（on）・`AF_SESSION_IDLE_TIMEOUT`（1h）・`AF_INTERACTION_IDLE_TIMEOUT`（session の値）・`AF_WS_IDLE_TIMEOUT`（2h）・`AF_PRESENCE_IDLE_TIMEOUT`（30m）・`AF_IDLE_SWEEP_INTERVAL`（1m）・`AF_STOP_GRACE_SEC`（30・上限 120）・`AF_USAGE_SAMPLE_INTERVAL`（5m） | 自動起動・アイドル停止・停止猶予・利用量サンプリング。アイドルのタイムアウト・掃引・usage サンプラーは `0` で無効 | [03](03-control-plane.ja.md) |
| MCP | `AF_MCP_ENABLED` | `/mcp` がそもそも存在するか。有効になるのは文字列がちょうど `true` のときだけ | [08](08-integrations.ja.md) |
| MCP OAuth | `AF_MCP_OAUTH`（`1` で有効。`AF_MCP_ENABLED=true` と `https` の `PUBLIC_BASE_URL` が必要）· `AF_MCP_OAUTH_TRUSTED_HOSTS`（`claude.ai,claude.com,chatgpt.com`） | Control Plane が自身の `/mcp` の OAuth 認可サーバーになる。後者はサインインのリダイレクト先になれる／クライアントメタデータを配信できるホストのフォールバック（Admin モーダルで super admin が保存した一覧が優先） | [decisions/0118](../decisions/0118-mcp-oauth.ja.md) |
| ウォッチ Webhook | `AF_GITHUB_WEBHOOK_SECRET`（デプロイの GitHub App の Webhook シークレット。未設定なら `/webhooks/github` は存在しない） | ウォッチエンジンへのポーク。公開リポジトリのみ（[ADR 0133](../decisions/0133-external-watch-engine.ja.md) §4）。レート制限はプロセスごとなので、Control Plane を複数レプリカで動かすと実効の上限はレプリカ数倍になる。 | [05](05-api.ja.md) |
| egress | `AF_EGRESS_LISTEN`（`:3128`）・`AF_EGRESS_TOKEN`・`AF_EGRESS_{INGEST,POLICY}_URL`・`AF_EGRESS_PROXY_ADDR`・`AF_EGRESS_ENFORCE`・`AF_EGRESS_ALLOWLIST`・`AF_EGRESS_EVENT_RETENTION_DAYS`（生の gateway イベントを残す日数、既定 30、`0` で無期限）・`AF_EGRESS_ARCHIVE_URL`（`s3://bucket/prefix`・`gs://bucket/prefix`・`file:///dir` のいずれか。purge が消す前に、生イベントを UTC の日ごとにここへ保存する。空で無効。`gs://` はバケットへの `roles/storage.objectCreator` と `objectViewer` が要り、認証は Application Default Credentials のみ（GKE では Workload Identity）。[03 §3.8](03-control-plane.ja.md)） | forward proxy サブコマンドと CP の集約。`AF_EGRESS_PROXY_ADDR` がプロキシ変数を注入するのはすべてのターゲット: `docker`・`native`・Kubernetes は配備全体の env（`WS_ENV` を含む）をそのまま渡し、`ecs` と `ecs-ec2` はそこからプロキシ変数だけを取り、`WS_ENV` は引き続き渡さない。docker・Kubernetes・ecs・ecs-ec2 では CP の内部ホストが `NO_PROXY` に加わり、ecs / ecs-ec2 では `169.254.170.2` と `169.254.169.254` も加わる | [07 §7.8](07-security.ja.md) |
| Postgres | `AF_DATABASE_URL`、または `AF_DB_{HOST,PORT,USER,PASSWORD,NAME,SSLMODE}`、それと**パスワードの真値が居る場所** `AF_DB_PASSWORD_SECRET_ARN` / `AF_DB_PASSWORD_SECRET_KEY` | Store が Postgres のときだけ。部品から DSN を組む。ARN は、ローテートされたパスワードを**タスクを作り直さずに**拾うためのもの（§9.9） | [06](06-data.ja.md) |
| ECS アダプタ | `AF_ECS_{CLUSTER,REGION,SUBNETS,SECURITY_GROUP,NAMESPACE_ARN,EFS_ID,EXEC_ROLE,TASK_ROLE,INFRA_ROLE,LOG_GROUP,WORKSPACE_IMAGE,TASK_CPU,TASK_MEMORY,WS_DISK_GB,POSIX_UID,POSIX_GID,START_TIMEOUT_SEC}` | テンプレートが作った静的基盤の座標。`ecs` も `ecs-ec2` も読む | [ecs runbook](../../deploy/aws/ecs/README.md) |
| EC2 スロットプール | `AF_ECS_EC2_LAUNCH_TEMPLATE`（必須）・`AF_ECS_EC2_SLOT_TYPES`・`AF_ECS_EC2_DEFAULT_SLOT_CLASS`・`AF_ECS_EC2_AMI_ARM64`・`AF_ECS_EC2_MAX_SLOTS`（8）・`AF_ECS_EC2_HOME_GB`（50）・`AF_ECS_EC2_SLOT_SLEEP_SEC`（900）・`AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC`（0 = しない）・`AF_ECS_EC2_HIBERNATE_AFTER_SEC`（0 = 無効）・`AF_ECS_EC2_BACKUP_EVERY_SEC`（0 = 無効）・`AF_ECS_EC2_BACKUP_KEEP`（3）・`AF_ECS_EC2_GOLDEN_AUTOBAKE`（on）、ほかに掃引とタイミングのノブ `AF_ECS_EC2_*_SEC` | `ecs-ec2` 専用: スロットの型と上限・home の大きさ・§9.5 のアイドル段 | [ecs runbook](../../deploy/aws/ecs/README.md) §Optional: EC2 slot pool / [decisions/0045](../decisions/0045-ec2-persistent-workspace.ja.md) |
| ワークロードの AWS 身元 | `AF_WS_WORKLOAD_AWS`（オフ） | `1` で ECS のタスクロールをセッションとターミナルに戻し、SDK のインスタンスメタデータ参照の抑止をやめる。ネットワークの防護（`ECS_AWSVPC_BLOCK_IMDS`、IMDS のホップ数 1、`DOCKER-USER` の拒否）は外さないので、docker のワークスペースがホストのインスタンスプロファイルを得ることはない。それには運用者が別に許した資格情報の経路が要る。オフなら Agent は起動するものすべてで `AWS_CONTAINER_CREDENTIALS_*` / `AWS_CONTAINER_AUTHORIZATION_TOKEN*` を外し `AWS_EC2_METADATA_DISABLED=true` を立てる（docker では CP がそれを付けてコンテナを起動する）ので、メンバーのプロファイルを持たないツールはワークロードとして動かずに失敗する。CP に設定し、次のワークスペース起動ですべてのランタイムに届く（docker では動作中のコンテナは Stop → Start まで環境を保つ） | [guide member/16](../../guide/member/16-cloud-accounts.ja.md) |
| Claude の bedrock ルートの閉鎖（受け入れ確認専用） | `AF_WS_CLAUDE_BEDROCK_ROUTE`（オフ） | `1` で、すべてのワークスペース起動に `AF_CLAUDE_BEDROCK_ROUTE=1` を付ける。これは、実機での受け入れ確認（ADR 0128 決定 14）まで Agent が拒否している claude の `bedrock` ルートを開く。受け入れ確認の実施専用のスイッチで、リリース設定ではない（閉鎖を解くのは別の変更）。値は `1` ちょうどだけで、それ以外（`true`、`0`）は何も足さない。CP に設定し、次のワークスペース起動ですべてのランタイム（docker、ECS EC2/Fargate、Kubernetes、native）に届く。すでに動作中または起動中のワークスペースはどのランタイムでも環境を保つので、スイッチを付けて CP を再起動した後は Stop → Start が要る | [ADR 0128](../decisions/0128-harness-route-maker.ja.md) · [guide ref/agents](../../guide/ref/agents.ja.md) |
| opencode の bedrock ルートの閉鎖（受け入れ確認専用） | `AF_WS_OPENCODE_BEDROCK_ROUTE`（オフ） | `1` で、すべてのワークスペース起動に `AF_OPENCODE_BEDROCK_ROUTE=1` を付ける。これは、独自の実機での受け入れ確認（ADR 0128 決定 14）まで Agent が拒否している opencode の `bedrock` ルート（ターミナル専用）を開く。上の claude 用スイッチとは独立で、規則は同じ（受け入れ確認専用、値は `1` ちょうどだけ、次のワークスペース起動ですべてのランタイムに届く、動作中のワークスペースは Stop → Start が要る） | [ADR 0128](../decisions/0128-harness-route-maker.ja.md) · [guide ref/agents](../../guide/ref/agents.ja.md) |
| エンジン | `AF_ENGINES_SSM_PARAM` / `AF_ENGINES_JSON`・`AF_LLM_URL`・`AF_COMFY_URL` / `AF_COMFY_API_KEY`・`AF_ENGINE_API_KEY_<KEY>`・`AF_ENGINE_<KEY>_{CONTROL_INTERVAL_SEC,WINDOW_SEC,IDLE_SEC,START_DEADLINE_SEC,FAIL_COOLDOWN_SEC}`・`AF_ENGINE_ECS_CLUSTER`・`AF_ENGINE_WAKE_TIMEOUT`（900 秒）・`AF_ENGINE_PLAIN_HOLD`・`AF_REMOTE_ENGINE_{URL,TOKEN,KEYS}` | エンジン表・エンジンの制御器・冷えたエンジンに対するゲートウェイの保留・別デプロイのエンジンの借用 | [decisions/0071](../decisions/0071-self-hosted-inference-engines.ja.md) / [0076](../decisions/0076-external-image-engine-on-lan.ja.md) / [0077](../decisions/0077-engine-boxes-bought-by-cp.ja.md) / [0079](../decisions/0079-remote-engine-from-another-deployment.ja.md) |
| 音声 | `AF_VOICEVOX_URL`（`http://127.0.0.1:50021`）・`AF_TTS_ECS_SERVICE` ほか `AF_TTS_ECS_*`・`AF_TTS_MAX_CHARS`（300）・`AF_POLLY_{REGION,ENGINE}` | URL で指す VOICEVOX、または CP がゼロから起こす ECS 上の VOICEVOX。Amazon Polly | [decisions/0070](../decisions/0070-tts-ondemand-engine.ja.md) |
| コンテナレスアダプタ | `AF_NATIVE_AGENT_BIN`（`PATH` 上の `workspace-agent`）・`AF_NATIVE_ROOTFS`・`AF_NATIVE_BWRAP` | Agent バイナリの所在・bubblewrap サンドボックスを有効にする rootfs | [native runbook](../../deploy/native/README.md) |
| Kubernetes アダプタ | `AF_K8S_NAMESPACE`・`AF_K8S_WORKSPACE_IMAGE`・`AF_K8S_STORAGE_CLASS`・`AF_K8S_HOME_GIB`・`AF_K8S_STATE_GIB`・`AF_K8S_IMAGE_PULL_SECRET`・`AF_K8S_NODE_SELECTOR`・`AF_K8S_SERVICE_ACCOUNT`（`default`）・`AF_K8S_HOME_SHARED_FS`（未設定。StorageClass が 1 つの共有ファイルシステムでホームを支えるときは `1`。ホームの残量不足の通知を出さない） | `kubernetes` のみ: Workspace の名前空間・イメージ・StorageClass、claim の既定サイズ、ノードプール、Workspace から CP への道（Workspace 専用リスナーの行・§9.3） | [kubernetes runbook](../../deploy/kubernetes/README.md) / [decisions/0106](../decisions/0106-kubernetes-runtime.ja.md) |
| Workspace 内（CP が注入・**運用者は設定しない**） | `AGENT_TOKEN`・`AF_SECRET_KEY`・`AGENT_STOP_GRACE_SEC`・`AGENT_SESSION_CMD`・`CLAUDE_CONFIG_DIR`・`AF_AGENT_SELF_UPDATE_ALLOWED`・`AF_CP_BASE_URL` と機能ごとのトークン（`AF_DOCS_TOKEN`・`AF_MCP_TOKEN`・`AF_MEMO_TOKEN` …）・`native` ではさらに `AGENT_ADDR`・`AF_TMUX_SOCKET`・`AGENT_DOCS_DIR`・設定時（`kubernetes`、任意で `docker`）はさらに `AF_CP_INTERNAL_URL`（要求はこちらを優先し、人が開くリンクは公開 URL のまま） | CP↔Agent 認証・DEK・停止猶予・Agent から CP への経路（`manager.workspaceExtraEnv`）。トークンと DEK は `docker` では 0600 の env ファイル、ECS では SSM SecureString のタスクシークレットで渡る | [04](04-agent.ja.md) / [07 §7.5](07-security.ja.md) |

網羅性の確認方法: **変数名そのものが grep アンカー。** CP の読み値（`envx.Or`・`envx.DurationOr`・
`runtime.EnvInt`・`os.Getenv`）と例示 env ファイルを突き合わせる。`run-dev.sh` は渡すものを
`exec env` ブロックで名指すが、あれはフィルタではない（export 済みの変数は全部 CP に届く）。
また独自の既定（`CP_ADDR=:8099`・`WS_MEMORY=5g`）を置く。

**`0` がどこでも「無効」になるわけではない。** アイドルのタイムアウト・アイドル掃引と、止められると
書いてある常駐ループ（`AF_USAGE_SAMPLE_INTERVAL`・`AF_CLOUD_COST_INTERVAL`・`AF_GIT_GC_INTERVAL`・
`AF_SCHEDULER_INTERVAL`）は `intervalOff` で読み、`0` を無効と読む。それ以外に `envx.DurationOr` で読む
期間（`AF_SESSION_TTL`・`AF_SCHEDULE_SETTLE` …）は `0` を未設定とみなして既定を使う。

**JDK の提供はランタイムで異なる — `/usr/lib/jvm` が埋まっていると仮定しない。** `WS_JVM_DIR` を
`/usr/lib/jvm` に読み取り専用で bind-mount するのは `docker` と、rootfs モードの `native` だけ。
ECS 系ランタイムにはこのマウントが無いので、そのディレクトリは空になり得る。ランタイムに依らない
受け皿は home ボリューム上の `~/.local/share/agent-fleet/jvm` で、`workspace-agent install-jdk <major>`
が Adoptium（`api.adoptium.net`）から Temurin を、`install-jdk corretto-<major>` が
Amazon Corretto（`corretto.aws`）を入れる。どちらのホストも、ほかのインストーラのダウンロード元と一緒に
既定の egress 許可リストに入っている（`workspace/agent/testdata/installer-egress-hosts.txt` を両モジュールの
テストが `defaultEgressAllowlist` と突き合わせる）。選択値は JDK ID で、数字だけなら Temurin、
`corretto-<major>` なら Corretto（`workspace/agent/internal/jdkvendor`）。Console で Java 版を選ぶと、entrypoint が未導入分を入れて
`JAVA_HOME` を通す。`GET /env/toolchains` は両ディレクトリにあるもの ∪ 導入可能なもの
（`java_available`）を示し、**未導入の版を選ぶとその場で入れるボタンが出る**（`POST /env/jdk-install`、
その後 `GET` でポーリング）。導入後は `resolvedToolchains` が起動のたびにディレクトリを glob するので、
再起動なしで**次のセッションから**効く。同じメジャーが両方のディレクトリにあれば新しいパッチの方を
選ぶ（`release` ファイルの `SEMANTIC_VERSION`、Corretto は `version.txt`。同じ版なら `/usr/lib/jvm`）ので、導入済みのメジャーを
もう一度入れればそれが更新になる。toolchains の GET は `java_versions`（JDK ID → 解決後の版）も返し、
ネットワークには出ない。`GET /env/jdk-latest?major=<id>` がベンダー（Adoptium、または corretto.aws のリダイレクト）に聞き（6 時間、失敗は 10 分キャッシュ）、
`update_available` を返す。Console はそれが真のときに更新ボタンを出す。未導入のメジャーにも `latest`
（インストールで入る版）を返し、Console はそれをボタンに出す。`GET /env/node-latest?major=N` は Node.js 版
（nodejs.org の index から。版は nvm と同じく `v` 付き）。どちらもピッカーが出すメジャーしか受け付けない
（メジャーごとにキャッシュのキーになるため。`version_lookup.go`）。toolchains の GET は `node_versions`
（メジャー → `nodeBinFor` が選ぶパッチ）も返し、Node の行も同じ更新を出す。`install-node` は新しいパッチを
古いものの横に足すので、自分で入れたツリーに印（`.agent-fleet-installed`）を付け、entrypoint はその直後・
セッションが無いうちに `prune-node-versions` を走らせる。メジャーごとに、最新より古い印付きパッチを消す。
印の無いツリー（nvm で入れたもの・印ができる前に入ったもの）は消さない。印は誰が入れたかを示すだけなので、
固定されている版は印があっても残す：install-node が返したツリー（`--keep`）、厳密な版で保存された選択、nvm の
alias（symlink の alias も含む）や `~/.nvmrc` / `~/repos/*/.nvmrc`（nvm と同じくコメント・空行・`key=value` 行を
飛ばして読む）が名指す厳密な版。消す前に全ツリーを調べ、symlink の項目・ツリー内のディレクトリの symlink・
絶対パスの symlink・途中で一度でも自ツリーより上へ出る相対 symlink が一つでもあれば、その回は何も消さない（Node
自身のツリーには自ツリー内のファイルへの相対リンクが数本あるだけ）。最新に実行できる `bin/node` が無いメジャーは見送る。

`GET /env/toolchain-installs` は JDK の全ツリー（home ボリュームと `/usr/lib/jvm`）と `~/.nvm` 配下の Node の
メジャーを、版・サイズ・削除できるかと一緒に返す。`DELETE /env/jdk/{major}` / `DELETE /env/node/{major}` は
home ボリュームのツリーだけを消す（`toolchain_uninstall.go`）。選択中のメジャー（厳密な `22.23.4` も 22 とみなす）は、
独立した `/usr/lib/jvm` のコピー（上位のパスにも symlink が無い、home の外にある実ディレクトリで、home のコピーと
同じディレクトリ（デバイスと inode）を一つも共有しない＝bind mount ではないもので、Node の prune と同じリンク検査を通り、`bin/java` が
動くもの。Temurin 21/25 は通る。Temurin 8 はディレクトリのリンクがあるので代わりにならない）が残る場合を除いて拒否する。インストール中も拒否し、端末から
起動したものも含む（install-jdk / install-node はインストールの間ずっと置き場の flock を持つ）。判定と
`<root>/.agent-fleet-trash` への改名は、その flock・インストーラの mutex・選択の PUT と共有するロック（PUT は
`toolchains.json` を原子的に書き換えるようにした）の内側で行い、削除は外側で行う。掃除するのはごみ箱ディレクトリだけで、
それが実ディレクトリのときに限り、空になっても残す。一覧と DELETE は同じ「置き場が使用中」判定を使うので、一覧が
削除できると示したものを DELETE が断ることはない。

## 9.5 aws ターゲット

CloudFormation スタックは 7 本で、配備順は `00-network → 10-data → 20-platform →
（40-ec2-pool）→（50-tts）→（60-engines）→ 30-ingress`（括弧は任意）。各スタックが何を持つかは
運用ガイドの [ECS 構成の全体像](../../guide/operate/aws-ecs/overview.ja.md)。

- **所有権の境界**: テンプレートは**静的基盤を 1 回だけ**作る。Workspace ごとのリソースは
  **CP が実行時に決定論的な名前で作る** — アダプタはステートレス（全部名前かタグで見つける）で、
  テンプレートは churn しない。
- **共通の対応関係**: 1 Workspace = desired 0 か 1 の ECS サービス 1 本（scale-to-zero）。Agent
  トークンと DEK は SSM SecureString パラメータなので、**DEK はタスク定義に参照としてだけ現れ、
  平文では決して現れない**。CP から Agent へは Service Connect。CP 自身は `30-ingress` の
  Fargate サービス（`desiredCount 1`）で、ストアは RDS Postgres。
- **`ecs`（Fargate）**: home は root と uid/gid を固定した EFS アクセスポイント。Fargate は
  イメージキャッシュを持たないので、毎回の起動でイメージをゼロから pull する。
- **`ecs-ec2`（EC2 スロットプール・[decisions/0045](../decisions/0045-ec2-persistent-workspace.ja.md)）**:
  - **スロットは同時に 1 メンバーだけが使う EC2 インスタンス。** CP が自分で買い
    （`40-ec2-pool` の起動テンプレートから `RunInstances` — Auto Scaling グループも capacity
    provider も無い）、上限は `AF_ECS_EC2_MAX_SLOTS`。タスクは `ec2InstanceId ==` の配置制約で
    そのスロットに固定する。スロットのルートボリュームがイメージキャッシュになる。
  - **home はメンバー自身の gp3 EBS ボリューム。** CP が `/dev/sdf` にアタッチし、SSM 経由で
    マウントする。資格情報（`/var/lib/af/claude`）と `keep` 領域は EFS に残る。
  - **新しい home はゴールデンスナップショットから作る** — 起動時インストールを済ませた home で、
    Workspace イメージが変わるたびに CP が焼き、動いているイメージと合わなければ使わない。
  - **アイドルは段になっている。** Stop は desired を 0 にして home をアタッチしたまま残すので、
    メンバーは同じスロットに戻る。`AF_ECS_EC2_SLOT_SLEEP_SEC` を過ぎると空いたスロットを停止する
    （ルートボリュームは課金され続ける）。`AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC` を過ぎると、
    先に home を外してから終了する。`AF_ECS_EC2_HIBERNATE_AFTER_SEC`（またはテナント自身の上限）を
    過ぎると home をスナップショットにしてボリュームを消し、次の起動で戻す。
    `AF_ECS_EC2_BACKUP_EVERY_SEC` は使用中の home の予備スナップショットを取る — EBS
    ボリュームは AZ を出られないので、AZ を失ったときに戻る唯一の道。
  - **デプロイがこれを選ぶ理由は I/O・本当に残る home・Fargate の上限を超える大きさで、起動時間
    ではない。** アダプタ経由の実測で warm 起動は 43〜110 秒、Fargate は ~105 秒。EBS の home は
    小さいファイルの書き込みが EFS の 8〜30 倍速い。
- **Runtime 契約の `starting` 状態は、起動の収束待ちの間、全アダプタが報告する。** その間、呼び出し側は
  再 Start もアイドル停止もしない。ローカルのアダプタ（`docker`・`native`）は、Start のマーカーが
  立っていて Agent がまだ `/healthz` に答えていない間これを報告する。Start が Agent を待つのは猶予
  （アダプタの既定値か `AF_AGENT_HEALTH_WAIT_SEC`）だけで、その後は返り、`State` は Agent が答えるか
  マーカーの期限——Start がマーカーを立てた時点から `AgentBootBudget` と猶予の長い方——が過ぎるまで
  `starting` を返し続ける（`runtime_health.go`）。**ECS では Start は Agent を待たずに返る**:
  `ecs` ではサービスの desired count を設定した時点で、`ecs-ec2` ではそれより前のことも
  ある — スロットがまだ起動中・復帰中・登録中なら配置は背景（`finishStart`）で仕上がり、home に
  付けた claim が状態を `starting` に保つ。どちらでも収束は Console の `GET /api/workspace`
  ポーリングが拾う。同期待ちは戻せない: cold start はロードバランサの
  idle timeout 60 秒より長く、504 になる。
- **Fargate の起動は内訳を測ってある**: warm home の再起動 ~101 秒のうちイメージ pull は ~35 秒で、
  遅延ロード（SOCI）は不採用になった。残りはタスク作成・ネットワークインタフェース・EFS マウント・
  entrypoint。
- **エンジンは別スタック。** `50-tts` は Fargate 上の VOICEVOX で、CP がゼロから起こす。
  `60-engines` は llama.cpp と ComfyUI を、要求が来たときに CP が EC2 Fleet で買う GPU
  インスタンス上の ECS サービスとして動かす。どちらも Workspace のランタイムには依存せず、CP は
  エンジン表を通して見つける。
- コスト特性は §9.8。

## 9.6 パリティと相違点

Workspace イメージと Agent は全ターゲットで同一物 — それが分割の要点。能力の一覧は
[ref/deploy-targets](../../guide/ref/deploy-targets.ja.md) が正で、以下はその下にある基盤の違い。

| 観点 | docker / compose | native | ecs（Fargate） | ecs-ec2 | kubernetes |
|------|------------------|--------|----------------|---------|---|
| scale-to-zero | コンテナの stop / start | プロセスの stop / start | desired 0/1 | desired 0/1、その先は §9.5 のアイドル段 | replicas 0/1 |
| 隔離 | コンテナ境界（カーネル共有） | bubblewrap サンドボックス・1 人 | ホストを共有しないタスク | 同時には他の誰も使わないインスタンス上のタスク | `restricted` の Pod Security Standard 下の Pod。ノードのカーネルを他の Workspace と共有 |
| egress | コンテナのネットワーク、任意で forward proxy（[07 §7.8](07-security.ja.md)） | ホストのもの | セキュリティグループ | セキュリティグループ | NetworkPolicy、その先はクラスタの NAT（GKE では Cloud NAT） |
| home の置き場 | ローカルディレクトリ（速い） | ローカルディレクトリ | EFS: **git のようにメタデータ操作の多い作業は遅い** | EBS。資格情報は EFS | Workspace ごとのブロックストレージのボリューム（ゾーン単位）。ログインと Claude の状態は 2 本目 |
| 基盤権限 | Docker ソケットはホスト root 相当（[07 §7.1](07-security.ja.md)） | 利用者自身のアカウント | 最小のタスクロール・インスタンスメタデータ無し | 最小のタスクロール | Workspace の名前空間の Role と読み取り専用の ClusterRole。Workspace にクラウドの身元は無い |

**アイドル判定のロジックは共通**で、「停止」の実体の差は各 Runtime が吸収する。

## 9.7 バックアップ / リストア / アップグレードの設計前提

- **1 台のホストでは `WS_DATA`（compose では `DATA_DIR`）が保全対象のすべて**: DB・暗号化
  ストア `secrets.enc` を含む全員の home・平文の Agent 状態・wrap された DEK・Caddy の証明書。
  除外するのは再 provision できるもの（`shared/jvm`）だけ。
- **`AF_MASTER_KEY` はデータ領域にもバックアップにも入れない** — 別に保管する。失えば全バックアップが
  復号不能になる。逆に、**アーカイブには平文の Agent 状態が入るので、アーカイブ自体も保護対象。**
- リストアは親パスが変わってもよい: CP が起動時に付け替える。**ただし basename は契約**。
- **アップグレードは埋め込みの migration を起動時に自動適用し、ダウングレードできない** — 必ず先に
  バックアップする。
- **AWS では `WS_DATA` は何も持たない**（`30-ingress` は `/tmp` を指す）。状態は RDS・EFS、
  それに `ecs-ec2` ではメンバーの EBS home に分かれ、守られ方はそろっていない — **テンプレートが
  バックアップを宣言するのは RDS と EFS で、どちらも `Persistence=retain` のときだけ。EBS の home の
  バックアップは既定で無効。** スタック削除時にリソースを
  残すことはバックアップではない:
  - RDS: `10-data` の `Persistence=retain` が 7 日の自動バックアップ・最終スナップショット・
    削除保護を入れる。
  - EFS: `Persistence=retain` はスタックを消したときにファイルシステムを残し、専用のボールトへの
    AWS Backup の日次プラン（復旧ポイントの保持は `EfsBackupRetentionDays`・既定 7 日）を加える。
    復元は手作業 — メンバー 1 人分のディレクトリかファイルシステム全体を、稼働中のデータの横の
    ディレクトリへ戻してから書き戻す: ecs runbook の §EFS backup and restore。
  - EBS の home（`ecs-ec2`）: 守るのは §9.5 の任意の home バックアップ
    （`AF_ECS_EC2_BACKUP_EVERY_SEC`・既定は無効）だけ。
- **ECS のアップグレードはアプリのタグだけではない。** リリースが新しい ECR リポジトリと、まだ誰も
  写していないイメージを必要とすることがある（エンジンの fetch / ingest の手順は
  `af-engine-tools` にある）。そのため `update.sh` は順序を 1 本に固定する: **リポジトリ
  （20-platform、change set を出してから、置換が無いときだけ実行）→ イメージ（GHCR から
  `crane copy`）→ それを参照するスタック（60-engines）**。逆順でもその場では何も失敗せず、
  スタックは配備でき、fetch コンテナだけが `CannotPullContainerError` のまま、サービスは
  steady state を報告する。イメージを*焼く*ことだけは意図的にやらない: GHCR にもタグが無ければ
  止まり、焼くワークフローの名前を出す。
- 実手順（`backup.sh`・`restore.sh`・アップグレード・air-gapped）は
  [compose runbook](../../deploy/compose/README.md)、ECS は運用ガイドの
  [アップグレードする](../../guide/operate/aws-ecs/upgrade.ja.md)。

## 9.8 コスト特性（ec2-single / ECS）

AWS の形態は課金の**形**が違う。VM 1 台は**人数によらずほぼ定額**、ECS は**常設の床 + 人数×稼働
時間**で、scale-to-zero が効く。**選定はこの形で決まり**、下の絶対額はその裏付けにすぎない。

> **前提**: *実測* と書いたもの以外は、AWS Pricing API による ap-northeast-1（東京）の定価・
> 730 時間/月・2026-08 時点 — ecs runbook の「Cost & ephemerality」と
> [decisions/0045](../decisions/0045-ec2-persistent-workspace.ja.md)。us-east-1 は概ね 30% 安い。
> リザーブドインスタンスや Savings Plans は未適用。**エージェントのサブスクリプションは各利用者の
> ものであり、一切含まない。**

### 9.8.1 VM 1 台 — 定額

| 項目 | 月額 | 備考 |
|------|------|------|
| インスタンス・30 GB のディスク・固定 IP | ≈ $87（t3.large・既定）/ ≈ $47（t3.medium） | テンプレートの選択肢は t3.medium・t3.large・t3.xlarge |
| DNS ゾーン | $0.50 | テンプレートは既存の Route53 ホストゾーンを必要とする |
| **合計** | **≈ $88/月** | **人数が増えても変わらない** — RAM が尽きるまで |

**律速は RAM で、CPU ではない。** CP・Caddy・OS が要る分を引いた残りを `WS_MEMORY` で割った
数が、上限まで使う Workspace を同時に動かせる数。compose の例示は 5g なので t3.large で 1 つ分 —
チームで使う VM は全員のピークに合わせて選ぶことになる。runbook が t3.medium では上限を下げろと
言うのもこのため。

注意すべき性質:

- **バースト系のインスタンスファミリーは CPU クレジットを使い切ると基準性能に落ちる。** 重いビルドが
  続くなら固定性能のファミリーにする。
- **scale-to-zero は効かない。** アイドル停止は Workspace コンテナを止めるだけで、VM の課金は続く。
  **週末の費用を削るには VM 自体を止める。**
- **単一障害点**で、隔離はコンテナ境界だけ。

### 9.8.2 ECS — 常設の床 + 従量

**常設の床（Workspace が全部止まっていても掛かる）:**

| 項目 | 月額 | 備考 |
|------|------|------|
| NAT ゲートウェイ | $45 | Workspace は git やモデルの API へこれを通って出るうえ、起動経路（ECR・ログ・SSM）にも乗る。NAT インスタンス（≈ $8）にするのが最大の単一レバー |
| CP タスク 24/7（0.5 vCPU / 1 GB） | $23 | |
| データベース（RDS db.t4g.micro・20 GB） | $21 | CP タスクを入れ替えても状態が残る根拠 |
| ロードバランサ | $18 | + 従量 |
| シークレット・サービスディスカバリ・レジストリ | ≈ $1 | |
| EFS | 使用量 | `ecs` では全員の home、`ecs-ec2` では資格情報だけ |
| EFS のバックアップ | 使用量 | `Persistence=retain` のときだけ: バックアップ保存 $0.06/GB 月・日次 7 点 |
| **床** | **≈ $107/月 + EFS** | |

**Workspace 1 つあたり:**

| | `ecs`（Fargate・1 vCPU / 2 GB） | `ecs-ec2`（m7i.large・2 vCPU / 8 GB） |
|---|---|---|
| 稼働中 | $0.0616/時 | $0.130/時（m7i はどの大きさでも vCPU 時あたり $0.0651） |
| 平日 8 時間 × 22 日 | ≈ $11 | ≈ $23 |
| 24/7（アイドル停止が効いていない） | ≈ $45 | ≈ $95 |
| 停止中 | EFS（home が使った分） | home の EBS（**確保した**分・$0.096/GB 月・50 GB で $4.80）、加えてスロットが終了されるまではそのルートボリューム（100 GB で $9.60） |

- **24/7 の行は scale-to-zero が壊れたときの請求額でもある。** アイドル設定が実際に効いているかは、
  運用だけでなく**費用**の問題でもある。
- **EBS は確保した量、EFS は使った量で課金される**: 分岐点は home の使用率 26.7%
  （$0.096 / $0.36）。休眠させると、20 GB 使った 50 GB の home が $4.80 から $1.00 の
  スナップショットになる。
- **EFS の I/O は別の費目**で、資格情報が残る `ecs-ec2` でも掛かる。本番デプロイで 1 日 *実測*
  した elastic スループットの I/O（2026-09-17・CloudWatch × 単価）は Workspace 1 時間あたり約
  $0.10 で、月 1,341 Workspace 時間という *見積り* を掛けると月約 $135 になる。CloudWatch で
  測るこの方法自体は、前日の窓で Cost Explorer と突き合わせて 0.12% で一致した
  （[decisions/0087](../decisions/0087-efs-metadata-io.ja.md)。まだ本番に届いていない修正も
  そこに記録がある）。
- **動かしっぱなしの GPU エンジンは他の全部を上回る**: g6.xlarge は $1.26/時（月 ≈ $918）。
  CP はエンジンを要求時に起こしてアイドルで止め、`pause.sh` はまだ生きているエンジンの
  インスタンスを掃除する。
- **小さなデプロイの請求の大半は人ではなく床。** sandbox での *実測*（2026-08-01〜16）: メンバーに
  帰属できたのは請求の最大 22.3% で、残りは NAT・DNS・税・EFS・CP・ロードバランサ・
  データベース・パブリック IPv4 だった（[decisions/0048](../decisions/0048-member-cloud-cost.ja.md)）。`ecs-ec2` では
  Console がコスト配分タグからメンバーごとの実費を示す。`ecs` のタグ付けは実機未検証のまま出ている。

### 9.8.3 使い分け

- **1〜2 人、または VM 1 台に収まるチーム → ec2-single**（AWS 上の compose）。素直に安く、
  全員のピークに合わせた VM でも定額のまま。
- **ECS が費用で追いつくのは同時利用 8〜10 人あたり**（上の定価からの見積り）。そこでは VM を全員の
  ピークに合わせて 24 時間動かすことになり、ECS は各 Workspace が動いた時間だけ課金する。
- **それより少ない人数で ECS を選ぶのは、価格に関係なく得られるもののため**: タスク単位の隔離・
  ユーザー単位の障害分離・イメージの順次入れ替え・より締まったメタデータとロールの構え（§9.6）。
  **scale-to-zero は安くする仕組みではなく、床を薄める仕組み。**
- **ECS の 2 つのランタイムの間では**、`ecs-ec2` は稼働 1 時間あたりが高いかわりに箱が大きく、
  I/O・永続性・大きさを買う（§9.5）。Workspace ごとに運用するリソースも 4 種類増える。
- **見積りは実際の請求と突き合わせる** — Cost Explorer と、`ecs-ec2` なら Console のメンバー別コスト表示で。

## 9.9 ヘルスとレディネスと動くパスワード

**`/healthz` は liveness だけ。** リテラルの `ok` を書くだけで、他には何にも触れない —— DB にも
ファイルシステムにもアダプタにも。`deploy/local/restart-cp.sh` は body をこの文字列と verbatim
比較しており、ALB のターゲットグループもここをヘルスチェックしている。**凍結された契約**として扱うこと。

**ストアを実際に見に行くのは `/readyz`。** MetadataStore に 2 秒の予算で ping し、届かなければ
`503 database unavailable` を返す。セッション無しで届く（監視はログインできないので）。body には
未認証の呼び出し元に見せてよくないものを意図的に一切書かない。

**ALB は意図的に `/healthz` のまま。** `/readyz` へ向けると、DB の一瞬の不可用が CP タスクを
殺してしまう。CP は `desiredCount 1` なので、CP が今や自力でやる自己修復と引き換えに、恒久的な
再起動リスクを買うことになる（[decisions/0065](../decisions/0065-db-credential-rotation.ja.md)）。

### RDS で DB パスワードを環境変数にできない理由

ECS タスク定義の `secrets` が解決されるのは**タスク起動時の 1 回だけ**である。RDS のマネージドな
マスターパスワードは定期的（既定 7 日）にローテートするので、長く走っているタスクは DB が受け付け
なくなったパスワードを提示し続け、全クエリが `28P01` で落ちる。**これは外からは一切見えない** ——
プロセスは生きているので `/healthz` は `ok`、だからターゲットは healthy、だからサービスは
steady state である。

そこで CP は注入された値をブートストラップの手掛かりとして扱い、Postgres に拒否されたら
`AF_DB_PASSWORD_SECRET_ARN` を Secrets Manager から読み直して、コネクタの中でリトライする。
これが効くには 2 つが真である必要がある。

1. **`secretsmanager:GetSecretValue` が要るのは実行ロールではなく `CpTaskRole`。** 実行ロール側の
   同じ許可は起動時に変数を注入するためのもので、その後は何もしない。タスクロールに無いと、CP は
   注入値のまま動き続けて `DB_SECRET_REFRESH_FAILED` を吐く —— 機構は消えているが、**次の
   ローテーションまで何も壊れない**。
2. **`AF_DB_PASSWORD_SECRET_ARN` が設定されていること。** 未設定＝注入値がすべて。compose・
   オンプレ・SQLite では正しく、RDS では潜在的な障害である。

### 聞こえるようにする

CP は接続を開けないとき `DB_UNAVAILABLE` を出す。`30-ingress.yaml` はこれを CloudWatch メトリクス
`AgentFleet/<stack>/DbUnavailable` に**無条件で**変換し、`CpAlarmEmail`（既定は空）がそのアラームに
宛先を購読させる。

**設定すること。** 2026-09-01、本番のデプロイが全呼び出しに 500 を返していたという記録は、誰も
開く理由の無かったロググループの 1 行だけだった。最後の手段としての復旧は、blue/green なので
4 分・断無し:

```sh
aws ecs update-service --cluster <cluster> --service <cp-service> --force-new-deployment
```

## 9.10 ECS runbook の開発者向けの節

以下は、手順が運用ガイド（`guide/operate/aws-ecs/`）に移るまで `deploy/aws/ecs/README.md` にあった節で、
ここにそのまま移した（英語版が原文、こちらはその訳）。Agent Fleet を変える人だけが実行するもの、または
ガイドが経緯を省いて規則だけを書いている、その経緯にあたるもの: ECS アダプタの検証手順と E2E 完了ゲート、
開発用デプロイ（このリポジトリのワークフローで `main` を焼く `dev-deploy.sh`）、51,200 バイトのテンプレートの
件とその往復、コールドスタートの計測。見出しは 1 段下げ、`docs/log/` へのリンクは文字にした。

### 検証手順（Prove-out sequence）

**専用のサンドボックス**の認証情報を持つホスト（または Agent Fleet のワークスペース — ドッグフーディング）から
実行する。AWS の API 呼び出しはただの HTTPS で、重い計算（ECS タスク、RDS）は AWS 側にあるので、
このホストのビルド / OOM の制限は効かない。

- **S0** — サンドボックスで `aws sts get-caller-identity`。リージョンを選ぶ。
- **S1** — `00-network.yaml` をデプロイ。デプロイ → `delete-stack` の往復と常時の費用（主に NAT）を確かめる。**実証済み。**
- **S1.5** — `10-data.yaml`（EFS+RDS）と `20-platform.yaml`（ECR/クラスター/SC 名前空間/IAM）をデプロイ。確かめて撤去。**実証済み。**
- **S2** — ECR ができたら（`20` から）、CP と Workspace のイメージを push（下の §ECR push）。ECR に入るまで ECS は取得できない。
- **S3** — `30-ingress.yaml` をデプロイ。CP/Console のサービスが起動し、CP ネイティブの Google OAuth ログインが動き、RDS に届く。
- **S4** — `runtime_ecs.go`（段 2）を実装した状態で、CP が**ワークスペースを動的に用意する**（本物のアダプタを通す）→ 下の E2E ゲート。

CFN の作成（S1–S3）と段 2 の Go のコードは**並行の線**で、S4 で合流する。

### E2E 完了ゲート（凍結仕様 §20b.7.14）

1. ログイン（CP ネイティブの Google OAuth）→ テナント → ワークスペースの Start（Service desired 1、RUNNING、healthz）。
2. セッション作成 → ターミナル接続（CP→Service Connect→Agent）→ 入出力。
3. Stop（desired 0）→ 再 Start でホームと claude の状態が戻る（EFS が残る）。
4. P3-9 リーパー: アイドル → 段 1 停止 → 段 2 desired 0。接続中は発火しない。
5. **平文の秘密がない**: `describe-task-definition` で DEK は SSM の `valueFrom` の ARN としてだけ出て、`environment` の値には出ない。

### 開発用デプロイ: `dev-deploy.sh`

`update.sh` は**リリース済み**のタグをデプロイする。開発用に置いているデプロイが欲しいのはその逆で、
版を切らずに今の `main` を載せたい。それが `dev-deploy.sh`（docs/log/73）で、最後に `update.sh` を呼ぶ —
上の事前チェックがその理由で、開発用の経路にチェックの写しを書くと、ずれるのはチェックの方になる。

```bash
./dev-deploy.sh --profile <p> --region <r>            # origin/main → the dev deployment
./dev-deploy.sh --profile <p> --region <r> --dry-run  # just the plan
```

タグを決め（`<next patch>-dev-<sha8>`）、`dev-image.yml`（GHCR、リリースのタグには決してしない）で焼き、
ECR に**インデックスごと**写し、`ImageTag` を動かす。代わりに決めてくれること:

- **ワークスペースのイメージを焼く必要があるか。** デプロイ済みのタグから `workspace/` が変わったときだけ。
  それ以外はワークスペースのイメージを ECR の中でタグ付けし直すだけで、バイトは写さない。焼くと QEMU で約 10 分。
- **中身が同じならゴールデンスナップショットを焼き直さなくてよいこと**（docs/log/72 §72.6.4 はまさにそれで 10 分とスロット 2 台を払った）。
- **誰かの本番のデプロイではないこと。** `--allow-fqdn` がない限り、開発用以外の `Fqdn` を拒否する。
  `ImageTag` を動かすと、そこでワークスペースを動かしている全員に「要再起動」のバッジが出るため。
- **エンジンのツールのイメージを運ぶ必要があるか。** `af-engine-tools` は取得のサイドカーと取り込みの手順を持ち（ADR 0072）、
  `dev-image.yml` には含まれず、以前は `standup.sh` だけが写していた — そのため開発用デプロイでは、エンジンの取得の
  コンテナが取得するものがなかった。今は、エンジンのスタックが求めるタグが ECR になければ運び、デプロイ済みのコミットから
  `deploy/aws/ecs/engine-tools/` が変わっていればコミットごとのタグを焼く。🔴 `EngineToolsImageTag` は自分では設定できない
  （ingress スタックに `update.sh` を実行するだけ）ので、後者の場合は `params/60-engines` に設定するタグを表示する —
  そうしないと、デプロイは最新に見えて前のリリースのスクリプトを動かす。

⚠️ 焼くのは **origin の** ref。push していないローカルのコミットは、黙って含まれない。

### 51,200 バイトの壁（誰も触っていないのにデプロイが止まる理由）

CloudFormation は **51,200 バイト**を超えるテンプレート本文を拒否し、それより大きいものは S3 経由で渡す必要がある。
`cfn/30-ingress.yaml` は **2026-09-01** にこの線を越え（PR #277、RDS の管理パスワードのローテーション: 50,191 → 54,681 バイト）、
その瞬間に**それをデプロイするすべての経路が止まった** — `standup.sh`（構築）と `update.sh`（本番を含むすべてのリリース）。
どちらも同じ `cloudformation deploy --template-file` を呼ぶため。

見つかったのは 2026-09-02、その後最初の構築でだった。隠れていた理由は 3 つ:

1. **AWS CLI は API を呼ぶ前にファイルの大きさで拒否する。** 失敗は CloudFormation ではなく CLI のエラーで、スタックのイベントには何もない。
2. **`update.sh` は正常に見えた。** 上書きするのは `ImageTag` だけなので、パラメータの変更だと思われがちだが、毎回テンプレート全体を渡すので同じように壊れていた。
3. **構築の経路は端から端まで通したことがなかった。** `30-ingress` の作成は、大きくなってから一度も実行されていない唯一の操作だった。

★ これはより一般的な規則の一例: **デプロイのスクリプトは、誰かが実際に通す経路でしか検証されていない。**
緑の単体テストはそこに触れず、「当然まだ動く」と飛ばした経路こそが腐る。だから直し方は「大きいテンプレートは S3 へ」だけではない:

- `env.sh` の **`af_cfn_deploy` が `cloudformation deploy` の唯一の入り口**で、**呼ぶたびにファイルを測る**。
  「30-ingress が大きい」と覚えておくやり方は、次に大きくなったテンプレートで同じ事故を繰り返す。
- 受け渡しの S3 Bucket は **20-platform** にある（30-ingress より先にでき、後まで残る。30-ingress に置くと、
  それを作るスタックのデプロイにその S3 Bucket が要ることになる）。オブジェクトは 7 日で消える。
- `teardown.sh` は **20-platform を消す前に S3 Bucket を空にする** — CloudFormation は空でない S3 Bucket を消せず、
  途中で死ぬ撤去こそが「作成の経路が通されない」状態を再現する。
- `deploy/local/ecs-lifecycle-stub-test.sh` のケース **3b / 3c** が、わざと膨らませたテンプレートで、
  CI のたびにこの両方を実行する。誰も実行しない修正は、元のバグそのもの。

#### 壁の内側に戻した（2026-09-04）

S3 の経路は安全網であって定位置ではない — 20-platform ができて初めて存在する S3 Bucket が要り、撤去の順番を縛り、
失敗はスタックのイベントに何も残らない CLI のエラーとして出る。そこで `30-ingress.yaml` を上限の内側に戻した:
**54,681 → 40,493 バイト**。パラメータ・既定値・リソースは何も変えていない。

動かしたのは説明文。パラメータの `Description:` の本文が 21,630 バイト — **テンプレートの 40%** — で、
API に届かなくても役目を果たせる唯一の部分だった。今は [`cfn/PARAMETERS.md`](../../deploy/aws/ecs/cfn/PARAMETERS.md) に全文があり、
テンプレートにはパラメータごとに 1〜2 行だけを残している。

⚠️ **YAML のコメントは `Description:` とまったく同じだけかかる。** 上限はテンプレートの*本文*にかかるので、
段落を `#` に移しても 1 バイトも減らない。長い説明はファイルの中で書き方を変えるのではなく、ファイルの外に出す。

`ecs-lifecycle-stub-test.sh` のケース **3b-2** は、どれかの `cfn/*.yaml` が 51,200 バイトを超えるとビルドを失敗させる。
わざと強い失敗にしている: 2026-09-01 の膨張は 3 か月気づかれず、警告でも同じだったはず。壁をまた越えるのは、
差分に見える、その定数のわざとの変更であるべき — 一方で `af_cfn_deploy` は実行時に測り続けるので、
越えたテンプレートも死なずにデプロイされる。

#### 30-ingress はまた壁を越えてよい（2026-11-11、#3550）

壁の内側に留まる余地が尽きた: `30-ingress.yaml` はテストの予算 50,176 バイトの 200 バイト下にあり、説明文はすでに
出してあり（上）、新しいパラメータは 1 つごとに — `Parameters` の項目、条件、`Environment` の行 — 300〜500 バイトかかるので、
足す PR はどれも CI に落ちて手で削っていた。S3 なしで場所を作れるのはネストしたスタックか 2 つ目のテンプレートだけで、
スタックの間でリソースを動かすと作り直しになる。ALB とそのリスナーは作り直してはいけないので、それはできない。

そこで S3 の経路は、**このテンプレートに限って**、安全網であるだけではなくなった: 30-ingress は必ず 20-platform の
S3 Bucket ができたあとにデプロイされ（構築の順番 00 → 10 → 20 → イメージ → 40 → 30）、それが S3 Bucket より前のテンプレートにはない性質。
変えたこと:

- `cfn-cp-settings-env-test.py` の 30-ingress の予算: 51,200 − 1,024 → **65,536**（約 14 KiB、パラメータ 30 個あまりの余地）。また上げるのも、わざとの変更のまま。
- ケース **3b-2** は 30-ingress を 51,200 の上限から外す（ほかはそのまま）。`cfn-template-size-test.py` は S3 Bucket より前のテンプレートを上限の 4 KiB 下に保つ。
- この README・`PARAMETERS.md`・`release-ecr.sh` のヒントにある、30-ingress を手で打つ `aws cloudformation deploy`（`--template-file`）は、
  すべて `--s3-bucket "$CFN_BUCKET" --s3-prefix cfn` を付けた（AWS CLI は 51,200 バイトを超える直接のアップロードを API を呼ぶ前に拒否する）。
  付いていないものがあれば `cfn-template-size-test.py` が失敗する。`CFN_BUCKET` は 20-platform の出力 `CfnTemplatesBucket`:
  `CFN_BUCKET=$(aws cloudformation describe-stacks --stack-name <platform stack> --query "Stacks[0].Outputs[?OutputKey=='CfnTemplatesBucket'].OutputValue" --output text)`。
- テンプレート自体は変えていない: 論理 ID・パラメータ・リソースはどれも動いていないので、今のスタックはその場で更新される。

#### 往復を測った（2026-09-02）

撤去 → 構築を端から端まで通したことはなかった。開発用デプロイで、`0.14.3-dev-ecbbcc59` で通した:

| 手順 | 結果 |
|---|---|
| `dev-image.yml`、`image=both`、2 アーキテクチャ | 約 11 分。両方のイメージが `[amd64, arm64]` のインデックス |
| `standup.sh` 00 → 10 → 20 → イメージ → 40 → 30 | `30-ingress` は S3 を通り、`CREATE_COMPLETE` |
| CP の起動 | `metadata store: postgres`、`/readyz` 200 |
| Agent、両アーキテクチャ | ゴールデンの焼き付けが本物のワークスペースを起動: `Agent healthy` 99 秒（arm64）/ 101 秒（x86_64） |
| CP → Agent | Cloud Map への切り替えが働いて成功: `did not resolve through the Service Connect alias -> using 10.20.x.x:7700 from Cloud Map` |
| `teardown.sh` | 5 つのスタックが消え、掃除と独立した数え上げのどちらも 0 スタック / 0 NAT / 0 RDS / 0 ALB / 0 EC2 / 0 EFS / 0 ECR |

⚠️ **`/healthz` ではなく `/readyz` を見る。** `/healthz` はデータベースが死んでいても 200 を返す — RDS のローテーションの障害が
長く気づかれなかったのはそのため。データベースに触るのは `/readyz`。

途中で見つかった粗い所が 2 つあり、**どちらも直した**:

- **`standup.sh` の事前チェックは、キャプチャをテンプレートと突き合わせる**（`Default` のないパラメータがあること）。
  以前はキャプチャの*ファイルがあるか*だけを見ていたので、テンプレートより古いキャプチャは 00–20 を建てた**あとで**失敗した —
  いちばん高くつく時点。そのパラメータを固定のインデントで数えることもやめた: `Parameters:` の下の最初のキーの深さを取り、
  **1 つも読めなかったときはそう言う**。「必須 0 個」と黙って報告するパーサーは、上の壁と同じ形 — 動いていないのに何も見えないチェック。
- **`teardown.sh` の S3 Bucket を空にする手順は、失敗した理由を表示する。** 以前は stderr を捨てていた
  （`>/dev/null 2>&1 || echo "(skip: …)"`）ので、「S3 Bucket はもうなかった」と「空にできなかった」が同じに見えた —
  そして次に失敗するのは 20-platform の削除で、その時点で理由は捨てられている。

### 既知の挙動: コールドスタートの Start は `running` ではなく `starting` を返す

コールドのワークスペースの Start は、起動のたびにイメージ全体を取得する（Fargate はタスクの間でイメージをキャッシュしない）。
サンドボックスで端から端まで測った値（2026-08-15、`docs/log/62` §62.9）: **新しいホームで 126 秒、使ったホームで 128〜218 秒** —
以前の「約 100 秒」は楽観的だった。`POST /api/workspace/start` はそれを**待たない**: ECS のサービスが `desiredCount 1` になった
時点で、今の状態 — ふつうは `starting` — を返す。`running` への移行は `GET /api/workspace` をポーリングして見る。
Console はすでにそうしている（4 秒間隔で、再読み込みなしにコールドスタートを `running` まで進める）。

古いビルドは、リクエストを Agent の準備待ち（`AF_ECS_START_TIMEOUT_SEC`、当時 90 秒）で止めていたので、ALB の **60 秒**の
アイドルタイムアウトを超えた — ワークスペースは裏で問題なく収束しているのに、*HTTP リクエスト*は **504** で死んだ。
`control-plane/runtime_ecs.go`（`watchReady`）で直した: 準備のポーリングは今は裏でログの 1 行
（`ecs start: service <name> Agent healthy <n>s after Start`）のためだけに動き、その予算はどのリクエストよりもわざと長い
（既定 **300 秒**）。`30-ingress.yaml` は、上限が見えるように ALB の `idle_timeout` を既定の 60 秒のまま明記した。
🚧 単体テストでだけ確かめた — 本物の ECS のスタックではやり直していない。

ここでの `running` は ECS の**タスク**が RUNNING という意味で、エントリーポイントが終わって Agent が応答する少し前。
その間に出した作成は `502 workspace agent unreachable` になる。やり直す。

**内訳は測ってある**（2026-08-15、`docs/log/62-ecs-start-latency.md` §62.9–62.10）。答えはイメージではなかった。
使ったホームの Stop→Start は **約 101 秒**: ECS がタスクを作るのに **4〜8 秒**、準備（ENI の取り付け）**16 秒**、
イメージの取得 **35 秒**（圧縮で 918 MiB、34 レイヤー）、EFS のマウントとコンテナの作成 **約 25 秒**、エントリーポイント **21 秒**。
イメージの取得はその約 35% — 判断の基準の 40% より下、絶対値でも 40 秒より下 — なので、SOCI の遅延読み込みは**見送った**。

⚠️ **測るときの注意（痛い目を見て学んだ）**: 止めた*直後*（1〜2 分以内）にワークスペースを起動し直すと、前のタスクの片付け中に
新しいデプロイが重なるので、ECS はタスクを作るだけで **40〜143 秒**かかる。これは計測のループの性質で、本番の性質ではない
（ワークスペースはリーパーが止め、数分から数時間後に起動される）。**再起動の数字を出すときは、止めてから起動するまでの間隔を必ず書く。**
何かが変わったら、同じやり方で測り直す:

```bash
# image pull window (and the surrounding task lifecycle)
aws ecs describe-tasks --cluster <cluster> --tasks <task-arn> \
  --query 'tasks[0].{created:createdAt,pullStart:pullStartedAt,pullStop:pullStoppedAt,started:startedAt}'
# entrypoint cost (boot-install of the pinned CLIs on a fresh home is NOT a pull)
aws logs tail <ws-log-group> --since 10m
```

ここで push したワークスペースのイメージは **軽量版**（`BAKE_AGENT_CLIS=0`）なので、*新しいホーム*ではエントリーポイントの中で
npm / GitHub からの起動時のインストールが 1 回だけ加わる（全 CLI で約 60 秒、`workspace/entrypoint.sh` で測った）。
これはイメージの取得ではなくネットワークの時間で、繰り返さない — `~/.local` は EFS に残る。だから**2 つ**の場面を測り、
2 つ目で判断する: 空のホームでの最初の Start（取得 + インストール）と、使ったホームの Stop→Start（取得だけ）。
スケールトゥゼロが毎回払うのは後者。

EFS やエントリーポイントの雑音なしに取得だけを切り出すには、同じイメージで `entryPoint` を上書きした使い捨てのタスク定義を登録する
（`run-task --overrides` は `command` を上書きできるが `entryPoint` は**できない**。`entrypoint.sh` は `exec "$@"` で終わるので、
コマンドだけを上書きしてもエントリーポイント全体が動く）。`run-task` は作りからしていつもコールド — Fargate はタスクの間で
イメージをキャッシュしない。手順の全体、差分の計算、判断の基準は `docs/log/62-ecs-start-latency.md` §62.7 にある
（SOCI の結論: この計測を条件に、条件付きで可）。
