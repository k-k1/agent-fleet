# 20. P3-10 実装プラン — パッケージング & 配布 & アップグレード（オンプレ compose）

> 🗄 **歴史的記録（初期プラン）— 実装は [docs/35 パッケージング](../log/35-packaging.md) で実施**。現状は [HANDOFF](../HANDOFF.md)、方針は [ロードマップ P3-10](../roadmap.md#p3-10-パッケージング--配布--アップグレード提供モデルの核) / [decisions/0001](../decisions/0001-self-host-vs-saas.ja.md)。

提供モデル（グループ各社が自社でセルフホスト）の**核**。P3-1〜P3-9 の機能を「他社の情シスがゼロから設置・運用・更新できる形」にする。今回は**オンプレ Docker/compose 一本**に絞る（AWS は P3-7 で後追い）。**完了ゲート = 第2デプロイをクリーン環境にゼロから立てて E2E 通過**。

## 20.0 提供モデル（OSS コア＋サービス）

P3-10 の配布は**戦略と不可分**なので前提として据える（2026-07-01 議論）。
- **OSS コア＝自己ホストの土台**（オンプレ compose=P3-10 / AWS ECS=P3-7）。**OSS 化する方向は確定**、ライセンスは AGPL-3.0（＋商標留保・dual-license 余地）か Apache/MIT（最大採用）で**保留**。決め手＝「managed/hosted の商用エッジをコピーレフトで守りたいか」。本製品は ToS 摩擦が第三者 SaaS 化を既に抑止するため AGPL の動機はやや弱く、採用/信頼最大化なら permissive も妥当。**初回公開前に確定**（外部貢献後の再ライセンスは困難＝一方通行）。単一著者の今が選び時。
- **商用＝厚いサポート＋カスタマイズ＋"managed self-host"**（我々が運用代行するが**顧客の infra/AWS で・顧客の Claude シートで**＝ToS クリーン維持）。カスタマイズ＝ports&アダプタ＋エージェント kind＋ブランドの受け皿で実装。
- **信頼シグナル**: 資格情報（Claude/GitHub/Bitbucket を封筒暗号で保管）を扱うツールのソース公開＝各社が暗号実装を監査可能＝採用の強み。
- **含意**: repo 衛生（LICENSE・秘密の非同梱・再現ビルド）を P3-10 の一部に含める（§段4）。core に Docker 前提を染み込ませない（§段0）。

## 20.1 確定した前提

- **TLS/ingress = Caddy 同梱**（compose に Caddy サービス、公開ドメインで Let's Encrypt 自動取得＝ACME、自己署名フォールバック記載）。各社はドメイン+DNS のみ用意。「各社の既存プロキシで前段」は代替として軽く文書化。
- **認証 = `AUTH=oauth`**（CP ネイティブ Google OAuth）既定。外部 oauth2-proxy は任意。
- **MetadataStore = SQLite 既定**（小規模＝数十〜百ユーザー、[p3-1-metadatastore](p3-1-metadatastore.md)）。Postgres は AWS/HA 時に港の裏で後追い。
- **phone-home しない**。各社デプロイは我々の中央基盤に一切依存しない。

## 20.2 現状のギャップ（コード事実）

- **CP に Dockerfile が無い**＝現状ホストバイナリ運用（`run-dev.sh`/`restart-cp.sh` が `go build -o /tmp/af-cp` してホストで実行）。→ **CP のコンテナ化が最初の一手**。
- **CP は host の `docker` CLI を exec** して workspace を起動（`runtime.go` `dockerRuntime`）＝コンテナ化すると **docker-out-of-docker（DooD）** が必須（`docker` CLI 同梱＋`/var/run/docker.sock` マウント）。
- **CP は `WS_DATA` を直接 read/write する**（`AF_DB` 既定＝`<WS_DATA>/control-plane.db`、`backfill` の `os.ReadDir`、`cleanHome`）**かつ** 同じ `WS_DATA` を workspace の `docker run -v` に渡す。DooD ではこのバインドマウントを**host デーモンが解決**するため、CP コンテナ内でも **host と同一パスで** `WS_DATA` をマウントし、両者を一致させる必要がある（下記の肝）。
- マイグレーションは **go:embed の自前冪等マイグレータ**（0001〜0007、CP 起動時に適用）＝外部ツール不要。roadmap の goose 想定は**現行踏襲でよい**（配布が1バイナリで済む・後方互換のみ守る）。
- 港の抽象: KeyCustodian（P3-3）と MetadataStore は IF 化済。Runtime は Docker 具象のみ（P3-7 で抽出）。

## 20.3 肝：DooD が課す3つの正しさ制約

CP をコンテナ化して host docker.sock を叩くと、「起動はするが静かに動かない」失敗を生む3制約が出る。段1 の完了条件で必ず検証する。

### (A) agent 到達性 → **CP は `network_mode: host`**（最重要）
CP が発行する `docker run -p 127.0.0.1:<port>:7700` は **host デーモン**が実行するので publish 先は **host の 127.0.0.1**。bridge 網の CP コンテナは別 netns ゆえ `127.0.0.1:<port>` に**到達できない**。
→ **CP を `network_mode: host` で動かす**と host loopback を共有し publish 先にそのまま届く。loopback-only の既存セキュリティ不変条件（HANDOFF §6.8 B1）も保たれ、**コード改修ゼロ**。将来の綺麗な解は `Runtime.Endpoint` 港を「container 名:7700 をワークスペース網 join した CP から叩く」方式に改修（AWS Service Connect と同型）だが v1 では過剰。

### (B) bind-mount パス同一性 → **CP 内 `WS_DATA` = host と同一絶対パス**
CP が `docker run -v <home>:/root` を **host デーモン**に渡すので `<home>` は host パスとして解決される。CP コンテナ内で home/DB を作る `WS_DATA` が host の bind ソースと**同一絶対パスでない**と、workspace が空 home をマウント（データ喪失に見える）。
→ compose で host `/srv/agent-fleet/data` を CP コンテナの**同一パス**に bind し `WS_DATA=/srv/agent-fleet/data`。`WS_JVM_DIR` も同様。`docker.sock` も同マウント。

### (C) uid/gid 整合
CP は `os.MkdirAll(home,0755)` で home を作り、workspace は uid 1000（`dev`）で読み書きする。CP プロセスの uid が作成物 owner になるので **CP を uid 1000 で動かす**（`user: "1000:1000"`）＋ docker.sock 権限のため host の docker gid を `group_add`（`DOCKER_GID` を `.env` 変数化＝host 依存）。

## 20.4 要決定（選択肢＋推奨）

| 論点 | 選択肢 | 推奨（v1）|
|------|--------|-----------|
| **docker.sock 方式** | (a) 素の `/var/run/docker.sock` マウント（DooD）(b) rootless docker (c) socket プロキシ(tecnativa/docker-socket-proxy)で API 絞り | **(a) 素マウント**。既知の「CP=ホスト root 相当」リスクの顕在化にすぎず新規リスクではない。runbook で明記＋(c) を「強化オプション」として案内。会社間は別デプロイゆえ波及なし。 |
| **レジストリ（イメージ配布）** | (a) 我々の社内レジストリから pull (b) 各社の自前レジストリへ push して pull (c) `docker save/load` の tar 同梱 | **(a)+(c) 併用**。ネット可＝(a)、エアギャップ＝(c)。compose は `image: <registry>/agent-fleet/{cp,workspace}:<tag>` を .env の `REGISTRY`/`VERSION` で解決。 |
| **DB 既定** | (a) SQLite (b) Postgres 同梱 | **(a) SQLite**（小規模）。Postgres は AWS/HA/大規模で港の裏に後追い（今回スコープ外）。 |
| **CP/Caddy の網** | (a) 両方 host-net (b) Caddy=bridge+CP=host-net | **(a) 両方 host-net**。Caddy→127.0.0.1:8099 が自然に届き §20.3(A) も満たす。bridge Caddy だと host-net CP の loopback bind に届かず CP を 0.0.0.0 bind せねばならず loopback 不変条件が崩れる。 |
| **workspace イメージの claude** | 既存踏襲（entrypoint で起動時 install＝常に最新）| 踏襲。エアギャップ社向けに `CLAUDE_INSTALL=0`＋焼き込み版フォールバックを文書化。 |

## 20.5 Increment 分割（依存順）

### 段0 — Runtime インターフェース抽出（ECS の継ぎ目・P3-10 と並行で先に）
**目的**: `dockerRuntime` 具象を `Runtime` インターフェースの背後へ回し、compose(local Docker) を第1実装、将来の ECS を第2実装として同じ継ぎ目に載せる。P3-10(A) の Docker 前提を core に染み込ませず、P3-7「顧客 AWS で managed」を塞がない。
- **純リファクタ（挙動不変）**: `Runtime` IF（`Start/Stop/State/Endpoint`＝CP↔Agent 到達 URL を吸収、host-loopback↔Service Connect の差はここ）を定義し、`dockerRuntime` を local 実装に。`manager.go`/`runtime.go`/`proxy.go` 等の呼び出し側を IF 経由に。`agentBase()` は `Runtime.Endpoint()` へ。
- **完了条件**: **挙動完全不変**（既存 live で `go build`＋`go vet`＋実機疎通）。ECS 実装は空 or stub（P3-7 で実装）。
- **リスク**: 到達ロジック（`127.0.0.1:port` publish）を IF に切り出す際の取りこぼし＝実機回帰で検証。**低リスク（内部リファクタのみ）**。

### 段1 — CP コンテナ化 + 最小 compose（ゼロ起動の骨）
- **新規 `control-plane/Dockerfile`**: マルチステージ（`golang:1.26-bookworm` でビルド → slim ランタイム）。`console/dist` を同梱（ビルド済を COPY、または Node ステージを含める）。ランタイムに **`docker` CLI** を入れる（DooD 用、静的 or `docker-ce-cli` パッケージ）。`AF_DB`/`WS_DATA` は host 同一パス前提。
- **新規 `deploy/compose/docker-compose.yml`**: `cp` サービス（**`network_mode: host`**〔§20.3(A)〕、**`user: "1000:1000"`**＋**`group_add: [${DOCKER_GID}]`**〔(C)〕、`env_file: .env`、`-v /var/run/docker.sock:/var/run/docker.sock`、`-v ${DATA_DIR}:${DATA_DIR}` 同一パス〔(B)〕、`WS_DATA=${DATA_DIR}`、`CP_ADDR=127.0.0.1:8099`、`restart: unless-stopped`）。まず TLS 無し・`127.0.0.1:8099` で E2E を通す。`.dockerignore`（node_modules/dist/*.db 除外）も新規。
- 完了条件: `docker compose up` → ブラウザ → Start → shell セッション疎通。
- 触る/新規: `control-plane/Dockerfile`（新）、`deploy/compose/docker-compose.yml`（新）、`.env.example`（新, §段4 で拡充）。**CP コード無改修**（env は既存のまま）。

### 段2 — Caddy 同梱（ACME 自動 TLS）
- compose に `caddy` サービス追加。`Caddyfile`（`${PUBLIC_DOMAIN} { reverse_proxy control-plane:8099 }`＝ACME 自動）。CP は内部ネットワークのみ公開（ホストポート閉じる）。`PUBLIC_BASE_URL=https://${PUBLIC_DOMAIN}`。
- 自己署名フォールバック（`tls internal`）と「既存プロキシで前段する」代替を Caddyfile コメント/runbook に。
- Google OAuth の `redirect_uri` は `PUBLIC_BASE_URL/oauth2/callback`＝各社が自社 Google Console で登録（runbook 手順）。
- 完了条件: 公開ドメインで https → Google ログイン → Console。

### 段3 — バックアップ/復元（P3-9 残・価値の本体）
- 対象: `${DATA_DIR}` 配下（per-user home・`secrets.enc`・wrapped DEK）＋ `control-plane.db`。
- **鍵の明記**: `AF_MASTER_KEY` を失う = 全 wrapped DEK が unwrap 不可 = **crypto-shred（全復号不能）**。マスタ鍵は DB/home と**別管理**（バックアップ対象だが別金庫）。
- 新規 `deploy/compose/backup.sh` / `restore.sh`（コンテナ停止 or WAL チェックポイント→ rsync/tar → 復元）。runbook に世代・保管方針。
- 完了条件: バックアップ→クリーン環境で復元→同一状態で起動（セッション/接続復活）。

### 段4 — 設定一元化 + リリースバンドル + runbook
- **`.env.example`**（`oauth.env.example` を基に単一化）: `REGISTRY`/`VERSION`、`PUBLIC_DOMAIN`/`PUBLIC_BASE_URL`、`GOOGLE_OAUTH_CLIENT_ID/SECRET`、`AF_OAUTH_ALLOWED_DOMAINS/EMAILS[_FILE]`、`AF_COOKIE_SECRET`、`SUPER_ADMIN_EMAILS`、`AF_MASTER_KEY`、`DATA_DIR`、`WS_MEMORY`、`WS_IMAGE`、idle-stop 既定（`AF_*_IDLE_TIMEOUT`/`AF_IDLE_SWEEP_INTERVAL`）、`AF_MCP_ENABLED`。**秘密は同梱しない**（例値＋生成手順）。
- **リリース**: タグ付き `cp`/`workspace` イメージ＋`docker-compose.yml`＋`Caddyfile`＋`.env.example`＋`backup/restore`＋runbook を1バンドル（GitHub Release or tar）。`docker save` tar もエアギャップ向けに。
- **アップグレード**: 新 `VERSION` に差し替え → `compose pull` → `compose up -d`（CP 起動時に go:embed マイグレータが後方互換適用、home/DB 保持）。ダウングレード不可点は release note。
- **runbook** 目次: 前提（Docker/ドメイン/DNS/Google OAuth）→ `.env` 作成（`AF_MASTER_KEY`/`AF_COOKIE_SECRET` 生成）→ `compose up` → 初回 super_admin（`SUPER_ADMIN_EMAILS`）→ テナント/メンバー作成 → バックアップ → アップグレード → 障害対応（CP ログ・healthz・docker.sock 権限）。
- **OSS repo 衛生（§20.0、公開する場合）**: `LICENSE`（確定ライセンス）・`README`（クイックスタート＝このバンドル）・`CONTRIBUTING`・`SECURITY.md`・秘密の非同梱確認（`oauth.env`/鍵/`allowed-emails.txt` は git-ignore 済を再監査）・履歴に秘密が無いか点検・再現ビルド（タグ→image のピン）。ライセンス確定は初回公開前。

### 段5 — 第2デプロイ検証（完了ゲート）
- クリーンな環境（別ホスト or 別ユーザー名前空間）に**リリースバンドルだけ**でゼロから設置し、login→tenant→workspace→session を E2E 通過。ここで見つかる「ホスト前提の漏れ」（パス・権限・PATH・docker グループ）を潰す。
- 完了条件: **手順書どおりに他人が立てられる**。これを以て Phase 3 完了判定。

## 20.6 スコープ外（後続）

- P3-7 AWS アダプタ（Runtime IF 抽出＋ECS/EFS/RDS/KMS）。段1-2 のコンテナ化・設定集約が土台になる。
- Postgres 既定化 / HA / k8s Helm chart（AWS/大規模希望社向け）。
- auto-start（idle-stop の透明復帰、Console 側自動 resume として後追い）。

## 20.7 リスク

- **docker.sock = ホスト root 相当**（既知の残存リスク）。1デプロイ内は CP 侵害でそのデプロイの分離が崩れるが、会社間は別デプロイゆえ波及しない。socket プロキシは強化オプション。
- **DooD の3制約**（§20.3: host-net / パス同一 / uid+docker gid）のどれか漏れ＝「起動するが agent 到達不可 or home 空」の静かな失敗＝設置時の最頻トラブル。段1 完了条件と runbook のチェックリストで潰す。`DOCKER_GID` は host 依存ゆえ `.env` 変数化。
- **AF_MASTER_KEY 紛失 = 全 crypto-shred**。バックアップ運用と別金庫管理を runbook で最重要注意に。
