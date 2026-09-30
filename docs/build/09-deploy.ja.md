---
audience: "デプロイ形態やアダプタを足す人"
source_of_truth: "コード ＋ 各 runbook（`deploy/*/README.md`）"
updated: "2026-09"
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

- **ec2-single は VM 上の compose** — ランタイムは `docker` で、別のプロファイルではない。
- 認証モードの中身は [07 §7.3](07-security.ja.md) が正。ここでは繰り返さない。

## 9.2 ポート&アダプタ — 何をどのノブで差し替えるか

**コアは全ターゲットで同一物**で、差し替わるのは CP 内の interface seam のみ（seam の一覧は
[01 §1.6](01-architecture.ja.md)）。本節はその選択ノブ側:

| ポート（seam） | 切替ノブ | 選択肢 |
|---------------|----------|--------|
| `Runtime` / `RuntimeFactory` | `AF_RUNTIME` | 空・`local`・`docker` = Docker Engine（既定）/ `ecs`・`aws` = Fargate 上の ECS / `ecs-ec2` = プールの EC2 スロット上の ECS（別名なし）/ `native`・`wsl` = サンドボックス化したホストプロセス（**`AUTH=dev` 必須**）。**未知値は起動時に fail-fast**（`unknown AF_RUNTIME profile`・`runtime.NewFactory`） |
| `Store` | `AF_DB`（SQLite のパス）/ `AF_DATABASE_URL`、または `AF_DB_HOST` ほか `AF_DB_*` | SQLite（既定・pure Go）/ Postgres |
| `KeyCustodian` | `AF_MASTER_KEY` の有無 | 設定時 = ローカル custodian / 未設定 = 暗号化なし（開発専用）。KMS / Vault は 📋（[decisions/0005](../decisions/0005-envelope-custodian.ja.md)・#969） |
| `AuthGateway` | `AUTH` | `dev`（未設定時の既定）/ `oauth`（compose と AWS のテンプレートが設定する）/ `proxy`（[07 §7.3](07-security.ja.md)） |
| エンジン | エンジン表: `AF_ENGINES_SSM_PARAM` か `AF_ENGINES_JSON`、加えて `AF_LLM_URL` / `AF_COMFY_URL` からの役割ごとの 1 行 | AWS では CP が要求時に起動するエンジン。どこでも、ネットワーク上で既に動いているサーバを URL で指せる |
| Ingress / TLS | CP 外 | Caddy（compose）/ Tailscale Funnel（local）/ ALB + ACM（aws） |

## 9.3 入口（ingress）の選択肢と「入口からしか届かない」不変条件

**不変条件: CP には入口を通してしか届かない。** 1 台のホストでは CP は loopback に bind する —
イメージと compose は `CP_ADDR=127.0.0.1:8099` を設定する。コードの既定（`:8080`）と
`run-dev.sh` の既定（`:8099`）は全インタフェースに bind するので、開発ホストで 1 人使うなら
よいが、共有するなら誤り。AWS では CP タスクは自分のネットワークインタフェース内で `0.0.0.0` に
bind し、そのセキュリティグループはロードバランサのものだけを通す。

入口の仕事は TLS 終端と転送。`AUTH=oauth` では認証も CP 自身が担い、`AUTH=proxy` のときだけ
入口側が identity ヘッダを注入する。

| 入口 | 使いどころ | 備考 |
|------|-----------|------|
| **Caddy** | compose 標準 | `PUBLIC_DOMAIN` の DNS を向けるだけで Let's Encrypt 自動取得・更新（WS も透過）。CP と両方 host-net で loopback に到達。既存プロキシで前段する社は外せる（Caddyfile 代替2）|
| **Tailscale Funnel** | local 運用の一形態 | Funnel → `127.0.0.1:8099` 直結 |
| **ALB + ACM** | aws | TLS 終端のみ — 認証は CP に残る。`30-ingress.yaml` の `AuthMode` は `oauth`（既定）か `dev` だけを許し、テンプレートはロードバランサの OIDC を設定しない |

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
| CP コア | `CP_ADDR`（`:8080`）・`CONSOLE_DIR`・`AF_RUNTIME`（`local`）・`AF_DB`（`<WS_DATA>/control-plane.db`）・`PUBLIC_BASE_URL`・`AF_PREVIEW_DOMAIN`・`AF_TRUSTED_PROXY_HOPS`（0） | bind 先・配る Console・アダプタの選択・外部 URL・プレビューのサブドメイン・送信元アドレス | 本章 |
| Workspace 起動テンプレ | `WS_IMAGE`・`WS_DATA`（`/tmp/af-data`）・`WS_MEMORY`（`1g`）・`AF_MAX_WORKSPACE_MEM`・`WS_AGENT_PORT`（7700・Workspace ごとのポートの起点）・`WS_AGENT_HOST`（`127.0.0.1`）・`WS_JVM_DIR`・`WS_ENV`・`WS_SESSION_CMD` | CP が Workspace を起動するときに流し込む共通テンプレ。`WS_ENV` が届くのは `docker` と `native` の Workspace だけで、ECS 系ランタイムは渡さない | [04](04-agent.ja.md) |
| L1 認証 | `AUTH`（`dev`）・`DEV_USER`（`dev`）・`AUTH_EMAIL_HEADER`（`X-Forwarded-Email`）・`GOOGLE_OAUTH_CLIENT_ID/SECRET`・`AF_GITHUB_LOGIN_CLIENT_ID/SECRET`（または `GITHUB_OAUTH_CLIENT_ID/SECRET`）と `AF_GITHUB_ALLOWED_ORGS` ほか `AF_GITHUB_*`・`AF_OIDC_PROVIDERS` ＋ `AF_OIDC_<ID>_{ISSUER,CLIENT_ID,CLIENT_SECRET,TRUST,LABEL_JA,LABEL_EN,SCOPES,PROMPT,LINK_CLAIM,ALLOWED_EMAILS,ALLOWED_DOMAINS,ALLOWED_TIDS}`・`AF_COOKIE_SECRET`・`AF_SESSION_TTL`（168h）・`AF_OAUTH_ALLOWED_{EMAILS,DOMAINS,EMAILS_FILE}` | Console ログイン。`AUTH=oauth` は有効な provider が無いと起動しない。OIDC の provider は `TRUST` の宣言が、GitHub は `AF_GITHUB_ALLOWED_ORGS` が必要で、無ければその provider は無効になる。**どの入口も受け入れないサインインは拒否される**: 入口はこれらの許可リスト・テナントの名簿・テナントの auto-join ドメイン・承認済みのテナント IdP。どれも無ければ全ログインが拒否される | [07 §7.3](07-security.ja.md) / [decisions/0043](../decisions/0043-login-idp.ja.md) |
| プロビジョン / 権限 | `AF_PROVISION`（`auto`）・`SUPER_ADMIN_EMAILS` | 未知の identity をどう受け入れるか / 誰がデプロイ管理者か | [06](06-data.ja.md) |
| at-rest 暗号 | `AF_MASTER_KEY` | 未設定 = 平文（開発専用）。**紛失 = crypto-shred** — データとは別の金庫に置く | [07 §7.6](07-security.ja.md) |
| git プロバイダ OAuth | **env は無い** | テナント管理者が Console で登録する。`BITBUCKET_OAUTH_KEY/SECRET` はもう読まれず、`GITHUB_OAUTH_CLIENT_ID` はサインイン専用 | [decisions/0052](../decisions/0052-tenant-git-oauth.ja.md) |
| scale-to-zero / showback | `AF_AUTOSTART`（on）・`AF_SESSION_IDLE_TIMEOUT`（1h）・`AF_INTERACTION_IDLE_TIMEOUT`（session の値）・`AF_WS_IDLE_TIMEOUT`（2h）・`AF_PRESENCE_IDLE_TIMEOUT`（30m）・`AF_IDLE_SWEEP_INTERVAL`（1m）・`AF_STOP_GRACE_SEC`（30・上限 120）・`AF_USAGE_SAMPLE_INTERVAL`（5m） | 自動起動・アイドル停止・停止猶予・利用量サンプリング。アイドルのタイムアウト・掃引・usage サンプラーは `0` で無効 | [03](03-control-plane.ja.md) |
| MCP | `AF_MCP_ENABLED` | `/mcp` がそもそも存在するか。有効になるのは文字列がちょうど `true` のときだけ | [08](08-integrations.ja.md) |
| egress | `AF_EGRESS_LISTEN`（`:3128`）・`AF_EGRESS_TOKEN`・`AF_EGRESS_{INGEST,POLICY}_URL`・`AF_EGRESS_PROXY_ADDR`・`AF_EGRESS_ENFORCE`・`AF_EGRESS_ALLOWLIST` | forward proxy サブコマンドと CP の集約。`AF_EGRESS_PROXY_ADDR` がプロキシ変数を注入するのは `docker` と `native` の Workspace だけ | [07 §7.8](07-security.ja.md) |
| Postgres | `AF_DATABASE_URL`、または `AF_DB_{HOST,PORT,USER,PASSWORD,NAME,SSLMODE}`、それと**パスワードの真値が居る場所** `AF_DB_PASSWORD_SECRET_ARN` / `AF_DB_PASSWORD_SECRET_KEY` | Store が Postgres のときだけ。部品から DSN を組む。ARN は、ローテートされたパスワードを**タスクを作り直さずに**拾うためのもの（§9.9） | [06](06-data.ja.md) |
| ECS アダプタ | `AF_ECS_{CLUSTER,REGION,SUBNETS,SECURITY_GROUP,NAMESPACE_ARN,EFS_ID,EXEC_ROLE,TASK_ROLE,INFRA_ROLE,LOG_GROUP,WORKSPACE_IMAGE,TASK_CPU,TASK_MEMORY,WS_DISK_GB,POSIX_UID,POSIX_GID,START_TIMEOUT_SEC}` | テンプレートが作った静的基盤の座標。`ecs` も `ecs-ec2` も読む | [ecs runbook](../../deploy/aws/ecs/README.md) |
| EC2 スロットプール | `AF_ECS_EC2_LAUNCH_TEMPLATE`（必須）・`AF_ECS_EC2_SLOT_TYPES`・`AF_ECS_EC2_DEFAULT_SLOT_CLASS`・`AF_ECS_EC2_AMI_ARM64`・`AF_ECS_EC2_MAX_SLOTS`（8）・`AF_ECS_EC2_HOME_GB`（50）・`AF_ECS_EC2_SLOT_SLEEP_SEC`（900）・`AF_ECS_EC2_SLOT_TERMINATE_AFTER_SEC`（0 = しない）・`AF_ECS_EC2_HIBERNATE_AFTER_SEC`（0 = 無効）・`AF_ECS_EC2_BACKUP_EVERY_SEC`（0 = 無効）・`AF_ECS_EC2_BACKUP_KEEP`（3）・`AF_ECS_EC2_GOLDEN_AUTOBAKE`（on）、ほかに掃引とタイミングのノブ `AF_ECS_EC2_*_SEC` | `ecs-ec2` 専用: スロットの型と上限・home の大きさ・§9.5 のアイドル段 | [ecs runbook](../../deploy/aws/ecs/README.md) §Optional: EC2 slot pool / [decisions/0045](../decisions/0045-ec2-persistent-workspace.ja.md) |
| エンジン | `AF_ENGINES_SSM_PARAM` / `AF_ENGINES_JSON`・`AF_LLM_URL`・`AF_COMFY_URL` / `AF_COMFY_API_KEY`・`AF_ENGINE_API_KEY_<KEY>`・`AF_ENGINE_<KEY>_{CONTROL_INTERVAL_SEC,WINDOW_SEC,IDLE_SEC,START_DEADLINE_SEC,FAIL_COOLDOWN_SEC}`・`AF_ENGINE_ECS_CLUSTER`・`AF_ENGINE_WAKE_TIMEOUT`（900 秒）・`AF_ENGINE_PLAIN_HOLD`・`AF_REMOTE_ENGINE_{URL,TOKEN,KEYS}` | エンジン表・エンジンの制御器・冷えたエンジンに対するゲートウェイの保留・別デプロイのエンジンの借用 | [decisions/0071](../decisions/0071-self-hosted-inference-engines.ja.md) / [0076](../decisions/0076-external-image-engine-on-lan.ja.md) / [0077](../decisions/0077-engine-boxes-bought-by-cp.ja.md) / [0079](../decisions/0079-remote-engine-from-another-deployment.ja.md) |
| 音声 | `AF_VOICEVOX_URL`（`http://127.0.0.1:50021`）・`AF_TTS_ECS_SERVICE` ほか `AF_TTS_ECS_*`・`AF_TTS_MAX_CHARS`（300）・`AF_POLLY_{REGION,ENGINE}` | URL で指す VOICEVOX、または CP がゼロから起こす ECS 上の VOICEVOX。Amazon Polly | [decisions/0070](../decisions/0070-tts-ondemand-engine.ja.md) |
| コンテナレスアダプタ | `AF_NATIVE_AGENT_BIN`（`PATH` 上の `workspace-agent`）・`AF_NATIVE_ROOTFS`・`AF_NATIVE_BWRAP` | Agent バイナリの所在・bubblewrap サンドボックスを有効にする rootfs | [native runbook](../../deploy/native/README.md) |
| Workspace 内（CP が注入・**運用者は設定しない**） | `AGENT_TOKEN`・`AF_SECRET_KEY`・`AGENT_STOP_GRACE_SEC`・`AGENT_SESSION_CMD`・`CLAUDE_CONFIG_DIR`・`AF_AGENT_SELF_UPDATE_ALLOWED`・`AF_CP_BASE_URL` と機能ごとのトークン（`AF_DOCS_TOKEN`・`AF_MCP_TOKEN`・`AF_MEMO_TOKEN` …）・`native` ではさらに `AGENT_ADDR`・`AF_TMUX_SOCKET`・`AGENT_DOCS_DIR` | CP↔Agent 認証・DEK・停止猶予・Agent から CP への経路（`manager.workspaceExtraEnv`）。トークンと DEK は `docker` では 0600 の env ファイル、ECS では SSM SecureString のタスクシークレットで渡る | [04](04-agent.ja.md) / [07 §7.5](07-security.ja.md) |

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
が Adoptium から Temurin を入れる。Console で Java 版を選ぶと、entrypoint が未導入分を入れて
`JAVA_HOME` を通す。`GET /env/toolchains` は両ディレクトリにあるもの ∪ 導入可能なもの
（`java_available`）を示し、**未導入の版を選ぶとその場で入れるボタンが出る**（`POST /env/jdk-install`、
その後 `GET` でポーリング）。導入後は `resolvedToolchains` が起動のたびにディレクトリを glob するので、
再起動なしで**次のセッションから**効く。

## 9.5 aws ターゲット

CloudFormation スタックは 7 本で、配備順は `00-network → 10-data → 20-platform →
（40-ec2-pool）→（50-tts）→（60-engines）→ 30-ingress`（括弧は任意）。各スタックが何を持つかは
runbook の「Stack decomposition」。

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
- **Runtime 契約の `starting` 状態は実質 ECS 専用。** 収束待ちの間、呼び出し側は再 Start も
  アイドル停止もしない。Docker アダプタは秒で上がるので報告しない。**Start は Agent を待たずに
  返る**: `ecs` ではサービスの desired count を設定した時点で、`ecs-ec2` ではそれより前のことも
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

| 観点 | docker / compose | native | ecs（Fargate） | ecs-ec2 |
|------|------------------|--------|----------------|---------|
| scale-to-zero | コンテナの stop / start | プロセスの stop / start | desired 0/1 | desired 0/1、その先は §9.5 のアイドル段 |
| 隔離 | コンテナ境界（カーネル共有） | bubblewrap サンドボックス・1 人 | ホストを共有しないタスク | 同時には他の誰も使わないインスタンス上のタスク |
| egress | コンテナのネットワーク、任意で forward proxy（[07 §7.8](07-security.ja.md)） | ホストのもの | セキュリティグループ | セキュリティグループ |
| home の置き場 | ローカルディレクトリ（速い） | ローカルディレクトリ | EFS: **git のようにメタデータ操作の多い作業は遅い** | EBS。資格情報は EFS |
| 基盤権限 | Docker ソケットはホスト root 相当（[07 §7.1](07-security.ja.md)） | 利用者自身のアカウント | 最小のタスクロール・インスタンスメタデータ無し | 最小のタスクロール |

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
  [compose runbook](../../deploy/compose/README.md)、ECS は [ecs runbook](../../deploy/aws/ecs/README.md)
  の §Upgrade。

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
